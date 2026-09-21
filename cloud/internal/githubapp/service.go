package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/google/uuid"
)

type Store interface {
	CreateGitHubUserAuthAttempt(context.Context, string, []byte, []byte, []byte, time.Time) (domain.GitHubUserAuthAttempt, error)
	GitHubUserAuthAttempt(context.Context, []byte) (domain.GitHubUserAuthAttempt, error)
	CompleteGitHubUserAuthorization(context.Context, []byte, postgres.GitHubUserConnectionInput) (domain.GitHubUserConnection, error)
	GitHubUserConnection(context.Context, string) (domain.GitHubUserConnection, error)
	UpdateGitHubUserConnection(context.Context, string, postgres.GitHubUserConnectionInput) (domain.GitHubUserConnection, error)
	DeleteGitHubUserConnection(context.Context, string) error
	DeleteGitHubUserConnectionByGitHubID(context.Context, int64) error
	CreateGitHubInstallAttempt(context.Context, domain.Principal, string, []byte, time.Time) (domain.GitHubInstallAttempt, error)
	ValidateGitHubInstallState(context.Context, []byte) error
	BeginGitHubOAuth(context.Context, []byte, domain.GitHubInstallation, []byte, []byte, []byte, time.Time) (domain.GitHubInstallAttempt, error)
	GitHubOAuthAttempt(context.Context, []byte) (domain.GitHubInstallAttempt, error)
	CompleteGitHubInstallation(context.Context, []byte, domain.GitHubInstallation) (domain.GitHubInstallation, error)
	ListGitHubInstallations(context.Context, domain.Principal, string) ([]domain.GitHubInstallation, error)
	GitHubInstallationForSync(context.Context, domain.Principal, string, string) (domain.GitHubInstallation, error)
	BeginGitHubRepositorySync(context.Context, domain.GitHubInstallation) (int64, error)
	ReconcileGitHubRepositories(context.Context, string, domain.GitHubInstallation, int64, []domain.GitHubRepository) error
	MarkGitHubSyncFailure(context.Context, string, domain.GitHubInstallation, int64, string) error
	DisconnectGitHubInstallation(context.Context, domain.Principal, string, string) (domain.GitHubInstallation, error)
	BindGitHubInstallation(context.Context, domain.Principal, string, domain.GitHubInstallation) (domain.GitHubInstallation, error)
	ListGitHubRepositories(context.Context, domain.Principal, string, *domain.Cursor, int) ([]domain.GitHubRepository, bool, error)
	InsertGitHubWebhook(context.Context, domain.GitHubWebhookDelivery, []byte) (bool, error)
	ClaimGitHubWebhook(context.Context, string, time.Time) (domain.GitHubWebhookDelivery, error)
	CompleteGitHubWebhook(context.Context, string, string) error
	RetryGitHubWebhook(context.Context, string, string, string, time.Time, bool) error
	GitHubInstallationRoute(context.Context, int64) (string, string, error)
	GitHubInstallationByRoute(context.Context, string, string) (domain.GitHubInstallation, error)
	ApplyGitHubInstallationEvent(context.Context, string, string, string) error
	WorkerGitHubCheckoutContext(context.Context, string, string) (domain.GitHubCheckoutContext, error)
	WorkerRemoteGitHubCheckoutContext(context.Context, string, string) (domain.RemoteGitHubCheckoutContext, error)
	CreatePullRequestRecord(
		ctx context.Context,
		orgID, sessionID string,
		provider, repository, author string,
		number int,
		url, sourceBranch, targetBranch, headSHA, title string,
		additions, deletions, changedFiles int,
	) (domain.PullRequest, error)
	ClaimPullRequestRecord(context.Context, string, string, domain.PullRequest) (domain.PullRequest, error)
	GitHubInstallationForRepository(ctx context.Context, orgID, repository string) (installationID, repositoryID int64, err error)
	UpdatePullRequestObservation(
		ctx context.Context,
		orgID, pullRequestID string,
		observation domain.PullRequestObservation,
	) (domain.PullRequest, error)
	CreateReviewRun(ctx context.Context, orgID, pullRequestID, reviewSessionID, targetSHA string) (domain.ReviewRun, bool, error)
	OpenReviewTerminal(ctx context.Context, orgID, sessionID, reviewRunID, prompt string) error
	CloseReviewTerminal(ctx context.Context, orgID, sessionID, reviewRunID string) error
	ReviewRunPullRequest(ctx context.Context, orgID, reviewRunID string) (domain.ReviewRunPullRequest, error)
	CompleteAndDeliverReviewRun(
		ctx context.Context,
		orgID, reviewRunID, reviewSessionID string,
		result domain.SubmitReviewResult,
		providerReviewID string,
	) (domain.ReviewRun, error)
	FailReviewRun(ctx context.Context, orgID, reviewRunID, reviewSessionID, lastError string) (domain.ReviewRun, error)
	ReserveGitHubRepositoryCapability(context.Context, domain.Principal, string, string, string, []byte, int64) (domain.GitHubRepositoryCapability, bool, error)
	ActivateGitHubRepositoryCapability(context.Context, domain.Principal, string, string, domain.GitHubRepository, []byte, []byte, []byte) (domain.GitHubRepositoryCapability, error)
	GitHubRepositoryCapability(context.Context, []byte, string) (domain.GitHubRepositoryCapability, error)
	RevokeGitHubRepositoryCapability(context.Context, domain.Principal, string, []byte, string) (domain.GitHubRepositoryCapability, error)
	RevokeGitHubRepositoryCapabilitiesForUser(context.Context, string, string) error
}

