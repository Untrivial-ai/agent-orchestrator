package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBaseURL = "https://api.github.com"
	defaultWebBaseURL = "https://github.com"
	maxResponseBytes  = 8 << 20
)

type Config struct {
	AppID         int64
	AppSlug       string
	ClientID      string
	ClientSecret  string
	PrivateKeyPEM string
	PublicURL     string
	APIBaseURL    string
	WebBaseURL    string
}

type Client struct {
	appID        int64
	appSlug      string
	clientID     string
	clientSecret string
	privateKey   *rsa.PrivateKey
	publicURL    string
	apiBaseURL   string
	webBaseURL   string
	httpClient   *http.Client
	now          func() time.Time
}

type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GitHub request returned status %d", e.StatusCode)
}

type Installation struct {
	ID                  int64             `json:"id"`
	Account             InstallationOwner `json:"account"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
	Events              []string          `json:"events"`
	SuspendedAt         *time.Time        `json:"suspended_at"`
}

type App struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

type InstallationOwner struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type Repository struct {
	ID            int64           `json:"id"`
	Owner         RepositoryOwner `json:"owner"`
	Name          string          `json:"name"`
	FullName      string          `json:"full_name"`
	HTMLURL       string          `json:"html_url"`
	CloneURL      string          `json:"clone_url"`
	SSHURL        string          `json:"ssh_url"`
	DefaultBranch string          `json:"default_branch"`
	Visibility    string          `json:"visibility"`
	Private       bool            `json:"private"`
	Archived      bool            `json:"archived"`
	Disabled      bool            `json:"disabled"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type RepositoryOwner struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// UserAccessToken redacts GitHub user-to-server credentials from formatting
// and JSON. Callers should encrypt it immediately.
type UserAccessToken struct {
	value            string
	refreshValue     string
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
}

func (token UserAccessToken) Token() string        { return token.value }
func (token UserAccessToken) RefreshToken() string { return token.refreshValue }
func (UserAccessToken) String() string             { return "[REDACTED GitHub user token]" }
func (UserAccessToken) GoString() string           { return "[REDACTED GitHub user token]" }

type installationAccessToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func InstallationSupportsAuthorityProof(installation Installation) bool {
	switch installation.Account.Type {
	case "User":
		return true
	case "Organization":
		permission := installation.Permissions["members"]
		return permission == "read" || permission == "write"
	default:
		return false
	}
}

func New(config Config, httpClient *http.Client) (*Client, error) {
	if config.AppID <= 0 || strings.TrimSpace(config.AppSlug) == "" ||
		strings.TrimSpace(config.ClientID) == "" || config.ClientSecret == "" ||
		strings.TrimSpace(config.PublicURL) == "" {
		return nil, errors.New("GitHub App configuration is incomplete")
	}
	privateKey, err := parsePrivateKey(config.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	apiBaseURL := strings.TrimRight(config.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	webBaseURL := strings.TrimRight(config.WebBaseURL, "/")
	if webBaseURL == "" {
		webBaseURL = defaultWebBaseURL
	}
	return &Client{
		appID:        config.AppID,
		appSlug:      strings.TrimSpace(config.AppSlug),
		clientID:     strings.TrimSpace(config.ClientID),
		clientSecret: config.ClientSecret,
		privateKey:   privateKey,
		publicURL:    strings.TrimRight(config.PublicURL, "/"),
		apiBaseURL:   apiBaseURL,
		webBaseURL:   webBaseURL,
		httpClient:   httpClient,
		now:          time.Now,
	}, nil
}

func (c *Client) InstallationURL(state string) string {
	query := url.Values{"state": {state}}
	return c.webBaseURL + "/apps/" + url.PathEscape(c.appSlug) +
		"/installations/new?" + query.Encode()
}

func (c *Client) Check(ctx context.Context) error {
	var app App
	if err := c.appJSON(ctx, http.MethodGet, "/app", nil, &app); err != nil {
		return err
	}
	if app.ID != c.appID || app.Slug != c.appSlug {
		return errors.New("GitHub returned a different App identity")
	}
	return nil
}

func (c *Client) OAuthURL(state, challenge string) string {
	query := url.Values{
		"client_id":             {c.clientID},
		"redirect_uri":          {c.OAuthCallbackURL()},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return c.webBaseURL + "/login/oauth/authorize?" + query.Encode()
}

func (c *Client) SetupCallbackURL() string {
	return c.publicURL + "/api/cloud/v1/github/install/setup"
}

func (c *Client) OAuthCallbackURL() string {
	return c.publicURL + "/api/cloud/v1/github/oauth/callback"
}

func (c *Client) UserOAuthCallbackURL() string {
	return c.publicURL + "/api/cloud/v1/github/user/callback"
}

func (c *Client) UserAuthorizationURL(state, challenge string) string {
	query := url.Values{
		"client_id":             {c.clientID},
		"redirect_uri":          {c.UserOAuthCallbackURL()},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	return c.webBaseURL + "/login/oauth/authorize?" + query.Encode()
}

func (c *Client) GetInstallation(ctx context.Context, installationID int64) (Installation, error) {
	var installation Installation
	err := c.appJSON(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(installationID, 10), nil, &installation)
	return installation, err
}

func (c *Client) ExchangeOAuthCode(ctx context.Context, code, verifier string) (string, error) {
	payload := map[string]string{
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"code":          code,
		"redirect_uri":  c.OAuthCallbackURL(),
		"code_verifier": verifier,
	}
	var response struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := c.jsonRequest(
		ctx,
		http.MethodPost,
		c.webBaseURL+"/login/oauth/access_token",
		"",
		payload,
		&response,
	); err != nil {
		return "", err
	}
	if response.Error != "" || response.AccessToken == "" {
		return "", errors.New("GitHub OAuth exchange was rejected")
	}
	return response.AccessToken, nil
}

func (c *Client) ExchangeUserCode(
	ctx context.Context,
	code, verifier string,
) (UserAccessToken, error) {
	return c.exchangeUserToken(ctx, map[string]string{
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"code":          strings.TrimSpace(code),
		"redirect_uri":  c.UserOAuthCallbackURL(),
		"code_verifier": strings.TrimSpace(verifier),
	})
}

func (c *Client) RefreshUserAccessToken(
	ctx context.Context,
	refreshToken string,
) (UserAccessToken, error) {
	return c.exchangeUserToken(ctx, map[string]string{
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": strings.TrimSpace(refreshToken),
	})
}

func (c *Client) exchangeUserToken(
	ctx context.Context,
	payload map[string]string,
) (UserAccessToken, error) {
	for key, value := range payload {
		if strings.TrimSpace(value) == "" {
			return UserAccessToken{}, fmt.Errorf("GitHub user token %s is required", key)
		}
	}
	var response struct {
		AccessToken           string `json:"access_token"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshToken          string `json:"refresh_token"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
	}
	if err := c.jsonRequest(
		ctx,
		http.MethodPost,
		c.webBaseURL+"/login/oauth/access_token",
		"",
		payload,
		&response,
	); err != nil {
		return UserAccessToken{}, err
	}
	response.AccessToken = strings.TrimSpace(response.AccessToken)
	if response.Error != "" || response.AccessToken == "" {
		return UserAccessToken{}, errors.New("GitHub user authorization was rejected")
	}
	token := UserAccessToken{
		value:        response.AccessToken,
		refreshValue: strings.TrimSpace(response.RefreshToken),
	}
	if response.ExpiresIn > 0 {
		expiresAt := c.now().Add(time.Duration(response.ExpiresIn) * time.Second)
		token.ExpiresAt = &expiresAt
	}
	if response.RefreshTokenExpiresIn > 0 {
		expiresAt := c.now().Add(time.Duration(response.RefreshTokenExpiresIn) * time.Second)
		token.RefreshExpiresAt = &expiresAt
	}
	if token.refreshValue != "" && token.RefreshExpiresAt == nil {
		return UserAccessToken{}, errors.New("GitHub omitted the refresh token expiry")
	}
	return token, nil
}

func (c *Client) GetUser(ctx context.Context, token string) (User, error) {
	var user User
	if err := c.userJSON(ctx, token, http.MethodGet, "/user", nil, &user); err != nil {
		return User{}, err
	}
	if user.ID <= 0 || strings.TrimSpace(user.Login) == "" {
		return User{}, errors.New("GitHub returned an invalid user")
	}
	return user, nil
}

func (c *Client) ListUserInstallations(
	ctx context.Context,
	token string,
) ([]Installation, error) {
	var installations []Installation
	for page := 1; page <= 100; page++ {
		var response struct {
			Installations []Installation `json:"installations"`
		}
		path := fmt.Sprintf("/user/installations?per_page=100&page=%d", page)
		if err := c.userJSON(
			ctx,
			token,
			http.MethodGet,
			path,
			nil,
			&response,
		); err != nil {
			return nil, err
		}
		installations = append(installations, response.Installations...)
		if len(response.Installations) < 100 {
			return installations, nil
		}
	}
	return nil, errors.New("GitHub user installation pagination exceeded limit")
}

func (c *Client) RevokeUserAuthorization(
	ctx context.Context,
	token string,
) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("GitHub user token is required")
	}
	endpoint := c.apiBaseURL + "/applications/" +
		url.PathEscape(c.clientID) + "/grant"
	body, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodDelete,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.SetBasicAuth(c.clientID, c.clientSecret)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("revoke GitHub user authorization: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotFound {
		return nil
	}
	return &HTTPError{StatusCode: response.StatusCode}
}

func (c *Client) CreateRepositoryAsUser(
	ctx context.Context,
	token, accountLogin, accountType, name string,
	private bool,
) (Repository, error) {
	accountLogin = strings.TrimSpace(accountLogin)
	name = strings.TrimSpace(name)
	path := "/user/repos"
	switch strings.ToLower(strings.TrimSpace(accountType)) {
	case "user":
	case "organization":
		path = "/orgs/" + url.PathEscape(accountLogin) + "/repos"
	default:
		return Repository{}, errors.New("unsupported GitHub account type")
	}
	if accountLogin == "" || name == "" {
		return Repository{}, errors.New("GitHub repository owner and name are required")
	}
	var repository Repository
	if err := c.userJSON(ctx, token, http.MethodPost, path, map[string]any{
		"name":      name,
		"private":   private,
		"auto_init": true,
	}, &repository); err != nil {
		return Repository{}, err
	}
	return repository, nil
}

// PullRequestResponse is the GitHub API's pull request shape, trimmed to the
// fields this client needs.
type PullRequestResponse struct {
	ID           int64  `json:"id"`
	Number       int    `json:"number"`
	HTMLURL      string `json:"html_url"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	Title        string `json:"title"`
	User         User   `json:"user"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changed_files"`
	Head         struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// GetPullRequestRecord fetches the full pull request fields required to
// durably claim a PR that was created by worker-side tooling.
func (c *Client) GetPullRequestRecord(
	ctx context.Context,
	token, owner, repo string,
	number int,
) (PullRequestResponse, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" || number <= 0 {
		return PullRequestResponse{}, errors.New("pull request owner, repo, and number are required")
	}
	var pullRequest PullRequestResponse
	if err := c.userJSON(
		ctx,
		token,
		http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls/"+strconv.Itoa(number),
		nil,
		&pullRequest,
	); err != nil {
		return PullRequestResponse{}, err
	}
	if pullRequest.Number != number || pullRequest.HTMLURL == "" || pullRequest.Head.SHA == "" ||
		pullRequest.Head.Ref == "" || pullRequest.Base.Ref == "" {
		return PullRequestResponse{}, errors.New("GitHub returned an incomplete pull request response")
	}
	return pullRequest, nil
}

// CreatePullRequestInput is the request to open a pull request.
type CreatePullRequestInput struct {
	Title string
	Body  string
	Head  string
	Base  string
}

// CreatePullRequest opens a pull request using an installation access token
// (from repositoryWriteToken, not repositoryToken — this needs pull_requests
// write, not just contents read).
func (c *Client) CreatePullRequest(
	ctx context.Context,
	token, owner, repo string,
	input CreatePullRequestInput,
) (PullRequestResponse, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	head := strings.TrimSpace(input.Head)
	base := strings.TrimSpace(input.Base)
	title := strings.TrimSpace(input.Title)
	if owner == "" || repo == "" || head == "" || base == "" || title == "" {
		return PullRequestResponse{}, errors.New(
			"pull request owner, repo, head, base, and title are required",
		)
	}
	var pr PullRequestResponse
	if err := c.userJSON(
		ctx,
		token,
		http.MethodPost,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls",
		map[string]any{
			"title": title,
			"body":  input.Body,
			"head":  head,
			"base":  base,
		},
		&pr,
	); err != nil {
		return PullRequestResponse{}, err
	}
	if pr.Number <= 0 || pr.HTMLURL == "" || pr.Head.SHA == "" {
		return PullRequestResponse{}, errors.New(
			"GitHub returned an incomplete pull request response",
		)
	}
	return pr, nil
}

func (c *Client) GetRepositoryAsUser(
	ctx context.Context,
	token, owner, name string,
) (Repository, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return Repository{}, errors.New("GitHub repository owner and name are required")
	}
	var repository Repository
	if err := c.userJSON(
		ctx,
		token,
		http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name),
		nil,
		&repository,
	); err != nil {
		return Repository{}, err
	}
	return repository, nil
}

func (c *Client) DeleteRepositoryAsUser(
	ctx context.Context,
	token, owner, name string,
) error {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return errors.New("GitHub repository owner and name are required")
	}
	return c.userJSON(
		ctx,
		token,
		http.MethodDelete,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name),
		nil,
		nil,
	)
}

