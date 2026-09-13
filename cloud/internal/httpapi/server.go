package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/auth"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// DefaultMaxSandboxesPerOrg caps how much provider capacity one organization
// can hold at once. Counts only live/idle sandboxes (terminal states like
// terminated/failed/deleting are excluded from the quota), so dead sandboxes
// never block new sessions.
const DefaultMaxSandboxesPerOrg = 1000

type Store interface {
	Ping(context.Context) error
	UpsertWorkOSUser(context.Context, domain.Principal) (domain.Principal, error)
	RegisterLocal(context.Context, domain.LocalRegistration, []byte, time.Time) (domain.Principal, string, error)
	LocalUserByEmail(context.Context, string) (domain.Principal, string, error)
	CreateLocalSession(context.Context, string, []byte, time.Time) error
	PrincipalFromLocalToken(context.Context, []byte) (domain.Principal, error)
	RevokeLocalSession(context.Context, []byte) error
	ListMemberships(context.Context, domain.Principal) ([]domain.Membership, error)
	CreateOrganization(context.Context, domain.Principal, string) (domain.Membership, error)
	ListOrgMembers(context.Context, domain.Principal, string) ([]domain.OrgMember, error)
	UpdateOrgMemberRole(context.Context, domain.Principal, string, string, string) (domain.OrgMember, error)
	ListOrgInvitations(context.Context, domain.Principal, string) ([]domain.Invitation, error)
	ListMyInvitations(context.Context, domain.Principal) ([]domain.Invitation, error)
	CreateOrgInvitation(context.Context, domain.Principal, string, domain.CreateInvitation) (domain.Invitation, error)
	RevokeOrgInvitation(context.Context, domain.Principal, string, string) error
	GetOrgInvitation(context.Context, domain.Principal, string, string) (domain.Invitation, error)
	AcceptOrgInvitation(context.Context, domain.Principal, string, string) (domain.Membership, error)
	DeclineOrgInvitation(context.Context, domain.Principal, string, string) error
	CreateProject(context.Context, domain.Principal, string, string, domain.CreateProject) (domain.Project, error)
	ListProjects(context.Context, domain.Principal, string, *domain.Cursor, int) ([]domain.Project, bool, error)
	UpdateProject(context.Context, domain.Principal, string, string, domain.UpdateProject) (domain.Project, error)
	ArchiveProject(context.Context, domain.Principal, string, string) error
	CreateSession(context.Context, domain.Principal, string, string, int, domain.CreateSession) (domain.Session, error)
	ListSessions(context.Context, domain.Principal, string, string, *domain.Cursor, int) ([]domain.Session, bool, error)
	GetSession(context.Context, domain.Principal, string, string) (domain.Session, error)
	SendMessage(context.Context, domain.Principal, string, string, string, string) (domain.ClientEvent, error)
	ListClientEvents(context.Context, domain.Principal, string, string, int64, int) ([]domain.ClientEvent, bool, error)
	SetSandboxDesiredState(ctx context.Context, principal domain.Principal, orgID, sessionID, desiredState string) error
	WakePausedSessions(context.Context, domain.Principal, string) (int64, error)
	RedeemWorkerBootstrapTicket(context.Context, string) (domain.AccessTicket, error)
	WorkerLaunchSpec(context.Context, string, string) (domain.WorkerLaunch, error)
	RegisterWorkerBootstrap(ctx context.Context, orgID, sessionID, workerID, version string, epoch int64, capabilities []string) error
	WorkerConnectionCurrent(ctx context.Context, orgID, sessionID, workerID string, epoch int64) (bool, error)
	MarkWorkerSeen(ctx context.Context, orgID, sessionID, workerID, version string, epoch int64, capabilities []string) error
	SetWorkerActivity(ctx context.Context, orgID, sessionID, workerID string, epoch int64, activity worker.ActivityEvent) error
	AppendSessionEvent(ctx context.Context, orgID, sessionID, eventType string, payload json.RawMessage) (domain.ClientEvent, error)
	ClaimWorkerTurn(ctx context.Context, orgID, sessionID, workerID string, epoch int64) (domain.WorkerTurn, bool, error)
	RequestTurnCancellation(ctx context.Context, principal domain.Principal, orgID, sessionID, turnID string) error
	WorkerTurnCancellationRequested(ctx context.Context, orgID, sessionID, workerID, turnID string, epoch int64, attempt int) (bool, error)
	AppendWorkerTurnOutput(ctx context.Context, orgID, sessionID, workerID, turnID string, epoch int64, attempt int, stream, text string) error
	FinishWorkerTurn(ctx context.Context, orgID, sessionID, workerID, turnID string, epoch int64, attempt int, outcome, errorMessage string) (bool, error)
	WorkerAgentCredential(ctx context.Context, orgID, sessionID, workerID string, epoch int64) (domain.WorkerCredential, error)
	ListOrchestratorChildren(context.Context, string, string, *domain.Cursor, int) ([]domain.Session, bool, error)
	CreateOrchestratorChild(context.Context, string, string, string, int, domain.CreateSession) (domain.Session, error)
	SendOrchestratorChildMessage(context.Context, string, string, string, string, string) (domain.ClientEvent, error)
	DeleteOrchestratorChild(context.Context, string, string, string) error
	CreateWorkspaceRequest(context.Context, domain.Principal, string, string, string, json.RawMessage, time.Duration) (domain.WorkerRequest, error)
	GetWorkspaceRequest(context.Context, domain.Principal, string, string, string) (domain.WorkerRequest, error)
	CancelWorkspaceRequest(context.Context, domain.Principal, string, string, string) error
	ClaimWorkerRequest(context.Context, string, string, string, int64, time.Duration) (domain.WorkerRequest, bool, error)
	CompleteWorkerRequest(context.Context, string, string, string, string, int64, int, json.RawMessage) error
	FailWorkerRequest(context.Context, string, string, string, string, int64, int, string, string) error
	IssueTerminalTicket(context.Context, domain.Principal, string, string, string, time.Duration) (string, []string, error)
	OpenTerminal(context.Context, string, string, time.Duration) (domain.TerminalSession, error)
	RefreshTerminalInteraction(context.Context, domain.TerminalSession, time.Duration) error
	QueueTerminalInput(context.Context, domain.TerminalSession, string, []byte) error
	QueueTerminalResize(context.Context, domain.TerminalSession, uint16, uint16) error
	CloseTerminal(context.Context, domain.TerminalSession) error
	AppendTerminalOutput(context.Context, string, string, string, string, int64, []byte) (int64, error)
	MarkTerminalExited(context.Context, string, string, string, string, int64, int) error
	EnsureWorkerAgentTerminal(context.Context, string, string, string, int64, time.Duration) (domain.TerminalSession, error)
	ListTerminalOutput(context.Context, domain.TerminalSession, int64, int) ([]domain.TerminalOutput, string, error)
	ListPullRequestsBySession(context.Context, domain.Principal, string, string) ([]domain.PullRequest, error)
	ListReviewRunsBySession(context.Context, domain.Principal, string, string) ([]domain.ReviewRunPullRequest, error)
	PRFactsBySession(ctx context.Context, orgID string, sessionIDs []string) (map[string][]contract.PRFacts, error)
	CreateProjectShareLink(context.Context, domain.Principal, string, string, domain.CreateShareLink) (domain.ShareLink, string, error)
	ListProjectShareLinks(context.Context, domain.Principal, string, string) ([]domain.ShareLink, error)
	ListProjectShareGrants(context.Context, domain.Principal, string, string) ([]domain.SharedProject, error)
	UpdateProjectShareGrant(context.Context, domain.Principal, string, string, string, domain.UpdateShareGrant) (domain.SharedProject, error)
	RevokeProjectShareLink(context.Context, domain.Principal, string, string, string) error
	RevokeProjectShareGrant(context.Context, domain.Principal, string, string, string) error
	RedeemProjectShareLink(context.Context, domain.Principal, string, string) (domain.SharedProject, error)
	ListSharedProjects(context.Context, domain.Principal) ([]domain.SharedProject, error)
	ListSharedProjectSessions(context.Context, domain.Principal, string, string) ([]domain.Session, error)
}

