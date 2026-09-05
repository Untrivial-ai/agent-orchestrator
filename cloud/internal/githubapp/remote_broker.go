package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

const brokerResponseLimit = 64 << 10

var ErrCapabilityRejected = errors.New("repository capability rejected")

type RemoteCapabilityStore interface {
    WorkerRemoteGitHubCheckoutContext(
        context.Context,
        string,
        string,
    ) (domain.RemoteGitHubCheckoutContext, error)
    CreatePullRequestRecord(
        ctx context.Context,
        orgID, sessionID string,
        provider, repository, author string,
        number int,
        url, sourceBranch, targetBranch, headSHA, title string,
        additions, deletions, changedFiles int,
    ) (domain.PullRequest, error)
    ClaimPullRequestRecord(
        ctx context.Context,
        orgID, sessionID string,
        input domain.PullRequest,
    ) (domain.PullRequest, error)
    CompleteAndDeliverReviewRun(
        ctx context.Context,
        orgID, reviewRunID, reviewSessionID string,
        result domain.SubmitReviewResult,
        providerReviewID string,
    ) (domain.ReviewRun, error)
	ReviewRunPullRequest(
		ctx context.Context,
		orgID, reviewRunID string,
	) (domain.ReviewRunPullRequest, error)
}


type RemoteCheckoutBroker struct {
	store       RemoteCapabilityStore
	cipher      *secrets.Cipher
	baseURL     string
	environment string
	authToken   string
	httpClient  *http.Client
}

type BrokerRepository struct {
	GitHubRepositoryID string     `json:"githubRepositoryId"`
	GitHubOwnerID      string     `json:"githubOwnerId"`
	Name               string     `json:"name"`
	FullName           string     `json:"fullName"`
	HTMLURL            string     `json:"htmlUrl"`
	CloneURL           string     `json:"cloneUrl"`
	SSHURL             string     `json:"sshUrl"`
	DefaultBranch      string     `json:"defaultBranch"`
	Visibility         string     `json:"visibility"`
	IsPrivate          bool       `json:"isPrivate"`
	IsArchived         bool       `json:"isArchived"`
	IsDisabled         bool       `json:"isDisabled"`
	GitHubUpdatedAt    *time.Time `json:"githubUpdatedAt,omitempty"`
}

func ToBrokerRepository(repository domain.GitHubRepository) BrokerRepository {
	return BrokerRepository{
		GitHubRepositoryID: strconv.FormatInt(repository.GitHubRepositoryID, 10),
		GitHubOwnerID:      strconv.FormatInt(repository.GitHubOwnerID, 10),
		Name:               repository.Name,
		FullName:           repository.FullName,
		HTMLURL:            repository.HTMLURL,
		CloneURL:           repository.CloneURL,
		SSHURL:             repository.SSHURL,
		DefaultBranch:      repository.DefaultBranch,
		Visibility:         repository.Visibility,
		IsPrivate:          repository.IsPrivate,
		IsArchived:         repository.IsArchived,
		IsDisabled:         repository.IsDisabled,
		GitHubUpdatedAt:    repository.GitHubUpdatedAt,
	}
}

func NewRemoteCheckoutBroker(
	store RemoteCapabilityStore,
	cipher *secrets.Cipher,
	baseURL, environment, authToken string,
	httpClient *http.Client,
) (*RemoteCheckoutBroker, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("repository broker URL must be an HTTPS origin")
	}
	if store == nil || cipher == nil ||
		!validCapabilityEnvironment(environment) ||
		len(strings.TrimSpace(authToken)) < 32 {
		return nil, errors.New("repository broker configuration is incomplete")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &RemoteCheckoutBroker{
		store:       store,
		cipher:      cipher,
		baseURL:     parsed.String(),
		environment: environment,
		authToken:   authToken,
		httpClient:  httpClient,
	}, nil
}