func (c *Client) UserHasInstallation(ctx context.Context, accessToken string, installationID int64) (bool, error) {
	for page := 1; page <= 20; page++ {
		var response struct {
			Installations []Installation `json:"installations"`
		}
		path := fmt.Sprintf("/user/installations?per_page=100&page=%d", page)
		if err := c.userJSON(ctx, accessToken, http.MethodGet, path, nil, &response); err != nil {
			return false, err
		}
		for _, installation := range response.Installations {
			if installation.ID == installationID {
				return true, nil
			}
		}
		if len(response.Installations) < 100 {
			return false, nil
		}
	}
	return false, errors.New("GitHub user installation pagination exceeded limit")
}

func (c *Client) UserCanAdministerInstallation(
	ctx context.Context,
	accessToken string,
	installation Installation,
) (bool, error) {
	var user struct {
		ID int64 `json:"id"`
	}
	if err := c.userJSON(
		ctx,
		accessToken,
		http.MethodGet,
		"/user",
		nil,
		&user,
	); err != nil {
		return false, err
	}
	switch installation.Account.Type {
	case "User":
		return user.ID == installation.Account.ID, nil
	case "Organization":
		var membership struct {
			State string `json:"state"`
			Role  string `json:"role"`
		}
		path := "/user/memberships/orgs/" + url.PathEscape(installation.Account.Login)
		if err := c.userJSON(
			ctx,
			accessToken,
			http.MethodGet,
			path,
			nil,
			&membership,
		); err != nil {
			return false, err
		}
		return membership.State == "active" && membership.Role == "admin", nil
	default:
		// Enterprise installation administration requires a separate enterprise
		// role proof, so it is denied until that proof is implemented.
		return false, nil
	}
}