// WorkerTokens issues and verifies the short-lived credentials sandbox workers
// present. It is nil when the deployment runs without sandbox provisioning, in
// which case the worker routes report 404 rather than failing open.
type WorkerTokens interface {
	Issue(worker.Claims, time.Duration) (string, error)
	Verify(string) (worker.Claims, error)
}

type CheckoutBroker interface {
	IssueCheckoutGrant(context.Context, string, string) (githubapp.CheckoutGrant, error)
	IssuePushGrant(context.Context, string, string) (githubapp.CheckoutGrant, error)
	RaisePullRequest(context.Context, string, string, domain.RaisePullRequest) (domain.PullRequest, error)
	ClaimPullRequest(context.Context, string, string, string) (domain.PullRequest, error)
	SubmitReview(context.Context, string, string, string, domain.SubmitReviewResult) (domain.ReviewRun, error)
}

type Server struct {
	store            Store
	workos           auth.WorkOSVerifier
	localAuthEnabled bool
	localSessionTTL  time.Duration
	localAuthLimiter *fixedWindowLimiter
	sandboxProvider  string
	// availableSandboxProviders is every provider a client may select for a
	// session, always including sandboxProvider (the default). It gates the
	// per-session provider override and is reported to clients via /me.
	availableSandboxProviders []string
	provisioning              sandbox.ProvisioningDefaults
	workerTokens              WorkerTokens
	// workerTokenLifetime is zero when the deployment does not override the
	// protocol default; workerTokenTTL() resolves that.
	workerTokenLifetime     time.Duration
	workerRequestTimeout    time.Duration
	maxSandboxes            int
	environment             string
	release                 string
	draining                atomic.Bool
	drainOnce               sync.Once
	drain                   chan struct{}
	logger                  *slog.Logger
	github                  *githubapp.Service
	checkoutBroker          CheckoutBroker
	brokerAuthToken         string
	environmentControlToken string
	secretCipher            *secrets.Cipher
	credentialValidator     credentialValidator
	webhookMaxBody          int64
	handler                 http.Handler
}