type CheckoutGrant struct {
	CloneURL  string    `json:"cloneUrl"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Service struct {
	store         Store
	client        *Client
	stateKey      []byte
	webhookSecret string
	installTTL    time.Duration
	logger        *slog.Logger
	workerID      string
	credentialKey []byte
	userTokenMu   sync.Mutex
	checkMu       sync.Mutex
	checkAt       time.Time
	checkErr      error
}

func (s *Service) Check(ctx context.Context) error {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if !s.checkAt.IsZero() && time.Since(s.checkAt) < 30*time.Second {
		return s.checkErr
	}
	s.checkErr = s.client.Check(ctx)
	s.checkAt = time.Now()
	return s.checkErr
}

func NewService(
	store Store,
	client *Client,
	stateKey []byte,
	credentialKey []byte,
	webhookSecret string,
	installTTL time.Duration,
	logger *slog.Logger,
) (*Service, error) {
	if store == nil || client == nil || len(stateKey) != 32 ||
		len(credentialKey) != 32 ||
		webhookSecret == "" || installTTL <= 0 {
		return nil, errors.New("GitHub App service configuration is incomplete")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:         store,
		client:        client,
		stateKey:      append([]byte(nil), stateKey...),
		credentialKey: append([]byte(nil), credentialKey...),
		webhookSecret: webhookSecret,
		installTTL:    installTTL,
		logger:        logger,
		workerID:      uuid.NewString(),
	}, nil
}

func (s *Service) StartInstallation(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
) (string, time.Time, error) {
	state, stateHash, err := NewState()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(s.installTTL)
	if _, err := s.store.CreateGitHubInstallAttempt(
		ctx,
		principal,
		orgID,
		stateHash,
		expiresAt,
	); err != nil {
		return "", time.Time{}, err
	}
	return s.client.InstallationURL(state), expiresAt, nil
}

func (s *Service) BeginOAuth(
	ctx context.Context,
	state string,
	installationID int64,
) (string, error) {
	if state == "" || installationID <= 0 {
		return "", postgres.ErrInvalid
	}
	installStateHash := HashState(state)
	if err := s.store.ValidateGitHubInstallState(ctx, installStateHash); err != nil {
		return "", err
	}
	providerInstallation, err := s.client.GetInstallation(ctx, installationID)
	if err != nil {
		return "", err
	}
	if !InstallationSupportsAuthorityProof(providerInstallation) {
		return "", postgres.ErrForbidden
	}
	oauthState, oauthStateHash, err := NewState()
	if err != nil {
		return "", err
	}
	verifier, challenge, err := NewPKCE()
	if err != nil {
		return "", err
	}
	associatedData := []byte(strconv.FormatInt(installationID, 10))
	ciphertext, nonce, err := Encrypt(
		s.stateKey,
		[]byte(verifier),
		associatedData,
	)
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().UTC().Add(s.installTTL)
	_, err = s.store.BeginGitHubOAuth(
		ctx,
		installStateHash,
		toDomainInstallation(providerInstallation),
		oauthStateHash,
		ciphertext,
		nonce,
		expiresAt,
	)
	if err != nil {
		return "", err
	}
	return s.client.OAuthURL(oauthState, challenge), nil
}

func (s *Service) CompleteOAuth(
	ctx context.Context,
	state, code string,
) (domain.GitHubInstallation, error) {
	if state == "" || code == "" {
		return domain.GitHubInstallation{}, postgres.ErrInvalid
	}
	stateHash := HashState(state)
	attempt, err := s.store.GitHubOAuthAttempt(ctx, stateHash)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	associatedData := []byte(strconv.FormatInt(attempt.PendingGitHubInstallationID, 10))
	verifier, err := Decrypt(
		s.stateKey,
		attempt.OAuthVerifierCiphertext,
		attempt.OAuthVerifierNonce,
		associatedData,
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	accessToken, err := s.client.ExchangeOAuthCode(ctx, code, string(verifier))
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	authorized, err := s.client.UserHasInstallation(
		ctx,
		accessToken,
		attempt.PendingGitHubInstallationID,
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if !authorized {
		return domain.GitHubInstallation{}, postgres.ErrForbidden
	}
	providerInstallation, err := s.client.GetInstallation(
		ctx,
		attempt.PendingGitHubInstallationID,
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if !InstallationSupportsAuthorityProof(providerInstallation) {
		return domain.GitHubInstallation{}, postgres.ErrForbidden
	}
	authorized, err = s.client.UserCanAdministerInstallation(
		ctx,
		accessToken,
		providerInstallation,
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if !authorized {
		return domain.GitHubInstallation{}, postgres.ErrForbidden
	}
	installation, err := s.store.CompleteGitHubInstallation(
		ctx,
		stateHash,
		toDomainInstallation(providerInstallation),
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	return installation, nil
}

func (s *Service) ListInstallations(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
) ([]domain.GitHubInstallation, error) {
	return s.store.ListGitHubInstallations(ctx, principal, orgID)
}

func (s *Service) SyncInstallation(
	ctx context.Context,
	principal domain.Principal,
	orgID, installationID string,
) (domain.GitHubInstallation, error) {
	installation, err := s.store.GitHubInstallationForSync(
		ctx,
		principal,
		orgID,
		installationID,
	)
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	if err := s.sync(ctx, installation); err != nil {
		return domain.GitHubInstallation{}, err
	}
	installation.SyncStatus = "ready"
	now := time.Now().UTC()
	installation.LastSyncedAt = &now
	installation.LastError = ""
	return installation, nil
}

func (s *Service) DisconnectInstallation(
	ctx context.Context,
	principal domain.Principal,
	orgID, installationID string,
) (domain.GitHubInstallation, error) {
	return s.store.DisconnectGitHubInstallation(
		ctx,
		principal,
		orgID,
		installationID,
	)
}

func (s *Service) ListRepositories(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	cursor *domain.Cursor,
	limit int,
) ([]domain.GitHubRepository, bool, error) {
	return s.store.ListGitHubRepositories(ctx, principal, orgID, cursor, limit)
}

// IssueCheckoutGrant first resolves the worker's durable session-to-repository
// authorization, then asks GitHub for a token restricted to that one repository
// with read-only contents permission. Installation-token issuance is kept
// private to this service so callers cannot bypass the PostgreSQL grant check.
func (s *Service) IssueCheckoutGrant(
	ctx context.Context,
	orgID, sessionID string,
) (CheckoutGrant, error) {
	return s.issueGrant(ctx, orgID, sessionID, s.client.repositoryToken)
}

// IssuePushGrant is IssueCheckoutGrant's write-scoped counterpart: the token
// it mints carries contents:write (and pull_requests:write, unused for a
// push but harmless to request once rather than adding a third token
// shape). Only ever handed to a worker immediately before a git push, never
// cached — see repositoryWriteToken.
func (s *Service) IssuePushGrant(
	ctx context.Context,
	orgID, sessionID string,
) (CheckoutGrant, error) {
	return s.issueGrant(ctx, orgID, sessionID, s.client.repositoryWriteToken)
}

// issueGrant is IssueCheckoutGrant and IssuePushGrant's shared authorization
// and token-minting path; they differ only in which of the client's two
// token-scope functions mints the access token.
func (s *Service) issueGrant(
	ctx context.Context,
	orgID, sessionID string,
	mintToken func(ctx context.Context, installationID, repositoryID int64) (installationAccessToken, error),
) (CheckoutGrant, error) {
	authorization, err := s.resolveWorkerCheckoutAuthorization(ctx, orgID, sessionID)
	if err != nil {
		return CheckoutGrant{}, err
	}
	access, err := mintToken(ctx, authorization.GitHubInstallationID, authorization.GitHubRepositoryID)
	if err != nil {
		return CheckoutGrant{}, err
	}
	if access.ExpiresAt.After(time.Now().UTC().Add(2 * time.Hour)) {
		return CheckoutGrant{}, errors.New("GitHub returned an unexpectedly long-lived installation token")
	}
	return CheckoutGrant{
		CloneURL:  authorization.CloneURL,
		Token:     access.Token,
		ExpiresAt: access.ExpiresAt,
	}, nil
}

// resolveWorkerCheckoutAuthorization loads and re-validates a session's
// repository authorization. It is the one place that decides whether a
// worker may touch a repository at all — every grant- or write-issuing path
// goes through it first.
func (s *Service) resolveWorkerCheckoutAuthorization(
	ctx context.Context,
	orgID, sessionID string,
) (domain.GitHubCheckoutContext, error) {
	authorization, err := s.store.WorkerGitHubCheckoutContext(ctx, orgID, sessionID)
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		authorization, err = s.resolveCapabilityCheckoutAuthorization(ctx, orgID, sessionID)
	}
	if err != nil {
		return domain.GitHubCheckoutContext{}, err
	}
	if authorization.OrgID != orgID ||
		authorization.SessionID != sessionID ||
		authorization.ProjectID == "" ||
		authorization.GitHubInstallationID <= 0 ||
		authorization.GitHubRepositoryID <= 0 ||
		!validGitHubCloneIdentity(authorization.CloneURL, authorization.FullName) {
		return domain.GitHubCheckoutContext{}, postgres.ErrForbidden
	}
	return authorization, nil
}

// resolveCapabilityCheckoutAuthorization maps a capability-backed project to
// the same repository authorization used by directly granted projects. This
// is required in production too: projects created through the environment
// control endpoint retain the production authority as an encrypted capability
// instead of a local repository-grant row.
func (s *Service) resolveCapabilityCheckoutAuthorization(
	ctx context.Context,
	orgID, sessionID string,
) (domain.GitHubCheckoutContext, error) {
	remote, err := s.store.WorkerRemoteGitHubCheckoutContext(ctx, orgID, sessionID)
	if err != nil {
		return domain.GitHubCheckoutContext{}, err
	}
	if remote.OrgID != orgID ||
		remote.SessionID != sessionID ||
		remote.ProjectID == "" ||
		remote.GitHubInstallationID <= 0 ||
		remote.GitHubRepositoryID <= 0 ||
		!validCapabilityEnvironment(remote.TargetEnvironment) ||
		strings.TrimSpace(remote.UserExternalID) == "" {
		return domain.GitHubCheckoutContext{}, postgres.ErrForbidden
	}
	plaintext, err := Decrypt(
		s.credentialKey,
		remote.CapabilityCiphertext,
		remote.CapabilityNonce,
		[]byte(RepositoryCapabilityAssociatedData(remote)),
	)
	if err != nil {
		return domain.GitHubCheckoutContext{}, postgres.ErrForbidden
	}
	defer clear(plaintext)
	authority, err := s.ValidateRepositoryCapability(
		ctx,
		string(plaintext),
		remote.TargetEnvironment,
		remote.GitHubInstallationID,
		remote.GitHubRepositoryID,
		remote.UserExternalID,
	)
	if err != nil {
		return domain.GitHubCheckoutContext{}, err
	}
	if !strings.EqualFold(strings.TrimRight(remote.RepositoryURL, "/"), strings.TrimRight(authority.Repository.HTMLURL, "/")) {
		return domain.GitHubCheckoutContext{}, postgres.ErrForbidden
	}
	return domain.GitHubCheckoutContext{
		OrgID:                orgID,
		SessionID:            sessionID,
		ProjectID:            remote.ProjectID,
		GitHubInstallationID: authority.GitHubInstallationID,
		GitHubRepositoryID:   authority.GitHubRepositoryID,
		FullName:             authority.Repository.FullName,
		CloneURL:             authority.Repository.CloneURL,
		DefaultBranch:        authority.Repository.DefaultBranch,
	}, nil
}

// RaisePullRequest opens a pull request for a session's already-pushed branch
// and durably records it. The GitHub API call and the durable record are two
// separate steps by necessity (GitHub doesn't offer an atomic "create and
// confirm" primitive), but they happen back to back in this one call so
// nothing else can observe a pull request that exists on GitHub with no
// corresponding AO record, or vice versa.
func (s *Service) RaisePullRequest(
	ctx context.Context,
	orgID, sessionID string,
	input domain.RaisePullRequest,
) (domain.PullRequest, error) {
	title := strings.TrimSpace(input.Title)
	head := strings.TrimSpace(input.HeadBranch)
	if title == "" || head == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	authorization, err := s.resolveWorkerCheckoutAuthorization(ctx, orgID, sessionID)
	if err != nil {
		return domain.PullRequest{}, err
	}
	base := strings.TrimSpace(input.BaseBranch)
	if base == "" {
		base = strings.TrimSpace(authorization.DefaultBranch)
	}
	if base == "" {
		return domain.PullRequest{}, fmt.Errorf(
			"%w: no base branch given and the repository has none on record",
			postgres.ErrInvalid,
		)
	}
	owner, repo, ok := strings.Cut(authorization.FullName, "/")
	if !ok || owner == "" || repo == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	access, err := s.client.repositoryWriteToken(
		ctx,
		authorization.GitHubInstallationID,
		authorization.GitHubRepositoryID,
	)
	if err != nil {
		return domain.PullRequest{}, err
	}
	pr, err := s.client.CreatePullRequest(ctx, access.Token, owner, repo, CreatePullRequestInput{
		Title: title,
		Body:  input.Body,
		Head:  head,
		Base:  base,
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	record, err := s.store.CreatePullRequestRecord(
		ctx,
		orgID, sessionID,
		"github", authorization.FullName, pr.User.Login,
		pr.Number, pr.HTMLURL, head, base, pr.Head.SHA, title,
		pr.Additions, pr.Deletions, pr.ChangedFiles,
	)
	if err != nil {
		return domain.PullRequest{}, err
	}
	s.triggerReview(ctx, orgID, sessionID, record)
	return record, nil
}

// ClaimPullRequest records a pull request that worker-side tooling already
// opened. Installation tokens deliberately do not represent a human GitHub
// identity, so this claims ownership for the AO worker session instead of
// attempting unsupported user-only GitHub subscription or assignment calls.
func (s *Service) ClaimPullRequest(
	ctx context.Context,
	orgID, sessionID, reference string,
) (domain.PullRequest, error) {
	authorization, err := s.resolveWorkerCheckoutAuthorization(ctx, orgID, sessionID)
	if err != nil {
		return domain.PullRequest{}, err
	}
	owner, repository, ok := strings.Cut(authorization.FullName, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repository) == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	number, err := parsePullRequestReference(reference, authorization.FullName)
	if err != nil {
		return domain.PullRequest{}, err
	}
	access, err := s.client.repositoryWriteToken(
		ctx,
		authorization.GitHubInstallationID,
		authorization.GitHubRepositoryID,
	)
	if err != nil {
		return domain.PullRequest{}, err
	}
	pullRequest, err := s.client.GetPullRequestRecord(ctx, access.Token, owner, repository, number)
	if err != nil {
		return domain.PullRequest{}, err
	}
	state := contract.PRState(strings.ToLower(strings.TrimSpace(pullRequest.State)))
	switch state {
	case contract.PRStateOpen, contract.PRStateClosed:
	default:
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	if pullRequest.Number != number || pullRequest.HTMLURL == "" || pullRequest.Head.SHA == "" ||
		pullRequest.Head.Ref == "" || pullRequest.Base.Ref == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	record, err := s.store.ClaimPullRequestRecord(ctx, orgID, sessionID, domain.PullRequest{
		Provider:     "github",
		Repository:   authorization.FullName,
		Author:       pullRequest.User.Login,
		Number:       pullRequest.Number,
		URL:          pullRequest.HTMLURL,
		Title:        pullRequest.Title,
		State:        state,
		Draft:        pullRequest.Draft,
		HeadSHA:      pullRequest.Head.SHA,
		SourceBranch: pullRequest.Head.Ref,
		TargetBranch: pullRequest.Base.Ref,
		Additions:    pullRequest.Additions,
		Deletions:    pullRequest.Deletions,
		ChangedFiles: pullRequest.ChangedFiles,
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	s.triggerReview(ctx, orgID, sessionID, record)
	return record, nil
}

func parsePullRequestReference(reference, fullName string) (int, error) {
	reference = strings.TrimSpace(reference)
	if number, err := strconv.Atoi(reference); err == nil && number > 0 {
		return number, nil
	}
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Port() != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, postgres.ErrInvalid
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[2], "pull") {
		return 0, postgres.ErrInvalid
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return 0, postgres.ErrInvalid
	}
	repository, err := url.PathUnescape(parts[1])
	if err != nil || !strings.EqualFold(owner+"/"+repository, strings.Trim(fullName, "/")) {
		return 0, postgres.ErrInvalid
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return 0, postgres.ErrInvalid
	}
	return number, nil
}

func validGitHubCloneIdentity(cloneURL, fullName string) bool {
	parsed, err := url.Parse(cloneURL)
	if err != nil ||
		parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), "github.com") ||
		parsed.Port() != "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return false
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return false
	}
	expected := "/" + strings.Trim(fullName, "/") + ".git"
	return strings.EqualFold(path, expected)
}

func (s *Service) EnqueueVerifiedWebhook(
	ctx context.Context,
	delivery domain.GitHubWebhookDelivery,
) (bool, error) {
	hash := HashState(string(delivery.Payload))
	return s.store.InsertGitHubWebhook(ctx, delivery, hash)
}

func (s *Service) VerifyWebhook(payload []byte, signature string) bool {
	return VerifyWebhook(s.webhookSecret, payload, signature)
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.processNext(ctx); err != nil &&
			!errors.Is(err, postgres.ErrNotFound) &&
			!errors.Is(err, context.Canceled) {
			s.logger.Error("process GitHub webhook", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) processNext(ctx context.Context) error {
	delivery, err := s.store.ClaimGitHubWebhook(
		ctx,
		s.workerID,
		time.Now().UTC().Add(30*time.Second),
	)
	if err != nil {
		return err
	}
	processCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = s.processWebhook(processCtx, delivery)
	if err == nil {
		return s.store.CompleteGitHubWebhook(ctx, delivery.DeliveryID, s.workerID)
	}
	terminal := delivery.AttemptCount >= 10 ||
		errors.Is(err, postgres.ErrInvalid)
	backoff := time.Second * time.Duration(1<<min(delivery.AttemptCount, 9))
	return s.store.RetryGitHubWebhook(
		ctx,
		delivery.DeliveryID,
		s.workerID,
		err.Error(),
		time.Now().UTC().Add(backoff),
		terminal,
	)
}

func (s *Service) processWebhook(
	ctx context.Context,
	delivery domain.GitHubWebhookDelivery,
) error {
	if delivery.GitHubInstallationID <= 0 {
		return postgres.ErrInvalid
	}
	orgID, installationID, err := s.store.GitHubInstallationRoute(
		ctx,
		delivery.GitHubInstallationID,
	)
	if err != nil {
		return err
	}
	switch delivery.Event {
	case "installation":
		action := "unsuspend"
		providerInstallation, err := s.client.GetInstallation(
			ctx,
			delivery.GitHubInstallationID,
		)
		if err != nil {
			var httpError *HTTPError
			if errors.As(err, &httpError) && httpError.StatusCode == http.StatusNotFound {
				action = "deleted"
			} else {
				return err
			}
		} else if providerInstallation.SuspendedAt != nil {
			action = "suspend"
		}
		if err := s.store.ApplyGitHubInstallationEvent(
			ctx,
			orgID,
			installationID,
			action,
		); err != nil {
			return err
		}
		if action == "suspend" || action == "deleted" {
			return nil
		}
	case "installation_repositories":
	default:
		return postgres.ErrInvalid
	}
	installation, err := s.store.GitHubInstallationByRoute(
		ctx,
		orgID,
		installationID,
	)
	if err != nil {
		return err
	}
	return s.sync(ctx, installation)
}

func (s *Service) sync(
	ctx context.Context,
	installation domain.GitHubInstallation,
) error {
	generation, err := s.store.BeginGitHubRepositorySync(ctx, installation)
	if err != nil {
		return err
	}
	providerRepositories, err := s.client.ListRepositories(
		ctx,
		installation.GitHubInstallationID,
	)
	if err != nil {
		_ = s.store.MarkGitHubSyncFailure(
			ctx,
			installation.OrgID,
			installation,
			generation,
			err.Error(),
		)
		return err
	}
	repositories := make([]domain.GitHubRepository, 0, len(providerRepositories))
	for _, repository := range providerRepositories {
		updatedAt := repository.UpdatedAt
		repositories = append(repositories, domain.GitHubRepository{
			GitHubRepositoryID: repository.ID,
			GitHubOwnerID:      repository.Owner.ID,
			Name:               repository.Name,
			FullName:           repository.FullName,
			HTMLURL:            repository.HTMLURL,
			CloneURL:           repository.CloneURL,
			SSHURL:             repository.SSHURL,
			DefaultBranch:      repository.DefaultBranch,
			Visibility:         repository.Visibility,
			IsPrivate:          repository.Private,
			IsArchived:         repository.Archived,
			IsDisabled:         repository.Disabled,
			GitHubUpdatedAt:    &updatedAt,
		})
	}
	return s.store.ReconcileGitHubRepositories(
		ctx,
		installation.OrgID,
		installation,
		generation,
		repositories,
	)
}

func toDomainInstallation(value Installation) domain.GitHubInstallation {
	permissions, _ := json.Marshal(value.Permissions)
	status := "active"
	if value.SuspendedAt != nil {
		status = "suspended"
	}
	return domain.GitHubInstallation{
		GitHubInstallationID: value.ID,
		GitHubAccountID:      value.Account.ID,
		AccountLogin:         value.Account.Login,
		AccountType:          value.Account.Type,
		Status:               status,
		RepositorySelection:  value.RepositorySelection,
		Permissions:          permissions,
		Events:               value.Events,
		SyncStatus:           "pending",
	}
}

func (s *Service) CompletionHTML(success bool) []byte {
	return s.completionHTML(success, false)
}

func (s *Service) InstallationCompletionHTML(success bool) []byte {
	return s.completionHTML(success, true)
}

func (s *Service) completionHTML(success, closeImmediately bool) []byte {
	title := "Connection failed"
	message := "Return to AO and try connecting GitHub again."
	statusClass := "error"
	statusIcon := "!"
	buttonLabel := "Close window"
	autoClose := ""
	if success {
		title = "GitHub connected"
		message = "Repository access is ready. Return to AO to continue."
		statusClass = "success"
		statusIcon = "✓"
		// Keep the popup alive long enough for the opener to advance the
		// account-authorization step into GitHub App installation. The final
		// step closes it immediately once the installation is visible.
		if closeImmediately {
			autoClose = "window.close();"
		} else {
			autoClose = "window.setTimeout(function(){window.close()},10000);"
		}
	}
	return []byte(fmt.Sprintf(
		`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<title>%s · AO</title>
<style>
:root{color-scheme:dark;font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#090a0c;color:#f4f5f7}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;background:#090a0c}
main{min-height:100vh;display:grid;place-items:center;padding:32px}
.content{width:min(100%%,440px);border:1px solid #292c32;border-radius:8px;background:#111317;padding:28px;box-shadow:0 20px 60px rgba(0,0,0,.32)}
.brand{display:flex;align-items:center;gap:10px;margin-bottom:36px;color:#a7abb3;font-size:13px;font-weight:500}
.brand-mark{display:grid;place-items:center;width:30px;height:30px;border:1px solid #353941;border-radius:7px;background:#1a1d22;color:#f4f5f7;font-size:12px;font-weight:700}
.status{display:grid;place-items:center;width:44px;height:44px;margin-bottom:20px;border:1px solid;border-radius:50%%;font-size:20px;font-weight:600}
.status.success{border-color:rgba(74,222,128,.38);background:rgba(74,222,128,.08);color:#4ade80}
.status.error{border-color:rgba(212,84,79,.42);background:rgba(212,84,79,.09);color:#e16a65}
h1{margin:0;font-size:24px;line-height:1.25;letter-spacing:0;font-weight:650}
p{margin:10px 0 0;color:#9ba1aa;font-size:14px;line-height:1.6}
.footer{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-top:30px;padding-top:22px;border-top:1px solid #292c32}
.action{display:inline-flex;height:38px;align-items:center;justify-content:center;border:1px solid #d8dbe1;border-radius:6px;background:#e8eaf0;color:#17191d;padding:0 16px;font:inherit;font-size:13px;font-weight:600;cursor:pointer}
.action:hover{background:#fff;border-color:#fff}
.action:focus-visible{outline:2px solid #4d8dff;outline-offset:2px}
.hint{display:flex;align-items:center;gap:7px;color:#777d87;font-size:12px}
.hint::before{content:"";width:6px;height:6px;border-radius:50%%;background:#4ade80;box-shadow:0 0 0 3px rgba(74,222,128,.1)}
.status.error~.footer .hint::before{background:#e16a65;box-shadow:0 0 0 3px rgba(225,106,101,.1)}
@media(max-width:520px){main{place-items:start;padding:20px}.content{padding:24px}.footer{align-items:flex-start;flex-direction:column-reverse}}
</style>
</head>
<body>
<main>
<section class="content" aria-labelledby="title">
<div class="brand"><span class="brand-mark" aria-hidden="true">AO</span><span>Agent Orchestrator</span></div>
<div class="status %s" aria-hidden="true">%s</div>
<h1 id="title">%s</h1>
<p>%s</p>
<div class="footer">
<div class="hint">This window may close automatically.</div>
<button class="action" type="button" onclick="window.close()">%s</button>
</div>
</section>
</main>
<script>%s</script>
</body>
</html>`,
		title,
		statusClass,
		statusIcon,
		title,
		message,
		buttonLabel,
		autoClose,
	))
}
