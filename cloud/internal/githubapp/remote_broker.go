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
)

const brokerResponseLimit = 64 << 10

var ErrCapabilityRejected = errors.New("repository capability rejected")

type RemoteCapabilityStore interface {
	WorkerRemoteGitHubCheckoutContext(
		context.Context,
		string,
		string,
	) (domain.RemoteGitHubCheckoutContext, error)
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
var errRemotePushNotSupported = errors.New(
	"raising a pull request is not supported for a repository authorized through the remote capability broker yet",
)

func (b *RemoteCheckoutBroker) IssuePushGrant(
	context.Context,
	string, string,
) (CheckoutGrant, error) {
	return CheckoutGrant{}, errRemotePushNotSupported
}

func (b *RemoteCheckoutBroker) RaisePullRequest(
	context.Context,
	string, string,
	domain.RaisePullRequest,
) (domain.PullRequest, error) {
	return domain.PullRequest{}, errRemotePushNotSupported
}

func (b *RemoteCheckoutBroker) ClaimPullRequest(
	context.Context,
	string, string, string,
) (domain.PullRequest, error) {
	return domain.PullRequest{}, errRemotePushNotSupported
}

func (b *RemoteCheckoutBroker) SubmitReview(
	context.Context,
	string, string, string,
	domain.SubmitReviewResult,
) (domain.ReviewRun, error) {
	return domain.ReviewRun{}, errRemotePushNotSupported
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