type Options struct {
	Store                     Store
	WorkOS                    auth.WorkOSVerifier
	LocalAuthEnabled          bool
	LocalSessionTTL           time.Duration
	SandboxProvider           string
	AvailableSandboxProviders []string
	Provisioning              sandbox.ProvisioningDefaults
	WorkerTokens              WorkerTokens
	WorkerTokenTTL            time.Duration
	WorkerRequestTimeout      time.Duration
	MaxSandboxes              int
	Environment               string
	Release                   string
	Logger                    *slog.Logger
	GitHub                    *githubapp.Service
	CheckoutBroker            CheckoutBroker
	BrokerAuthToken           string
	EnvironmentControlToken   string
	SecretCipher              *secrets.Cipher
	CredentialValidator       credentialValidator
	WebhookMaxBody            int64
}

func New(options Options) *Server {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	sandboxProvider := options.SandboxProvider
	if sandboxProvider == "" {
		sandboxProvider = sandbox.DefaultProvider
	}
	// A client may only select a provider the deployment offers. Default to the
	// single configured provider, and ensure the default is always present so a
	// session with no explicit provider is always valid.
	availableSandboxProviders := append([]string(nil), options.AvailableSandboxProviders...)
	if len(availableSandboxProviders) == 0 {
		availableSandboxProviders = []string{sandboxProvider}
	}
	if !slices.Contains(availableSandboxProviders, sandboxProvider) {
		availableSandboxProviders = append([]string{sandboxProvider}, availableSandboxProviders...)
	}
	environment := options.Environment
	if environment == "" {
		environment = "development"
	}
	release := options.Release
	if release == "" {
		release = "dev"
	}
	webhookMaxBody := options.WebhookMaxBody
	if webhookMaxBody == 0 {
		webhookMaxBody = 2 << 20
	}
	workerRequestTimeout := options.WorkerRequestTimeout
	if workerRequestTimeout <= 0 {
		workerRequestTimeout = 12 * time.Second
	}
	// An unset quota must not read as a quota of zero: that would reject every
	// session with SANDBOX_QUOTA_EXCEEDED rather than allow the default.
	maxSandboxes := options.MaxSandboxes
	if maxSandboxes <= 0 {
		maxSandboxes = DefaultMaxSandboxesPerOrg
	}
	server := &Server{
		store:                     options.Store,
		workos:                    options.WorkOS,
		localAuthEnabled:          options.LocalAuthEnabled,
		localSessionTTL:           options.LocalSessionTTL,
		localAuthLimiter:          newFixedWindowLimiter(10, time.Minute, 4096),
		sandboxProvider:           sandboxProvider,
		availableSandboxProviders: availableSandboxProviders,
		provisioning:              options.Provisioning,
		workerTokens:              options.WorkerTokens,
		workerTokenLifetime:       options.WorkerTokenTTL,
		workerRequestTimeout:      workerRequestTimeout,
		maxSandboxes:              maxSandboxes,
		environment:               environment,
		release:                   release,
		drain:                     make(chan struct{}),
		logger:                    logger,
		github:                    options.GitHub,
		checkoutBroker:            options.CheckoutBroker,
		brokerAuthToken:           options.BrokerAuthToken,
		environmentControlToken:   options.EnvironmentControlToken,
		secretCipher:              options.SecretCipher,
		credentialValidator:       options.CredentialValidator,
		webhookMaxBody:            webhookMaxBody,
	}
	if server.credentialValidator == nil {
		server.credentialValidator = newAgentCredentialValidator(nil)
	}
	if server.checkoutBroker == nil && options.GitHub != nil {
		server.checkoutBroker = options.GitHub
	}
	server.provisioning.Provider = sandboxProvider
	if server.provisioning.Release == "" {
		server.provisioning.Release = release
	}
	router := chi.NewRouter()
	router.Use(server.requestID)
	router.Use(server.requestLog)
	router.Get("/healthz", server.health)
	router.Get("/readyz", server.ready)
	router.Get("/github/healthz", server.githubHealth)
	if server.github != nil {
		router.Get("/api/cloud/v1/github/install/setup", server.githubSetupCallback)
		router.Get("/api/cloud/v1/github/oauth/callback", server.githubOAuthCallback)
		router.Get("/api/cloud/v1/github/user/callback", server.githubUserCallback)
		router.Post("/api/cloud/v1/github/webhooks", server.githubWebhook)
		if server.brokerAuthToken != "" {
			router.Post("/api/cloud/v1/control/github/capabilities/validate", server.validateRepositoryCapability)
			router.Post("/api/cloud/v1/control/github/capabilities/redeem", server.redeemRepositoryCapability)
			router.With(server.authenticateBrokerUser).Post(
				"/api/cloud/v1/orgs/{orgId}/github/scratch-capabilities",
				server.createGitHubScratchCapability,
			)
			router.With(server.authenticateBrokerUser).Post(
				"/api/cloud/v1/orgs/{orgId}/github/scratch-capabilities/revoke",
				server.revokeGitHubScratchCapability,
			)
		}
	}
	if server.environmentControlToken != "" {
		router.Post("/api/cloud/v1/control/github/scratch-projects", server.createEnvironmentScratchProject)
	}
	router.Route("/api/cloud/v1", func(router chi.Router) {
		router.Post("/auth/local/register", server.registerLocal)
		router.Post("/auth/local/login", server.loginLocal)
		router.With(server.authenticate).Post("/auth/local/logout", server.logoutLocal)
		router.With(server.authenticate).Get("/me", server.me)
		router.With(server.authenticate).Post("/orgs", server.createOrganization)
		router.With(server.authenticate).Get("/invitations", server.listMyInvitations)
		router.With(server.authenticate).Get("/me/providers", server.listUserProviderConnections)
		router.With(server.authenticate).Put("/me/providers/{agent}", server.putUserAgentConnection)
		router.With(server.authenticate).Delete("/me/providers/{agent}", server.deleteUserAgentConnection)
		router.With(server.authenticate).Post("/share-links/redeem", server.redeemProjectShareLink)
		router.With(server.authenticate).Get("/shared/projects", server.listSharedProjects)
		if server.github != nil {
			router.With(server.authenticate).Get("/github/user", server.getGitHubUser)
			router.With(server.authenticate).Post("/github/user/authorize", server.startGitHubUserAuthorization)
			router.With(server.authenticate).Delete("/github/user", server.disconnectGitHubUser)
		}
		// Workers hold no user identity, so they never pass through
		// server.authenticate. Bootstrap is gated by a one-time ticket;
		// everything after it by a short-lived worker token.
		router.Post("/worker/bootstrap", server.workerBootstrap)
		router.Group(func(router chi.Router) {
			router.Use(server.workerAuth)
			router.Post("/worker/heartbeat", server.workerHeartbeat)
			router.Post("/worker/events", server.workerEvent)
			router.Post("/worker/turns/claim", server.workerClaimTurn)
			router.Get("/worker/turns/{turnId}/cancellation", server.workerTurnCancellation)
			router.Post("/worker/turns/{turnId}/complete", server.workerCompleteTurn)
			router.Post("/worker/turns/{turnId}/fail", server.workerFailTurn)
			router.Get("/worker/credential", server.workerCredential)
			router.Post("/worker/checkout-grant", server.workerCheckoutGrant)
			router.Post("/worker/push-grant", server.workerPushGrant)
			router.Post("/worker/github-token", server.workerGitHubToken)
			router.Post("/worker/pull-requests", server.workerRaisePullRequest)
			router.Post("/worker/pull-requests/claim", server.workerClaimPullRequest)
			router.Post("/worker/reviews/{reviewRunId}/submit", server.workerSubmitReview)
			router.Get("/worker/children", server.listWorkerChildren)
			router.Post("/worker/children", server.createWorkerChild)
			router.Post("/worker/children/{sessionId}/messages", server.sendWorkerChildMessage)
			router.Delete("/worker/children/{sessionId}", server.deleteWorkerChild)
			router.Post("/worker/transport/claim", server.workerClaimTransport)
			router.Post("/worker/transport/{requestId}/complete", server.workerCompleteTransport)
			router.Post("/worker/transport/{requestId}/fail", server.workerFailTransport)
			router.Post("/worker/terminals/{terminalId}/output", server.workerTerminalOutput)
			router.Post("/worker/terminals/{terminalId}/exit", server.workerTerminalExit)
			router.Post("/worker/terminals/agent", server.workerEnsureAgentTerminal)
		})
		router.Get("/terminal", server.connectTerminal)
		router.Route("/orgs/{orgId}", func(router chi.Router) {
			router.Use(server.authenticate)
			if server.github != nil {
				router.Get("/github/installations", server.listGitHubInstallations)
				router.Post("/github/installations/start", server.startGitHubInstallation)
				router.Post("/github/installations/claim", server.claimGitHubInstallation)
				router.Post("/github/installations/{installationId}/sync", server.syncGitHubInstallation)
				router.Post("/github/installations/{installationId}/disconnect", server.disconnectGitHubInstallation)
				router.Get("/github/repositories", server.listGitHubRepositories)
				router.Post("/github/projects", server.createGitHubProject)
				router.Post("/projects/scratch", server.createGitHubScratchProject)
			}
			router.Get("/projects", server.listProjects)
			router.Post("/projects", server.createProject)
			router.Patch("/projects/{projectId}", server.updateProject)
			router.Delete("/projects/{projectId}", server.deleteProject)
			router.Get("/projects/{projectId}/shares", server.listProjectShareLinks)
			router.Post("/projects/{projectId}/shares", server.createProjectShareLink)
			router.Get("/projects/{projectId}/shares/grants", server.listProjectShareGrants)
			router.Patch("/projects/{projectId}/shares/grants/{grantId}", server.updateProjectShareGrant)
			router.Post("/projects/{projectId}/shares/{linkId}/revoke", server.revokeProjectShareLink)
			router.Post("/projects/{projectId}/shares/grants/{grantId}/revoke", server.revokeProjectShareGrant)
			router.Get("/shared/projects/{projectId}/sessions", server.listSharedProjectSessions)
			router.Get("/provider-connections", server.listProviderConnections)
			router.Put("/provider-connections/agents/{agent}", server.putAgentConnection)
			router.Delete("/provider-connections/agents/{agent}", server.deleteAgentConnection)
			router.Post("/provider-connections/agents/{agent}/promote", server.promoteAgentConnection)
			router.Get("/sessions", server.listSessions)
			router.Post("/sessions", server.createSession)
			router.Post("/sessions/wake", server.wakePausedSessions)
			router.Get("/sessions/{sessionId}", server.getSession)
			router.Delete("/sessions/{sessionId}", server.deleteSession)
			router.Post("/sessions/{sessionId}/messages", server.sendMessage)
			router.Post("/sessions/{sessionId}/turns/{turnId}/cancel", server.cancelTurn)
			router.Get("/sessions/{sessionId}/chat-events", server.replayClientEvents)
			router.Get("/sessions/{sessionId}/events", server.streamClientEvents)
			router.Post("/sessions/{sessionId}/terminal-ticket", server.createTerminalTicket)
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				router.MethodFunc(method, "/sessions/{sessionId}/browser/{origin}", server.proxyBrowser)
				router.MethodFunc(method, "/sessions/{sessionId}/browser/{origin}/*", server.proxyBrowser)
			}
			router.Get("/sessions/{sessionId}/workspace/files", server.listWorkspaceFiles)
			router.Get("/sessions/{sessionId}/workspace/file", server.readWorkspaceFile)
			router.Put("/sessions/{sessionId}/workspace/file", server.writeWorkspaceFile)
			router.Get("/sessions/{sessionId}/workspace/diff", server.getWorkspaceDiff)
			router.Get("/sessions/{sessionId}/pull-requests", server.listSessionPullRequests)
			router.Get("/sessions/{sessionId}/reviews", server.getSessionReviewState)
			router.Get("/members", server.listOrgMembers)
			router.Patch("/members/{userId}", server.updateOrgMemberRole)
			router.Get("/invitations", server.listOrgInvitations)
			router.Post("/invitations", server.createOrgInvitation)
			router.Get("/invitations/{invitationId}", server.getOrgInvitation)
			router.Post("/invitations/{invitationId}/accept", server.acceptOrgInvitation)
			router.Post("/invitations/{invitationId}/decline", server.declineOrgInvitation)
			router.Post("/invitations/{invitationId}/revoke", server.revokeOrgInvitation)
		})
	})
	server.handler = router
	return server
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) SetDraining(draining bool) {
	s.draining.Store(draining)
	if draining {
		s.drainOnce.Do(func() {
			close(s.drain)
		})
	}
}