func (c *Client) ListRepositories(ctx context.Context, installationID int64) ([]Repository, error) {
	token, err := c.installationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var repositories []Repository
	for page := 1; page <= 100; page++ {
		var response struct {
			Repositories []Repository `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page)
		if err := c.userJSON(ctx, token, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		repositories = append(repositories, response.Repositories...)
		if len(response.Repositories) < 100 {
			return repositories, nil
		}
	}
	return nil, errors.New("GitHub repository pagination exceeded limit")
}

func (c *Client) installationToken(ctx context.Context, installationID int64) (string, error) {
	response, err := c.createInstallationToken(ctx, installationID, map[string]any{})
	if err != nil {
		return "", err
	}
	return response.Token, nil
}

func (c *Client) repositoryToken(
	ctx context.Context,
	installationID, repositoryID int64,
) (installationAccessToken, error) {
	if installationID <= 0 || repositoryID <= 0 {
		return installationAccessToken{}, errors.New("GitHub installation token scope is invalid")
	}
	response, err := c.createInstallationToken(ctx, installationID, map[string]any{
		"repository_ids": []int64{repositoryID},
		"permissions": map[string]string{
			"contents": "read",
		},
	})
	if err != nil {
		return installationAccessToken{}, err
	}
	if response.ExpiresAt.IsZero() || !response.ExpiresAt.After(c.now()) {
		return installationAccessToken{}, errors.New("GitHub returned an expired installation token")
	}
	return response, nil
}

// repositoryWriteToken mints a short-lived installation token scoped to one
// repository with write access to its contents and pull requests. Unlike
// repositoryToken (contents:read, used for checkout), this is minted only
// right before a push or a pull-request API call and is never handed to a
// worker directly — the control plane holds it for the duration of one
// server-side operation and lets it expire otherwise.
func (c *Client) repositoryWriteToken(
	ctx context.Context,
	installationID, repositoryID int64,
) (installationAccessToken, error) {
	if installationID <= 0 || repositoryID <= 0 {
		return installationAccessToken{}, errors.New("GitHub installation token scope is invalid")
	}
	response, err := c.createInstallationToken(ctx, installationID, map[string]any{
		"repository_ids": []int64{repositoryID},
		"permissions": map[string]string{
			"contents":      "write",
			"pull_requests": "write",
		},
	})
	if err != nil {
		return installationAccessToken{}, err
	}
	if response.ExpiresAt.IsZero() || !response.ExpiresAt.After(c.now()) {
		return installationAccessToken{}, errors.New("GitHub returned an expired installation token")
	}
	return response, nil
}

// statusReadToken mints a short-lived installation token scoped to one
// repository with read access to pull requests and checks — the permissions
// GitHub's fine-grained token model requires to fetch PR/review/check-run
// detail, distinct from repositoryToken's contents:read (used for checkout).
func (c *Client) statusReadToken(
	ctx context.Context,
	installationID, repositoryID int64,
) (installationAccessToken, error) {
	if installationID <= 0 || repositoryID <= 0 {
		return installationAccessToken{}, errors.New("GitHub installation token scope is invalid")
	}
	response, err := c.createInstallationToken(ctx, installationID, map[string]any{
		"repository_ids": []int64{repositoryID},
		"permissions": map[string]string{
			"pull_requests": "read",
			"checks":        "read",
		},
	})
	if err != nil {
		return installationAccessToken{}, err
	}
	if response.ExpiresAt.IsZero() || !response.ExpiresAt.After(c.now()) {
		return installationAccessToken{}, errors.New("GitHub returned an expired installation token")
	}
	return response, nil
}

// PullRequestDetail is the subset of GitHub's pull request detail response
// used to refresh a tracked pull request's lifecycle and mergeability.
type PullRequestDetail struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeableState string `json:"mergeable_state"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// GetPullRequest fetches one pull request's current lifecycle and
// mergeability state.
func (c *Client) GetPullRequest(
	ctx context.Context,
	token, owner, repo string,
	number int,
) (PullRequestDetail, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" || number <= 0 {
		return PullRequestDetail{}, errors.New("pull request owner, repo, and number are required")
	}
	var detail PullRequestDetail
	if err := c.userJSON(
		ctx, token, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls/"+strconv.Itoa(number),
		nil, &detail,
	); err != nil {
		return PullRequestDetail{}, err
	}
	if detail.Number <= 0 {
		return PullRequestDetail{}, errors.New("GitHub returned an incomplete pull request response")
	}
	return detail, nil
}

// CheckRun is one GitHub Checks API run against a commit.
type CheckRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// ListCheckRuns returns every check run GitHub has recorded against ref
// (typically a pull request's head SHA), most recent GitHub Checks API page
// only — sufficient to aggregate an overall CI state.
func (c *Client) ListCheckRuns(
	ctx context.Context,
	token, owner, repo, ref string,
) ([]CheckRun, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	ref = strings.TrimSpace(ref)
	if owner == "" || repo == "" || ref == "" {
		return nil, errors.New("check run owner, repo, and ref are required")
	}
	var response struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	if err := c.userJSON(
		ctx, token, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/commits/"+url.PathEscape(ref)+"/check-runs?per_page=100",
		nil, &response,
	); err != nil {
		return nil, err
	}
	return response.CheckRuns, nil
}

// PullRequestReview is one submitted review on a pull request. ID and
// SubmittedAt exist to order a reviewer's reviews chronologically — GitHub
// returns every review event ever submitted, not just each reviewer's
// current standing verdict.
type PullRequestReview struct {
	ID          int64     `json:"id"`
	User        User      `json:"user"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// ListPullRequestReviews returns every review submitted on a pull request.
func (c *Client) ListPullRequestReviews(
	ctx context.Context,
	token, owner, repo string,
	number int,
) ([]PullRequestReview, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" || number <= 0 {
		return nil, errors.New("pull request review owner, repo, and number are required")
	}
	var reviews []PullRequestReview
	if err := c.userJSON(
		ctx, token, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls/"+strconv.Itoa(number)+"/reviews?per_page=100",
		nil, &reviews,
	); err != nil {
		return nil, err
	}
	return reviews, nil
}

// pullRequestReviewResponse is the subset of GitHub's create-review response
// this client needs.
type pullRequestReviewResponse struct {
	ID int64 `json:"id"`
}

// CreatePullRequestReview posts a review comment on a pull request. It
// always submits event "COMMENT" — GitHub refuses to let the same identity
// that opened a pull request APPROVE or REQUEST_CHANGES on it, so a comment
// is the only decisive-looking event this identity can actually post; the
// verdict itself lives in the comment body and in AO's own review-run
// record, not in GitHub's native review state.
func (c *Client) CreatePullRequestReview(
	ctx context.Context,
	token, owner, repo string,
	number int,
	body string,
) (int64, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" || number <= 0 || strings.TrimSpace(body) == "" {
		return 0, errors.New("pull request review owner, repo, number, and body are required")
	}
	var review pullRequestReviewResponse
	if err := c.userJSON(
		ctx, token, http.MethodPost,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls/"+strconv.Itoa(number)+"/reviews",
		map[string]any{"body": body, "event": "COMMENT"},
		&review,
	); err != nil {
		return 0, err
	}
	if review.ID <= 0 {
		return 0, errors.New("GitHub returned an incomplete pull request review response")
	}
	return review.ID, nil
}

func (c *Client) createInstallationToken(
	ctx context.Context,
	installationID int64,
	body map[string]any,
) (installationAccessToken, error) {
	var response installationAccessToken
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	if err := c.appJSON(ctx, http.MethodPost, path, body, &response); err != nil {
		return installationAccessToken{}, err
	}
	if response.Token == "" {
		return installationAccessToken{}, errors.New("GitHub returned an empty installation token")
	}
	return response, nil
}

func (c *Client) appJSON(ctx context.Context, method, path string, body, destination any) error {
	token, err := c.appJWT()
	if err != nil {
		return err
	}
	return c.jsonRequest(ctx, method, c.apiBaseURL+path, "Bearer "+token, body, destination)
}

func (c *Client) userJSON(ctx context.Context, token, method, path string, body, destination any) error {
	return c.jsonRequest(ctx, method, c.apiBaseURL+path, "Bearer "+token, body, destination)
}

func (c *Client) jsonRequest(
	ctx context.Context,
	method, endpoint, authorization string,
	body, destination any,
) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("GitHub request: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return errors.New("GitHub response exceeded size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{StatusCode: response.StatusCode}
	}
	if destination == nil {
		return nil
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return errors.New("GitHub returned invalid JSON")
	}
	return nil
}

func (c *Client) appJWT() (string, error) {
	now := c.now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": c.appID,
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("GitHub App private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("GitHub App private key is invalid")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key must be RSA")
	}
	return key, nil
}