func (b *RemoteCheckoutBroker) IssueCheckoutGrant(
	ctx context.Context,
	orgID, sessionID string,
) (CheckoutGrant, error) {
	authorization, err := b.store.WorkerRemoteGitHubCheckoutContext(
		ctx,
		orgID,
		sessionID,
	)
	if err != nil {
		return CheckoutGrant{}, err
	}
	if authorization.OrgID != orgID ||
		authorization.SessionID != sessionID ||
		authorization.ProjectID == "" ||
		authorization.TargetEnvironment != b.environment ||
		authorization.GitHubInstallationID <= 0 ||
		authorization.GitHubRepositoryID <= 0 {
		return CheckoutGrant{}, errors.New("remote repository authority is invalid")
	}
	plaintext, err := b.cipher.Decrypt(
		authorization.CapabilityCiphertext,
		authorization.CapabilityNonce,
		RepositoryCapabilityAssociatedData(authorization),
	)
	if err != nil {
		return CheckoutGrant{}, err
	}
	defer clear(plaintext)
	body, err := json.Marshal(map[string]any{
		"capability":           string(plaintext),
		"githubInstallationId": strconv.FormatInt(authorization.GitHubInstallationID, 10),
		"githubRepositoryId":   strconv.FormatInt(authorization.GitHubRepositoryID, 10),
		"userExternalId":       authorization.UserExternalID,
	})
	if err != nil {
		return CheckoutGrant{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		b.baseURL+"/api/cloud/v1/control/github/capabilities/redeem",
		bytes.NewReader(body),
	)
	if err != nil {
		return CheckoutGrant{}, err
	}
	request.Header.Set("Authorization", "Bearer "+b.authToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-AO-Target-Environment", b.environment)
	response, err := b.httpClient.Do(request)
	if err != nil {
		return CheckoutGrant{}, fmt.Errorf("redeem repository capability: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, brokerResponseLimit))
		return CheckoutGrant{}, fmt.Errorf(
			"repository capability broker returned status %d",
			response.StatusCode,
		)
	}
	var grant CheckoutGrant
	if err := json.NewDecoder(io.LimitReader(response.Body, brokerResponseLimit)).Decode(&grant); err != nil {
		return CheckoutGrant{}, fmt.Errorf("decode repository checkout grant: %w", err)
	}
	expectedClone := strings.TrimSuffix(authorization.RepositoryURL, "/") + ".git"
	if grant.Token == "" ||
		!grant.ExpiresAt.After(time.Now().UTC()) ||
		!strings.EqualFold(grant.CloneURL, expectedClone) {
		return CheckoutGrant{}, errors.New("repository capability broker returned an invalid grant")
	}
	return grant, nil
}

// errRemotePushNotSupported is returned by IssuePushGrant and
// RaisePullRequest: pushing and raising pull requests remotely would need a
// production-side capability-redemption endpoint of their own (mirroring
// /control/github/capabilities/redeem), which does not exist yet. This
// environment can still check out and read a repository through the
// existing redeem endpoint above — only writing back to GitHub is
// unsupported here for now.
var ErrorRemoteWriteNotSupported = errors.New(
	"Write operation are not supported for repository authorized thorugh the remote capability broker",
)

func (b *RemoteCheckoutBroker) IssuePushGrant(
    ctx context.Context,
    orgID, sessionID string,
) (CheckoutGrant, error) {
    authorization, err := b.store.WorkerRemoteGitHubCheckoutContext(
        ctx,
        orgID,
        sessionID,
    )
    if err != nil {
        return CheckoutGrant{}, err
    }
    if authorization.OrgID != orgID ||
        authorization.SessionID != sessionID ||
        authorization.ProjectID == "" ||
        authorization.TargetEnvironment != b.environment ||
        authorization.GitHubInstallationID <= 0 ||
        authorization.GitHubRepositoryID <= 0 {
        return CheckoutGrant{}, errors.New("remote repository authority is invalid")
    }
    plaintext, err := b.cipher.Decrypt(
        authorization.CapabilityCiphertext,
        authorization.CapabilityNonce,
        RepositoryCapabilityAssociatedData(authorization),
    )
    if err != nil {
        return CheckoutGrant{}, err
    }
    defer clear(plaintext)
    body, err := json.Marshal(map[string]any{
        "capability":           string(plaintext),
        "githubInstallationId": strconv.FormatInt(authorization.GitHubInstallationID, 10),
        "githubRepositoryId":   strconv.FormatInt(authorization.GitHubRepositoryID, 10),
        "userExternalId":       authorization.UserExternalID,
    })
    if err != nil {
        return CheckoutGrant{}, err
    }
    request, err := http.NewRequestWithContext(
        ctx,
        http.MethodPost,
        b.baseURL+"/api/cloud/v1/control/github/capabilities/redeem-write", 
        bytes.NewReader(body),
    )
    if err != nil {
        return CheckoutGrant{}, err
    }
    request.Header.Set("Authorization", "Bearer "+b.authToken)
    request.Header.Set("Content-Type", "application/json")
    request.Header.Set("X-AO-Target-Environment", b.environment)
    response, err := b.httpClient.Do(request)
    if err != nil {
        return CheckoutGrant{}, fmt.Errorf("redeem repository write capability: %w", err)
    }
    defer response.Body.Close()
    if response.StatusCode != http.StatusOK {
        _, _ = io.Copy(io.Discard, io.LimitReader(response.Body, brokerResponseLimit))
        return CheckoutGrant{}, fmt.Errorf(
            "repository write capability broker returned status %d",
            response.StatusCode,
        )
    }
    var grant CheckoutGrant
    if err := json.NewDecoder(io.LimitReader(response.Body, brokerResponseLimit)).Decode(&grant); err != nil {
        return CheckoutGrant{}, fmt.Errorf("decode repository write grant: %w", err)
    }
    expectedClone := strings.TrimSuffix(authorization.RepositoryURL, "/") + ".git"
    if grant.Token == "" ||
        !grant.ExpiresAt.After(time.Now().UTC()) ||
        !strings.EqualFold(grant.CloneURL, expectedClone) {
        return CheckoutGrant{}, errors.New("repository write capability broker returned an invalid grant")
    }
    return grant, nil
}


func (b *RemoteCheckoutBroker) RaisePullRequest(
    ctx context.Context,
    orgID, sessionID string,
    input domain.RaisePullRequest,
) (domain.PullRequest, error) {
    title := strings.TrimSpace(input.Title)
    head := strings.TrimSpace(input.HeadBranch)
    base := strings.TrimSpace(input.BaseBranch)
    if title == "" || head == "" || base == "" {
        return domain.PullRequest{}, fmt.Errorf("%w: title, head branch, and base branch are required", postgres.ErrInvalid)
    }
    grant, err := b.IssuePushGrant(ctx, orgID, sessionID)
    if err != nil {
        return domain.PullRequest{}, err
    }
    // Parse owner/repo from clone URL (https://github.com/owner/repo.git)
    cloneURL := strings.TrimSuffix(strings.TrimRight(grant.CloneURL, "/"), ".git")
    parts := strings.Split(strings.Trim(cloneURL, "/"), "/")
    if len(parts) < 2 {
        return domain.PullRequest{}, errors.New("could not parse repository identity from clone URL")
    }
    owner := parts[len(parts)-2]
    repo  := parts[len(parts)-1]
    var pr struct {
        Number   int    `json:"number"`
        HTMLURL  string `json:"html_url"`
        Title    string `json:"title"`
        User     struct {
            Login string `json:"login"`
        } `json:"user"`
        Head struct {
            SHA string `json:"sha"`
            Ref string `json:"ref"`
        } `json:"head"`
        Additions    int `json:"additions"`
        Deletions    int `json:"deletions"`
        ChangedFiles int `json:"changed_files"`
    }
    path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls"
    if err := b.githubJSON(ctx, http.MethodPost, path, grant.Token, map[string]any{
        "title": title,
        "body":  input.Body,
        "head":  head,
        "base":  base,
    }, &pr); err != nil {
        if errors.Is(err, errInvalidPullRequest) {
            return domain.PullRequest{}, postgres.ErrInvalid
        }
        return domain.PullRequest{}, err
    }
    if pr.Number <= 0 || pr.HTMLURL == "" || pr.Head.SHA == "" {
        return domain.PullRequest{}, errors.New("GitHub returned an incomplete pull request response")
    }
    return b.store.CreatePullRequestRecord(
        ctx,
        orgID, sessionID,
        "github", owner+"/"+repo, pr.User.Login,
        pr.Number, pr.HTMLURL, head, base, pr.Head.SHA, title,
        pr.Additions, pr.Deletions, pr.ChangedFiles,
    )
}

func (b *RemoteCheckoutBroker) ClaimPullRequest(
    ctx context.Context,
    orgID, sessionID, reference string,
) (domain.PullRequest, error) {
    grant, err := b.IssuePushGrant(ctx, orgID, sessionID)
    if err != nil {
        return domain.PullRequest{}, err
    }
    cloneURL := strings.TrimSuffix(strings.TrimRight(grant.CloneURL, "/"), ".git")
    parts := strings.Split(strings.Trim(cloneURL, "/"), "/")
    if len(parts) < 2 {
        return domain.PullRequest{}, errors.New("could not parse repository identity from clone URL")
    }
    owner    := parts[len(parts)-2]
    repo     := parts[len(parts)-1]
    fullName := owner + "/" + repo
    number, err := parsePullRequestReference(reference, fullName)
    if err != nil {
        return domain.PullRequest{}, err
    }
    var pr struct {
        Number   int    `json:"number"`
        HTMLURL  string `json:"html_url"`
        State    string `json:"state"`
        Draft    bool   `json:"draft"`
        Title    string `json:"title"`
        User     struct {
            Login string `json:"login"`
        } `json:"user"`
        Head struct {
            SHA string `json:"sha"`
            Ref string `json:"ref"`
        } `json:"head"`
        Base struct {
            Ref string `json:"ref"`
        } `json:"base"`
        Additions    int `json:"additions"`
        Deletions    int `json:"deletions"`
        ChangedFiles int `json:"changed_files"`
    }
    path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls/" + strconv.Itoa(number)
    if err := b.githubJSON(ctx, http.MethodGet, path, grant.Token, nil, &pr); err != nil {
        return domain.PullRequest{}, err
    }
    if pr.Number != number || pr.HTMLURL == "" || pr.Head.SHA == "" ||
        pr.Head.Ref == "" || pr.Base.Ref == "" {
        return domain.PullRequest{}, postgres.ErrInvalid
    }
    return b.store.ClaimPullRequestRecord(ctx, orgID, sessionID, domain.PullRequest{
        Provider:     "github",
        Repository:   fullName,
        Author:       pr.User.Login,
        Number:       pr.Number,
        URL:          pr.HTMLURL,
        Title:        pr.Title,
        Draft:        pr.Draft,
        HeadSHA:      pr.Head.SHA,
        SourceBranch: pr.Head.Ref,
        TargetBranch: pr.Base.Ref,
        Additions:    pr.Additions,
        Deletions:    pr.Deletions,
        ChangedFiles: pr.ChangedFiles,
    })
}

func (b *RemoteCheckoutBroker) SubmitReview(
    ctx context.Context,
    orgID, sessionID, reviewRunID string,
    result domain.SubmitReviewResult,
) (domain.ReviewRun, error) {
    run, err := b.store.ReviewRunPullRequest(ctx, orgID, reviewRunID)
    if err != nil {
        return domain.ReviewRun{}, err
    }
    owner, repo, ok := strings.Cut(run.PullRequestRepository, "/")
    if !ok || owner == "" || repo == "" {
        return domain.ReviewRun{}, postgres.ErrInvalid
    }
    grant, err := b.IssuePushGrant(ctx, orgID, sessionID)
    if err != nil {
        return domain.ReviewRun{}, err
    }
    var review struct {
        ID int64 `json:"id"`
    }
    path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
        "/pulls/" + strconv.Itoa(run.PullRequestNumber) + "/reviews"
    if err := b.githubJSON(ctx, http.MethodPost, path, grant.Token, map[string]any{
        "body":  result.Body,
        "event": "COMMENT",
    }, &review); err != nil {
        return domain.ReviewRun{}, err
    }
    if review.ID <= 0 {
        return domain.ReviewRun{}, errors.New("GitHub returned an incomplete review response")
    }
    return b.store.CompleteAndDeliverReviewRun(
        ctx,
        orgID,
        reviewRunID,
        sessionID,
        result,
        strconv.FormatInt(review.ID, 10),
    )
}

func (b *RemoteCheckoutBroker) ValidateCapability(
	ctx context.Context,
	capability string,
	githubInstallationID, githubRepositoryID int64,
	userExternalID string,
) (domain.GitHubRepositoryCapability, error) {
	body, err := json.Marshal(map[string]any{
		"capability":           capability,
		"githubInstallationId": strconv.FormatInt(githubInstallationID, 10),
		"githubRepositoryId":   strconv.FormatInt(githubRepositoryID, 10),
		"userExternalId":       userExternalID,
	})
	if err != nil {
		return domain.GitHubRepositoryCapability{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		b.baseURL+"/api/cloud/v1/control/github/capabilities/validate",
		bytes.NewReader(body),
	)
	if err != nil {
		return domain.GitHubRepositoryCapability{}, err
	}
	request.Header.Set("Authorization", "Bearer "+b.authToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-AO-Target-Environment", b.environment)
	response, err := b.httpClient.Do(request)
	if err != nil {
		return domain.GitHubRepositoryCapability{}, fmt.Errorf(
			"validate repository capability: %w",
			err,
		)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, brokerResponseLimit))
		if response.StatusCode == http.StatusForbidden ||
			response.StatusCode == http.StatusUnauthorized {
			return domain.GitHubRepositoryCapability{}, ErrCapabilityRejected
		}
		return domain.GitHubRepositoryCapability{}, fmt.Errorf(
			"repository capability validation returned status %d",
			response.StatusCode,
		)
	}
	var wire struct {
		GitHubInstallationID string           `json:"githubInstallationId"`
		GitHubRepositoryID   string           `json:"githubRepositoryId"`
		UserExternalID       string           `json:"userExternalId"`
		TargetEnvironment    string           `json:"targetEnvironment"`
		Repository           BrokerRepository `json:"repository"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, brokerResponseLimit)).Decode(&wire); err != nil {
		return domain.GitHubRepositoryCapability{}, err
	}
	installationID, installationErr := strconv.ParseInt(wire.GitHubInstallationID, 10, 64)
	repositoryID, repositoryErr := strconv.ParseInt(wire.GitHubRepositoryID, 10, 64)
	if installationErr != nil || repositoryErr != nil ||
		installationID != githubInstallationID ||
		repositoryID != githubRepositoryID ||
		wire.UserExternalID != userExternalID ||
		wire.TargetEnvironment != b.environment ||
		wire.Repository.GitHubRepositoryID != strconv.FormatInt(githubRepositoryID, 10) {
		return domain.GitHubRepositoryCapability{}, errors.New(
			"repository capability validation returned mismatched authority",
		)
	}
	ownerID, ownerErr := strconv.ParseInt(wire.Repository.GitHubOwnerID, 10, 64)
	if ownerErr != nil || ownerID <= 0 {
		return domain.GitHubRepositoryCapability{}, errors.New(
			"repository capability validation returned an invalid owner",
		)
	}
	return domain.GitHubRepositoryCapability{
		UserExternalID:       wire.UserExternalID,
		TargetEnvironment:    wire.TargetEnvironment,
		GitHubInstallationID: installationID,
		GitHubRepositoryID:   repositoryID,
		Repository: domain.GitHubRepository{
			GitHubRepositoryID: repositoryID,
			GitHubOwnerID:      ownerID,
			Name:               wire.Repository.Name,
			FullName:           wire.Repository.FullName,
			HTMLURL:            wire.Repository.HTMLURL,
			CloneURL:           wire.Repository.CloneURL,
			SSHURL:             wire.Repository.SSHURL,
			DefaultBranch:      wire.Repository.DefaultBranch,
			Visibility:         wire.Repository.Visibility,
			IsPrivate:          wire.Repository.IsPrivate,
			IsArchived:         wire.Repository.IsArchived,
			IsDisabled:         wire.Repository.IsDisabled,
			GitHubUpdatedAt:    wire.Repository.GitHubUpdatedAt,
		},
	}, nil
}

func RepositoryCapabilityAssociatedData(
	authorization domain.RemoteGitHubCheckoutContext,
) string {
	return strings.Join([]string{
		"github-repository-capability",
		authorization.OrgID,
		authorization.ProjectID,
		strconv.FormatInt(authorization.GitHubInstallationID, 10),
		strconv.FormatInt(authorization.GitHubRepositoryID, 10),
		authorization.UserExternalID,
		authorization.TargetEnvironment,
	}, ":")
}

func validCapabilityEnvironment(value string) bool {
	return value == "development" || value == "staging" || value == "production"
}

func (b *RemoteCheckoutBroker) githubJSON(
    ctx context.Context,
    method, path, token string,
    body, destination any,
) error {
    const apiBase = "https://api.github.com"
    var requestBody io.Reader
    if body != nil {
        encoded, err := json.Marshal(body)
        if err != nil {
            return err
        }
        requestBody = bytes.NewReader(encoded)
    }
    req, err := http.NewRequestWithContext(ctx, method, apiBase+path, requestBody)
    if err != nil {
        return err
    }
    req.Header.Set("Accept", "application/vnd.github+json")
    req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
    req.Header.Set("Authorization", "Bearer "+token)
    if body != nil {
        req.Header.Set("Content-Type", "application/json")
    }
    resp, err := b.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("GitHub API request: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusUnprocessableEntity {
        _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, brokerResponseLimit))
        return fmt.Errorf("GitHub API rejected request: %w", errInvalidPullRequest)
    }
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, brokerResponseLimit))
        return fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
    }
    if destination != nil {
        return json.NewDecoder(io.LimitReader(resp.Body, brokerResponseLimit)).Decode(destination)
    }
    _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, brokerResponseLimit))
    return nil
}

var errInvalidPullRequest = errors.New("pull request branches are invalid")