type contextKey string

const (
	principalKey contextKey = "principal"
	bearerKey    contextKey = "bearer"
	requestIDKey contextKey = "request-id"
)

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 200 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-AO-Release", s.release)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID)))
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		response := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(response, r)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		route := chi.RouteContext(r.Context()).RoutePattern()
		// Worker transport/turn polling is high frequency (every ~80ms per live
		// worker), so logging each at Info floods the access log and dominates
		// log ingestion. Record those at Debug -- available when needed but
		// suppressed by default -- and keep real API calls at Info.
		level := slog.LevelInfo
		switch route {
		case "/api/cloud/v1/worker/transport/claim", "/api/cloud/v1/worker/turns/claim":
			level = slog.LevelDebug
		}
		s.logger.Log(
			r.Context(),
			level,
			"HTTP request complete",
			"method",
			r.Method,
			"route",
			route,
			"status",
			status,
			"duration_ms",
			time.Since(started).Milliseconds(),
			"request_id",
			requestID(r),
			"release",
			s.release,
		)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		scheme, token, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "A bearer token is required.")
			return
		}
		token = strings.TrimSpace(token)
		principal, err := s.principalForBearer(r.Context(), token)
		if err != nil {
			if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, auth.ErrInvalidToken) {
				writeError(w, r, http.StatusUnauthorized, "unauthorized", "The access token is invalid or expired.")
				return
			}
			s.logger.Error("authenticate request", "error", err, "request_id", requestID(r))
			writeError(w, r, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is temporarily unavailable.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey, principal)
		ctx = context.WithValue(ctx, bearerKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) principalForBearer(
	ctx context.Context,
	token string,
) (domain.Principal, error) {
	if strings.HasPrefix(token, "ao_local_") {
		if !s.localAuthEnabled {
			return domain.Principal{}, auth.ErrInvalidToken
		}
		return s.store.PrincipalFromLocalToken(ctx, auth.HashToken(token))
	}
	if s.workos == nil {
		return domain.Principal{}, auth.ErrInvalidToken
	}
	principal, err := s.workos.Verify(ctx, token)
	if err != nil {
		return domain.Principal{}, err
	}
	return s.store.UpsertWorkOSUser(ctx, principal)
}

func principalFrom(r *http.Request) domain.Principal {
	principal, _ := r.Context().Value(principalKey).(domain.Principal)
	return principal
}

func bearerFrom(r *http.Request) string {
	token, _ := r.Context().Value(bearerKey).(string)
	return token
}

func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDKey).(string)
	return value
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.statusResponse("ok"))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		writeError(w, r, http.StatusServiceUnavailable, "draining", "The control plane is draining.")
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database_unavailable", "The database is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, s.statusResponse("ready"))
}

func (s *Server) statusResponse(status string) map[string]string {
	return map[string]string{
		"status":      status,
		"environment": s.environment,
		"release":     s.release,
	}
}
