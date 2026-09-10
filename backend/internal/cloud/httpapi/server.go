// Package httpapi serves the authenticated AO Cloud control-plane API.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	cloudauth "github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	clouddomain "github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	cloudevents "github.com/aoagents/agent-orchestrator/backend/internal/cloud/events"
	cloudpostgres "github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
	cloudsandbox "github.com/aoagents/agent-orchestrator/backend/internal/cloud/sandbox"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/sandbox/daytona"
	cloudlocalgh "github.com/aoagents/agent-orchestrator/backend/internal/cloud/scm/localgh"
	cloudsecrets "github.com/aoagents/agent-orchestrator/backend/internal/cloud/secrets"
	cloudworker "github.com/aoagents/agent-orchestrator/backend/internal/cloud/worker"
	cloudworkerhub "github.com/aoagents/agent-orchestrator/backend/internal/cloud/workerhub"
	shareddomain "github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type store interface {
	Ping(context.Context) error
	EnsureAccount(context.Context, string, string) (clouddomain.Account, error)
	ExternalAccountCanSignIn(context.Context, string, string, string) (bool, error)
	EnsureExternalAccount(context.Context, string, string, string, string, bool) (clouddomain.Account, error)
	UpdateUserProfile(context.Context, string, cloudpostgres.UpdateUserProfileInput) (clouddomain.User, error)
	CreateOrganization(context.Context, cloudpostgres.CreateOrganizationInput) (clouddomain.UserOrganization, error)
	UpdateOrganization(context.Context, clouddomain.OrgID, cloudpostgres.UpdateOrganizationInput) (clouddomain.Organization, error)
	ListUserOrganizations(context.Context, string) ([]clouddomain.UserOrganization, error)
	GetOrgMembership(context.Context, string, clouddomain.OrgID) (clouddomain.UserOrganization, error)
	ListOrgMembers(context.Context, clouddomain.OrgID) ([]clouddomain.OrgMember, error)
	UpdateOrgMemberRole(context.Context, clouddomain.OrgID, string, string) (clouddomain.OrgMember, error)
	CreateOrgInvitation(context.Context, clouddomain.OrgID, cloudpostgres.CreateOrgInvitationInput) (clouddomain.OrgInvitation, error)
	ListOrgInvitations(context.Context, clouddomain.OrgID) ([]clouddomain.OrgInvitation, error)
	ListUserInvitations(context.Context, string, string) ([]clouddomain.OrgInvitation, error)
	AcceptOrgInvitation(context.Context, string, string, string) (clouddomain.OrgMembership, error)
	DeclineOrgInvitation(context.Context, string, string, string) error
	RevokeOrgInvitation(context.Context, clouddomain.OrgID, string) error
	CreateProject(context.Context, clouddomain.AccountID, cloudpostgres.CreateProjectInput) (clouddomain.Project, error)
	ListProjects(context.Context, clouddomain.AccountID) ([]clouddomain.Project, error)
	GetProject(context.Context, clouddomain.AccountID, clouddomain.ProjectID) (clouddomain.Project, error)
	DeleteProject(context.Context, clouddomain.AccountID, clouddomain.ProjectID) error
	CreateProjectShareLink(context.Context, cloudpostgres.CreateProjectShareLinkInput) (cloudpostgres.ProjectShareLink, error)
	RedeemProjectShareLink(context.Context, string, string) (cloudpostgres.SharedProjectGrant, error)
	ListSharedProjectGrants(context.Context, string) ([]cloudpostgres.SharedProjectGrant, error)
	ListProjectShareAccess(context.Context, clouddomain.OrgID, clouddomain.ProjectID) (cloudpostgres.ProjectShareAccess, error)
	UpdateProjectShareGrantRole(context.Context, clouddomain.OrgID, clouddomain.ProjectID, string, string) (cloudpostgres.ProjectShareGrant, error)
	RevokeProjectShareGrant(context.Context, clouddomain.OrgID, clouddomain.ProjectID, string) error
	RevokeProjectShareLink(context.Context, clouddomain.OrgID, clouddomain.ProjectID, string) error
	CreateSession(context.Context, clouddomain.AccountID, cloudpostgres.CreateSessionInput) (cloudpostgres.CreateSessionResult, error)
	ListSessions(context.Context, clouddomain.AccountID) ([]clouddomain.Session, error)
	GetSession(context.Context, clouddomain.AccountID, clouddomain.SessionID) (clouddomain.Session, error)
	DeleteSession(context.Context, clouddomain.AccountID, clouddomain.SessionID) error
	GetActiveTurn(context.Context, clouddomain.AccountID, clouddomain.SessionID) (*clouddomain.Turn, error)
	GetLatestTurn(context.Context, clouddomain.AccountID, clouddomain.SessionID) (*clouddomain.Turn, error)
	TransitionActiveTurn(context.Context, clouddomain.AccountID, clouddomain.SessionID, string, string) (*clouddomain.Turn, error)
	ClaimActiveTurn(context.Context, clouddomain.AccountID, clouddomain.SessionID, int64, int64) error
	PrepareActiveTurnForWorker(context.Context, clouddomain.AccountID, clouddomain.SessionID, int64) (int64, error)
	GetSandbox(context.Context, clouddomain.AccountID, clouddomain.SessionID) (clouddomain.Sandbox, error)
	SetSandboxDesiredState(context.Context, clouddomain.AccountID, clouddomain.SessionID, string) error
	ConsumeAccessTicket(context.Context, string, string) (cloudpostgres.ConsumedTicket, error)
	RedeemWorkerBootstrapTicket(context.Context, string) (cloudpostgres.ConsumedTicket, error)
	RegisterWorkerBootstrap(context.Context, clouddomain.AccountID, clouddomain.SessionID, string, string, int64, []string) error
	MarkWorkerSeen(context.Context, clouddomain.AccountID, clouddomain.SessionID, string, string, int64, []string) error
	WorkerLaunchSpec(context.Context, clouddomain.AccountID, clouddomain.SessionID) (cloudpostgres.WorkerLaunchSpec, error)
	UpdateSessionActivity(context.Context, clouddomain.AccountID, clouddomain.SessionID, string) error
	WorkerConnectionCurrent(context.Context, clouddomain.AccountID, clouddomain.SessionID, string, int64) (bool, error)
	LatestEventSequenceByType(context.Context, clouddomain.AccountID, clouddomain.SessionID, string) (int64, error)
	LatestPromptAcceptedSequence(context.Context, clouddomain.AccountID, clouddomain.SessionID) (int64, error)
	SetAgentSessionID(context.Context, clouddomain.AccountID, clouddomain.SessionID, string) error
	UpsertProviderConnection(context.Context, clouddomain.AccountID, string, string, []byte, []byte, json.RawMessage) (cloudpostgres.ProviderConnection, error)
	SetOrgProviderSettings(context.Context, clouddomain.OrgID, string) (cloudpostgres.OrgProviderSettings, error)
	OrgProviderSettings(context.Context, clouddomain.OrgID) (cloudpostgres.OrgProviderSettings, error)
	PersonalOrgIDForUser(context.Context, string) (clouddomain.OrgID, error)
	ListDefaultProviderOrgsForUser(context.Context, string) ([]clouddomain.OrgID, error)
	ListProviderConnections(context.Context, clouddomain.AccountID) ([]cloudpostgres.ProviderConnection, error)
	ProviderConnectionSecretByProvider(context.Context, clouddomain.AccountID, string, string) ([]byte, []byte, json.RawMessage, error)
	DeleteProviderConnection(context.Context, clouddomain.AccountID, string, string) error
	IssueAccessTicket(context.Context, clouddomain.AccountID, clouddomain.SessionID, string, []string, time.Duration) (string, error)
	SessionSCM(context.Context, clouddomain.AccountID, clouddomain.SessionID) (*cloudpostgres.SessionSCM, error)
	UpsertIssueSnapshot(context.Context, clouddomain.AccountID, clouddomain.Issue) (clouddomain.Issue, error)
	LinkSessionIssue(context.Context, clouddomain.AccountID, clouddomain.SessionID, string) error
	ClaimPullRequest(context.Context, clouddomain.AccountID, clouddomain.PRClaim) (clouddomain.PRClaim, error)
	MarkReviewThreadResolved(context.Context, clouddomain.AccountID, clouddomain.SessionID, string) error
}

type sandboxProviderResolver interface {
	Resolve(context.Context, clouddomain.Sandbox) (cloudsandbox.Provider, error)
}

// Server serves the authenticated AO Cloud HTTP and WebSocket APIs.
type Server struct {
	store                    store
	events                   *cloudevents.Service
	auth                     cloudauth.Authenticator
	localAuth                *cloudauth.LocalAuthenticator
	workerTokens             *cloudworker.TokenManager
	secretCipher             *cloudsecrets.Cipher
	agentCredentials         *agentCredentialValidator
	sandboxProvider          string
	maxActiveSandboxesPerOrg int
	daytonaAPIURL            string
	daytonaTarget            string
	workerHub                *cloudworkerhub.Hub
	sandboxProviders         sandboxProviderResolver
	workerRPC                *workerRPCBroker
	previewTokens            *previewTokenStore
	workerReplayWait         time.Duration
	workerWriteWait          time.Duration
	localGitHub              *cloudlocalgh.Client
	githubStore              githubStore
	githubApp                *githubAppRuntime
	webOrigin                string
	webOriginHost            string
	allowExternalSignup      bool
	log                      *slog.Logger
	handler                  http.Handler
}

// WithMaxActiveSandboxesPerOrg rejects new session creation once an org has
// too many non-deleted sandboxes. A value of 0 disables the guard.
func WithMaxActiveSandboxesPerOrg(limit int) Option {
	return func(server *Server) {
		if limit >= 0 {
			server.maxActiveSandboxesPerOrg = limit
		}
	}
}

// WithSandboxProviderResolver allows destructive API paths to tear down provider
// resources before deleting durable rows.
func WithSandboxProviderResolver(resolver sandboxProviderResolver) Option {
	return func(server *Server) {
		server.sandboxProviders = resolver
	}
}

// New creates an AO Cloud API server.
func New(
	store store,
	events *cloudevents.Service,
	auth cloudauth.Authenticator,
	workerTokens *cloudworker.TokenManager,
	secretCipher *cloudsecrets.Cipher,
	sandboxProvider string,
	daytonaAPIURL, daytonaTarget string,
	workerHub *cloudworkerhub.Hub,
	localGitHub *cloudlocalgh.Client,
	webOrigin string,
	allowExternalSignup bool,
	log *slog.Logger,
	options ...Option,
) *Server {
	if log == nil {
		log = slog.Default()
	}
	localAuth, _ := auth.(*cloudauth.LocalAuthenticator)
	server := &Server{
		store:                    store,
		events:                   events,
		auth:                     auth,
		localAuth:                localAuth,
		workerTokens:             workerTokens,
		secretCipher:             secretCipher,
		agentCredentials:         newAgentCredentialValidator(nil),
		sandboxProvider:          sandboxProvider,
		maxActiveSandboxesPerOrg: 10,
		daytonaAPIURL:            strings.TrimRight(daytonaAPIURL, "/"),
		daytonaTarget:            daytonaTarget,
		workerHub:                workerHub,
		workerRPC:                newWorkerRPCBroker(),
		previewTokens:            newPreviewTokenStore(),
		workerReplayWait:         20 * time.Second,
		workerWriteWait:          10 * time.Second,
		localGitHub:              localGitHub,
		webOrigin:                strings.TrimRight(webOrigin, "/"),
		allowExternalSignup:      allowExternalSignup,
		log:                      log,
	}
	server.githubStore, _ = store.(githubStore)
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	if parsed, err := url.Parse(server.webOrigin); err == nil {
		server.webOriginHost = parsed.Host
	}
	server.handler = server.routes()
	return server
}

// Handler returns the configured AO Cloud HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}

type accountContextKey struct{}
type orgContextKey struct{}
type sharedProjectAccessContextKey struct{}

type sharedProjectAccess struct {
	Projects map[clouddomain.ProjectID]sharedProjectScope
}

type sharedProjectScope struct {
	ProjectRoles []string
	SessionRoles map[clouddomain.SessionID][]string
}

func (a sharedProjectAccess) allowsProject(projectID clouddomain.ProjectID) bool {
	_, ok := a.Projects[projectID]
	return ok
}

func (a sharedProjectAccess) projectRole(projectID clouddomain.ProjectID) (string, bool) {
	scope, ok := a.Projects[projectID]
	if !ok {
		return "", false
	}
	return leastPrivilegedShareRole(scope.ProjectRoles)
}

func (a sharedProjectAccess) sessionRole(
	projectID clouddomain.ProjectID,
	sessionID clouddomain.SessionID,
) (string, bool) {
	scope, ok := a.Projects[projectID]
	if !ok {
		return "", false
	}
	// A session-specific grant is more precise than a project-wide grant. If
	// several grants target the same session, choose the least privileged role
	// deterministically rather than letting grant ordering change access.
	if roles, scoped := scope.SessionRoles[sessionID]; scoped {
		return leastPrivilegedShareRole(roles)
	}
	return leastPrivilegedShareRole(scope.ProjectRoles)
}

func (a sharedProjectAccess) canOperateSession(
	projectID clouddomain.ProjectID,
	sessionID clouddomain.SessionID,
) bool {
	role, ok := a.sessionRole(projectID, sessionID)
	return ok && role == "editor"
}

func leastPrivilegedShareRole(roles []string) (string, bool) {
	best := ""
	for _, role := range roles {
		if !validProjectShareRole(role) {
			continue
		}
		if best == "" || orgRoleRank(role) < orgRoleRank(best) {
			best = role
		}
	}
	return best, best != ""
}

func accountFromContext(ctx context.Context) (clouddomain.Account, bool) {
	account, ok := ctx.Value(accountContextKey{}).(clouddomain.Account)
	return account, ok
}

func orgFromContext(ctx context.Context) (clouddomain.UserOrganization, bool) {
	org, ok := ctx.Value(orgContextKey{}).(clouddomain.UserOrganization)
	return org, ok
}

func sharedProjectAccessFromContext(ctx context.Context) (sharedProjectAccess, bool) {
	access, ok := ctx.Value(sharedProjectAccessContextKey{}).(sharedProjectAccess)
	return access, ok
}

func tenantAccountIDFromContext(ctx context.Context) clouddomain.AccountID {
	if org, ok := orgFromContext(ctx); ok {
		return clouddomain.AccountID(org.Organization.ID)
	}
	account, _ := accountFromContext(ctx)
	return account.ID
}

func (s *Server) routes() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP) //nolint:staticcheck // Cloud is deployed behind the trusted control-plane proxy.
	router.Use(middleware.Recoverer)
	router.Use(s.requestLogger)
	router.Use(s.cors)
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "ao-cloud"})
	})
	router.Get("/readyz", s.ready)

	router.Route("/api/cloud/v1", func(api chi.Router) {
		if s.localAuth != nil {
			api.Post("/auth/signup", s.localSignUp)
			api.Post("/auth/login", s.localLogin)
			api.With(s.localAuth.Middleware).Post("/auth/logout", s.localLogout)
		}
		api.Post("/worker/bootstrap", s.workerBootstrap)
		api.Get("/terminal", s.terminalSocket)
		api.HandleFunc("/preview/{token}/*", s.workspacePreviewProxy)
		api.Get("/github/install/callback", s.githubInstallCallback)
		api.Post("/github/webhooks", s.githubWebhook)
		api.Group(func(worker chi.Router) {
			worker.Use(s.workerAuth)
			worker.Post("/worker/heartbeat", s.workerHeartbeat)
			worker.Post("/worker/events", s.workerEvent)
			worker.Post("/worker/workspace-response", s.workerWorkspaceResponse)
			worker.Get("/worker/connect", s.workerConnect)
			worker.Get("/worker/orchestrate/sessions", s.workerListSessions)
			worker.Post("/worker/orchestrate/sessions", s.workerCreateSession)
			worker.Delete("/worker/orchestrate/sessions/{sessionId}", s.workerKillSession)
			worker.Post("/worker/orchestrate/sessions/{sessionId}/messages", s.workerSendMessage)
			worker.Post("/worker/orchestrate/sessions/{sessionId}/claim-pr", s.workerClaimPullRequest)
			worker.Post("/worker/orchestrate/sessions/{sessionId}/merge-pr", s.workerMergePullRequest)
			worker.Post("/worker/orchestrate/sessions/{sessionId}/review-threads/{threadId}/resolve", s.workerResolveReviewThread)
			worker.Get("/worker/orchestrate/sessions/{sessionId}/inspection", s.workerInspectSession)
			worker.Post("/worker/blocker", s.workerReportBlocker)
			worker.Post("/worker/scm/claim-pr", s.workerClaimOwnPullRequest)
			worker.Post("/worker/github-token", s.workerGitHubToken)
			worker.Handle("/git/{owner}/{repository}.git/*", http.HandlerFunc(s.gitProxy))
		})

		api.Group(func(protected chi.Router) {
			protected.Use(s.auth.Middleware)
			protected.Use(s.ensureAccount)
			protected.Get("/me", s.me)
			protected.Patch("/me", s.updateMe)
			protected.Get("/orgs", s.listOrgs)
			protected.Post("/orgs", s.createOrg)
			protected.Get("/invitations", s.listMyInvitations)
			protected.Post("/invitations/{invitationId}/accept", s.acceptInvitation)
			protected.Post("/invitations/{invitationId}/decline", s.declineInvitation)
			protected.Get("/shares", s.listSharedProjects)
			protected.Post("/share-links/{token}/redeem", s.redeemProjectShareLink)
			// Compatibility aliases for existing local tests/tools. The browser UI
			// uses the explicit /orgs/{orgId}/... routes below.
			protected.Get("/projects", s.listProjects)
			protected.Post("/projects", s.createProject)
			protected.Delete("/projects/{projectId}", s.deleteProject)
			protected.Get("/sessions", s.listSessions)
			protected.Post("/sessions", s.createSession)
			protected.Get("/sessions/{sessionId}", s.getSession)
			protected.Delete("/sessions/{sessionId}", s.deleteSession)
			protected.Get("/sessions/{sessionId}/active-turn", s.activeTurn)
			protected.Post("/sessions/{sessionId}/desired-state", s.setDesiredState)
			protected.Get("/sessions/{sessionId}/chat-events", s.chatEvents)
			protected.Post("/sessions/{sessionId}/messages", s.sendMessage)
			protected.Post("/sessions/{sessionId}/interrupt", s.interruptSession)
			protected.Get("/sessions/{sessionId}/events", s.streamEvents)
			protected.Get("/sessions/{sessionId}/scm", s.sessionSCM)
			protected.Get("/sessions/{sessionId}/workspace/files", s.workspaceFiles)
			protected.Get("/sessions/{sessionId}/workspace/file", s.workspaceFile)
			protected.Get("/sessions/{sessionId}/workspace/diff", s.workspaceDiff)
			protected.Post("/sessions/{sessionId}/workspace/preview", s.workspacePreview)
			protected.Post("/sessions/{sessionId}/workspace/preview-ticket", s.issueWorkspacePreview)
			protected.Post("/sessions/{sessionId}/workspace/file-preview-ticket", s.issueWorkspaceFilePreview)
			protected.Post("/sessions/{sessionId}/terminal-ticket", s.issueTerminalTicket)
			protected.Get("/provider-connections", s.listProviderConnections)
			protected.Put("/provider-connections/daytona", s.putDaytonaConnection)
			protected.Put("/provider-connections/agents/{agent}", s.putAgentConnection)
			protected.Delete("/provider-connections/agents/{agent}", s.deleteAgentConnection)
			protected.Get("/repositories", s.listRepositories)
			protected.Route("/orgs/{orgId}", func(org chi.Router) {
				org.Use(s.requireOrg)
				org.Patch("/", s.updateOrg)
				org.Get("/members", s.listOrgMembers)
				org.With(s.requireOrgRole("admin")).Patch("/members/{userId}", s.updateOrgMemberRole)
				org.Get("/invitations", s.listOrgInvitations)
				org.Post("/invitations", s.createOrgInvitation)
				org.Post("/invitations/{invitationId}/revoke", s.revokeInvitation)
				org.Get("/projects", s.listProjects)
				org.With(s.requireOrgRole("member")).Post("/projects", s.createProject)
				org.With(s.requireOrgRole("admin")).Delete("/projects/{projectId}", s.deleteProject)
				org.With(s.requireOrgRole("member")).Get("/projects/{projectId}/shares", s.listProjectShareAccess)
				org.With(s.requireOrgRole("member")).Post("/projects/{projectId}/shares", s.createProjectShareLink)
				org.With(s.requireOrgRole("member")).Patch("/projects/{projectId}/shares/grants/{grantId}", s.updateProjectShareGrant)
				org.With(s.requireOrgRole("member")).Delete("/projects/{projectId}/shares/grants/{grantId}", s.revokeProjectShareGrant)
				org.With(s.requireOrgRole("member")).Delete("/projects/{projectId}/shares/links/{linkId}", s.revokeProjectShareLink)
				org.Get("/sessions", s.listSessions)
				org.With(s.requireOrgRole("member")).Post("/sessions", s.createSession)
				org.Get("/sessions/{sessionId}", s.getSession)
				org.With(s.requireOrgRole("member")).Delete("/sessions/{sessionId}", s.deleteSession)
				org.Get("/sessions/{sessionId}/active-turn", s.activeTurn)
				org.With(s.requireOrgRole("member")).Post("/sessions/{sessionId}/desired-state", s.setDesiredState)
				org.Get("/sessions/{sessionId}/chat-events", s.chatEvents)
				org.With(s.requireOrgRole("member")).Post("/sessions/{sessionId}/messages", s.sendMessage)
				org.With(s.requireOrgRole("member")).Post("/sessions/{sessionId}/interrupt", s.interruptSession)
				org.Get("/sessions/{sessionId}/events", s.streamEvents)
				org.Get("/sessions/{sessionId}/scm", s.sessionSCM)
				org.Get("/sessions/{sessionId}/workspace/files", s.workspaceFiles)
				org.Get("/sessions/{sessionId}/workspace/file", s.workspaceFile)
				org.Get("/sessions/{sessionId}/workspace/diff", s.workspaceDiff)
				org.With(s.requireOrgRole("member")).Post("/sessions/{sessionId}/workspace/preview", s.workspacePreview)
				org.Post("/sessions/{sessionId}/workspace/preview-ticket", s.issueWorkspacePreview)
				org.Post("/sessions/{sessionId}/workspace/file-preview-ticket", s.issueWorkspaceFilePreview)
				org.Post("/sessions/{sessionId}/terminal-ticket", s.issueTerminalTicket)
				org.Get("/provider-connections", s.listProviderConnections)
				org.With(s.requireOrgRole("admin")).Patch("/provider-settings", s.updateProviderSettings)
				org.With(s.requireOrgRole("admin")).Put("/provider-connections/daytona", s.putDaytonaConnection)
				org.With(s.requireOrgRole("admin")).Put("/provider-connections/agents/{agent}", s.putAgentConnection)
				org.With(s.requireOrgRole("admin")).Delete("/provider-connections/agents/{agent}", s.deleteAgentConnection)
				org.Get("/repositories", s.listRepositories)
				org.Get("/github", s.getGitHub)
				org.With(s.requireOrgRole("admin")).Post("/github/install", s.createGitHubInstall)
				org.With(s.requireOrgRole("admin")).Post("/github/install/pending", s.pendingGitHubInstall)
				org.With(s.requireOrgRole("admin")).Post("/github/install/confirm", s.confirmGitHubInstall)
				org.With(s.requireOrgRole("admin")).Post("/github/sync", s.syncGitHub)
				org.With(s.requireOrgRole("admin")).Delete("/github/installations/{installationId}", s.deleteGitHubInstallation)
			})
		})
	})
	return router
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapped, r)
		status := wrapped.Status()
		if status == 0 {
			status = http.StatusOK
		}
		if status < http.StatusBadRequest && quietCloudRequest(r.URL.Path) {
			return
		}
		route := ""
		if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
			route = routeContext.RoutePattern()
		}
		logPath := redactedRequestLogPath(r.URL.Path)
		s.log.Info("AO Cloud request completed",
			"request_id", middleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", logPath,
			"route", route,
			"status", status,
			"bytes", wrapped.BytesWritten(),
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	})
}

func redactedRequestLogPath(path string) string {
	if strings.HasPrefix(path, "/api/cloud/v1/preview/") {
		return "/api/cloud/v1/preview/[redacted]"
	}
	if strings.HasPrefix(path, "/api/cloud/v1/share-links/") {
		return "/api/cloud/v1/share-links/[redacted]"
	}
	return path
}

func quietCloudRequest(path string) bool {
	if strings.HasPrefix(path, "/api/cloud/v1/preview/") {
		return true
	}
	switch path {
	case "/healthz", "/readyz",
		"/api/cloud/v1/worker/heartbeat",
		"/api/cloud/v1/worker/events":
		return true
	default:
		return false
	}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "Cloud persistence is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": "ao-cloud"})
}

func (s *Server) ensureAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := cloudauth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "A valid AO Cloud login is required.")
			return
		}
		account, err := s.ensurePrincipalAccount(r.Context(), &principal)
		if errors.Is(err, errExternalSignupDisabled) {
			writeError(w, r, http.StatusForbidden, "INVITATION_REQUIRED", "AO Cloud access requires an existing account or organization invitation.")
			return
		}
		if err != nil {
			s.internalError(w, r, "ensure account", err)
			return
		}
		ctx := cloudauth.ContextWithPrincipal(r.Context(), principal)
		ctx = context.WithValue(ctx, accountContextKey{}, account)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) ensurePrincipalAccount(
	ctx context.Context,
	principal *cloudauth.Principal,
) (clouddomain.Account, error) {
	if principal.AuthProvider != "" && principal.AuthProvider != "local" {
		createPersonalOrg := s.allowExternalSignup
		if !s.allowExternalSignup {
			canSignIn, err := s.store.ExternalAccountCanSignIn(
				ctx,
				principal.AuthProvider,
				principal.ExternalUserID,
				principal.Email,
			)
			if err != nil {
				return clouddomain.Account{}, err
			}
			if !canSignIn {
				return clouddomain.Account{}, errExternalSignupDisabled
			}
		}
		account, err := s.store.EnsureExternalAccount(
			ctx,
			principal.AuthProvider,
			principal.ExternalUserID,
			principal.Email,
			principal.DisplayName,
			createPersonalOrg,
		)
		if err != nil {
			return clouddomain.Account{}, err
		}
		principal.UserID = account.OwnerUserID
		return account, nil
	}
	return s.store.EnsureAccount(ctx, principal.UserID, principal.DisplayName)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	account, _ := accountFromContext(r.Context())
	organizations, err := s.store.ListUserOrganizations(r.Context(), principal.UserID)
	if err != nil {
		s.internalError(w, r, "list user organizations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]string{
			"id":          principal.UserID,
			"email":       principal.Email,
			"displayName": principal.DisplayName,
		},
		"account":         account,
		"organizations":   organizations,
		"sandboxProvider": s.sandboxProvider,
	})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROFILE", "Display name is required.")
		return
	}
	if len(input.DisplayName) > 120 {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROFILE", "Display name must be at most 120 characters.")
		return
	}
	user, err := s.store.UpdateUserProfile(r.Context(), principal.UserID, cloudpostgres.UpdateUserProfileInput{
		DisplayName: input.DisplayName,
	})
	if errors.Is(err, cloudpostgres.ErrInvalidUserProfile) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROFILE", "Display name is required.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrCloudUserNotFound) {
		writeError(w, r, http.StatusNotFound, "USER_NOT_FOUND", "The current user does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "update user profile", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) listOrgs(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	organizations, err := s.store.ListUserOrganizations(r.Context(), principal.UserID)
	if err != nil {
		s.internalError(w, r, "list user organizations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": organizations})
}

func (s *Server) createOrg(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	if !s.allowExternalSignup && principal.AuthProvider != "" && principal.AuthProvider != "local" {
		writeError(w, r, http.StatusForbidden, "ORG_CREATION_DISABLED", "Organization creation is invite-only in this AO Cloud deployment.")
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
		Kind        string `json:"kind"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	org, err := s.store.CreateOrganization(r.Context(), cloudpostgres.CreateOrganizationInput{
		UserID:      principal.UserID,
		DisplayName: input.DisplayName,
		Kind:        input.Kind,
	})
	if errors.Is(err, cloudpostgres.ErrInvalidOrganization) {
		writeError(w, r, http.StatusBadRequest, "INVALID_ORG", "Organization name is required.")
		return
	}
	if err != nil {
		s.internalError(w, r, "create organization", err)
		return
	}
	if org.Organization.Kind != "personal" {
		if _, err := s.store.SetOrgProviderSettings(r.Context(), org.Organization.ID, "personal_default"); err != nil {
			s.internalError(w, r, "set organization provider defaults", err)
			return
		}
		if err := s.syncPersonalDefaultAgentConnections(
			r.Context(),
			principal.UserID,
			org.Organization.ID,
		); err != nil {
			s.internalError(w, r, "sync organization provider defaults", err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"organization": org})
}

func (s *Server) updateOrg(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	if !orgRoleAtLeast(org.Membership.Role, "admin") {
		writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Only organization admins can update organization settings.")
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	updated, err := s.store.UpdateOrganization(r.Context(), org.Organization.ID, cloudpostgres.UpdateOrganizationInput{
		DisplayName: input.DisplayName,
	})
	if errors.Is(err, cloudpostgres.ErrInvalidOrganization) {
		writeError(w, r, http.StatusBadRequest, "INVALID_ORG", "Organization name is required.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrOrganizationNotFound) {
		writeError(w, r, http.StatusNotFound, "ORG_NOT_FOUND", "The organization does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "update organization", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": updated})
}

func (s *Server) listOrgMembers(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	members, err := s.store.ListOrgMembers(r.Context(), org.Organization.ID)
	if err != nil {
		s.internalError(w, r, "list org members", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) updateOrgMemberRole(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	targetUserID := strings.TrimSpace(chi.URLParam(r, "userId"))
	if targetUserID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_MEMBER", "Member user id is required.")
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Role = strings.TrimSpace(input.Role)
	if !validOrgRole(input.Role) {
		writeError(w, r, http.StatusBadRequest, "INVALID_ROLE", "Role must be owner, admin, member, or viewer.")
		return
	}
	if input.Role == "owner" && org.Membership.Role != "owner" {
		writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Only organization owners can grant owner role.")
		return
	}
	members, err := s.store.ListOrgMembers(r.Context(), org.Organization.ID)
	if err != nil {
		s.internalError(w, r, "list org members for role update", err)
		return
	}
	var target clouddomain.OrgMember
	ownerCount := 0
	targetFound := false
	for _, member := range members {
		if member.Membership.Role == "owner" {
			ownerCount++
		}
		if string(member.User.ID) == targetUserID {
			target = member
			targetFound = true
		}
	}
	if !targetFound {
		writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "The organization member does not exist.")
		return
	}
	if status, code, message, ok := validateOrgMemberRoleUpdate(
		principal.UserID,
		org.Membership.Role,
		target,
		ownerCount,
		input.Role,
	); !ok {
		writeError(w, r, status, code, message)
		return
	}
	member, err := s.store.UpdateOrgMemberRole(r.Context(), org.Organization.ID, targetUserID, input.Role)
	if errors.Is(err, cloudpostgres.ErrOrgMembershipNotFound) {
		writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "The organization member does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "update org member role", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": member})
}

func validateOrgMemberRoleUpdate(
	principalUserID string,
	actorRole string,
	target clouddomain.OrgMember,
	ownerCount int,
	nextRole string,
) (int, string, string, bool) {
	if string(target.User.ID) == principalUserID {
		return http.StatusForbidden, "ORG_ROLE_REQUIRED", "You cannot change your own organization role.", false
	}
	if target.Membership.Role == "owner" && actorRole != "owner" {
		return http.StatusForbidden, "ORG_ROLE_REQUIRED", "Only organization owners can change another owner's role.", false
	}
	if target.Membership.Role == "owner" && nextRole != "owner" && ownerCount <= 1 {
		return http.StatusConflict, "LAST_OWNER_REQUIRED", "An organization must keep at least one owner.", false
	}
	return 0, "", "", true
}

func (s *Server) listMyInvitations(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	invitations, err := s.store.ListUserInvitations(r.Context(), principal.UserID, principal.Email)
	if err != nil {
		s.internalError(w, r, "list user invitations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": invitations})
}

func (s *Server) listOrgInvitations(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	if !orgRoleAtLeast(org.Membership.Role, "admin") {
		writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Only organization admins can view invitations.")
		return
	}
	invitations, err := s.store.ListOrgInvitations(r.Context(), org.Organization.ID)
	if err != nil {
		s.internalError(w, r, "list org invitations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": invitations})
}

func (s *Server) createOrgInvitation(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	if !orgRoleAtLeast(org.Membership.Role, "admin") {
		writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Only organization admins can invite people.")
		return
	}
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	email, err := normalizeCloudEmail(input.Email)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EMAIL", "A valid invite email is required.")
		return
	}
	role := strings.TrimSpace(input.Role)
	if role == "" {
		role = "member"
	}
	if !validOrgRole(role) || role == "owner" {
		writeError(w, r, http.StatusBadRequest, "INVALID_ROLE", "Invite role must be admin, member, or viewer.")
		return
	}
	invitation, err := s.store.CreateOrgInvitation(
		r.Context(),
		org.Organization.ID,
		cloudpostgres.CreateOrgInvitationInput{
			Email:           email,
			InvitedByUserID: clouddomain.UserID(principal.UserID),
			Role:            role,
		},
	)
	if errors.Is(err, cloudpostgres.ErrOrgInvitationExists) {
		writeError(w, r, http.StatusConflict, "INVITATION_EXISTS", "This email already has a pending invitation.")
		return
	}
	if err != nil {
		s.internalError(w, r, "create org invitation", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": invitation})
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	membership, err := s.store.AcceptOrgInvitation(
		r.Context(),
		principal.UserID,
		principal.Email,
		chi.URLParam(r, "invitationId"),
	)
	if errors.Is(err, cloudpostgres.ErrOrgInvitationNotFound) {
		writeError(w, r, http.StatusNotFound, "INVITATION_NOT_FOUND", "The invitation is no longer available.")
		return
	}
	if err != nil {
		s.internalError(w, r, "accept org invitation", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"membership": membership})
}

func (s *Server) declineInvitation(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	err := s.store.DeclineOrgInvitation(
		r.Context(),
		principal.UserID,
		principal.Email,
		chi.URLParam(r, "invitationId"),
	)
	if errors.Is(err, cloudpostgres.ErrOrgInvitationNotFound) {
		writeError(w, r, http.StatusNotFound, "INVITATION_NOT_FOUND", "The invitation is no longer available.")
		return
	}
	if err != nil {
		s.internalError(w, r, "decline org invitation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	if !orgRoleAtLeast(org.Membership.Role, "admin") {
		writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Only organization admins can revoke invitations.")
		return
	}
	err := s.store.RevokeOrgInvitation(r.Context(), org.Organization.ID, chi.URLParam(r, "invitationId"))
	if errors.Is(err, cloudpostgres.ErrOrgInvitationNotFound) {
		writeError(w, r, http.StatusNotFound, "INVITATION_NOT_FOUND", "The invitation is no longer available.")
		return
	}
	if err != nil {
		s.internalError(w, r, "revoke org invitation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireOrg(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := cloudauth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "A valid AO Cloud login is required.")
			return
		}
		orgID := clouddomain.OrgID(strings.TrimSpace(chi.URLParam(r, "orgId")))
		if orgID == "" {
			writeError(w, r, http.StatusBadRequest, "ORG_REQUIRED", "An organization is required.")
			return
		}
		org, err := s.store.GetOrgMembership(r.Context(), principal.UserID, orgID)
		if errors.Is(err, cloudpostgres.ErrOrgMembershipNotFound) {
			shared, sharedOK, sharedErr := s.sharedAccessForOrg(r.Context(), principal.UserID, orgID)
			if sharedErr != nil {
				s.internalError(w, r, "authorize shared organization", sharedErr)
				return
			}
			if !sharedOK {
				writeError(w, r, http.StatusForbidden, "ORG_FORBIDDEN", "You do not have access to this organization.")
				return
			}
			if !sharedProjectRequestAllowed(r, orgID) {
				writeError(w, r, http.StatusForbidden, "PROJECT_SHARE_SCOPE_REQUIRED", "This share grants access only to its project and sessions.")
				return
			}
			org = clouddomain.UserOrganization{
				Organization: clouddomain.Organization{
					ID:          orgID,
					DisplayName: "Shared",
					Kind:        "shared",
					Status:      "active",
				},
				Membership: clouddomain.OrgMembership{
					OrgID:  orgID,
					UserID: clouddomain.UserID(principal.UserID),
					Role:   "viewer",
					Status: "active",
				},
			}
			account := clouddomain.Account{
				ID:          clouddomain.AccountID(org.Organization.ID),
				OwnerUserID: principal.UserID,
				DisplayName: org.Organization.DisplayName,
				CreatedAt:   org.Organization.CreatedAt,
				UpdatedAt:   org.Organization.UpdatedAt,
			}
			ctx := context.WithValue(r.Context(), orgContextKey{}, org)
			ctx = context.WithValue(ctx, accountContextKey{}, account)
			ctx = context.WithValue(ctx, sharedProjectAccessContextKey{}, shared)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if err != nil {
			s.internalError(w, r, "authorize organization", err)
			return
		}
		account := clouddomain.Account{
			ID:          clouddomain.AccountID(org.Organization.ID),
			OwnerUserID: principal.UserID,
			DisplayName: org.Organization.DisplayName,
			CreatedAt:   org.Organization.CreatedAt,
			UpdatedAt:   org.Organization.UpdatedAt,
		}
		ctx := context.WithValue(r.Context(), orgContextKey{}, org)
		ctx = context.WithValue(ctx, accountContextKey{}, account)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sharedProjectRequestAllowed(r *http.Request, orgID clouddomain.OrgID) bool {
	prefix := "/api/cloud/v1/orgs/" + string(orgID)
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == r.URL.Path {
		return false
	}
	switch path {
	case "/projects":
		return r.Method == http.MethodGet
	case "/sessions":
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	default:
		return strings.HasPrefix(path, "/sessions/")
	}
}

func (s *Server) sharedAccessForOrg(
	ctx context.Context,
	userID string,
	orgID clouddomain.OrgID,
) (sharedProjectAccess, bool, error) {
	grants, err := s.store.ListSharedProjectGrants(ctx, userID)
	if err != nil {
		return sharedProjectAccess{}, false, err
	}
	access := newSharedProjectAccess(grants, orgID)
	return access, len(access.Projects) > 0, nil
}

func newSharedProjectAccess(
	grants []cloudpostgres.SharedProjectGrant,
	orgID clouddomain.OrgID,
) sharedProjectAccess {
	access := sharedProjectAccess{Projects: map[clouddomain.ProjectID]sharedProjectScope{}}
	for _, grant := range grants {
		if grant.OrgID != orgID {
			continue
		}
		if !validProjectShareRole(grant.Role) {
			continue
		}
		scope := access.Projects[grant.Project.ID]
		if scope.SessionRoles == nil {
			scope.SessionRoles = map[clouddomain.SessionID][]string{}
		}
		if grant.Session == nil {
			scope.ProjectRoles = append(scope.ProjectRoles, grant.Role)
		} else {
			if grant.Session.ID == "" || grant.Session.ProjectID != grant.Project.ID {
				continue
			}
			scope.SessionRoles[grant.Session.ID] = append(scope.SessionRoles[grant.Session.ID], grant.Role)
		}
		access.Projects[grant.Project.ID] = scope
	}
	return access
}

func (s *Server) requireOrgRole(required string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
				if required != "member" {
					writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "A project share does not grant organization administration access.")
					return
				}
				sessionID := clouddomain.SessionID(strings.TrimSpace(chi.URLParam(r, "sessionId")))
				if sessionID == "" {
					next.ServeHTTP(w, r)
					return
				}
				account, _ := accountFromContext(r.Context())
				session, err := s.store.GetSession(r.Context(), account.ID, sessionID)
				if err != nil || !shared.canOperateSession(session.ProjectID, session.ID) {
					writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Viewer access is read-only for this project.")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			org, ok := orgFromContext(r.Context())
			if !ok || !orgRoleAtLeast(org.Membership.Role, required) {
				writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Your organization role cannot perform this action.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func normalizeCloudEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", errors.New("invalid email")
	}
	return email, nil
}

func validOrgRole(role string) bool {
	switch role {
	case "owner", "admin", "member", "viewer":
		return true
	default:
		return false
	}
}

func validProjectShareRole(role string) bool {
	return role == "viewer" || role == "editor"
}

func orgRoleAtLeast(actual, required string) bool {
	return orgRoleRank(actual) >= orgRoleRank(required)
}

func orgRoleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "admin":
		return 2
	case "member", "editor":
		return 1
	case "viewer":
		return 0
	default:
		return -1
	}
}

func (s *Server) localSignUp(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	principal, err := s.localAuth.SignUp(r.Context(), input.Email, input.Password, input.DisplayName)
	if errors.Is(err, cloudpostgres.ErrLocalUserExists) {
		writeError(w, r, http.StatusConflict, "EMAIL_EXISTS", "An account with this email already exists.")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CREDENTIALS", err.Error())
		return
	}
	writeLocalAuthResponse(w, http.StatusCreated, principal)
}

func (s *Server) localLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	principal, err := s.localAuth.Login(r.Context(), input.Email, input.Password)
	if errors.Is(err, cloudauth.ErrUnauthenticated) {
		writeError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email or password is incorrect.")
		return
	}
	if err != nil {
		s.internalError(w, r, "local login", err)
		return
	}
	writeLocalAuthResponse(w, http.StatusOK, principal)
}

func (s *Server) localLogout(w http.ResponseWriter, r *http.Request) {
	principal, ok := cloudauth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "A valid AO Cloud login is required.")
		return
	}
	if err := s.localAuth.Logout(r.Context(), principal.AccessToken); err != nil {
		s.internalError(w, r, "local logout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeLocalAuthResponse(w http.ResponseWriter, status int, principal cloudauth.Principal) {
	writeJSON(w, status, map[string]any{
		"accessToken": principal.AccessToken,
		"tokenType":   "Bearer",
		"user": map[string]string{
			"id":          principal.UserID,
			"email":       principal.Email,
			"displayName": principal.DisplayName,
		},
	})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if _, shared := sharedProjectAccessFromContext(r.Context()); shared {
		writeError(w, r, http.StatusForbidden, "PROJECT_FORBIDDEN", "A project share does not grant permission to create other projects.")
		return
	}
	account, _ := accountFromContext(r.Context())
	var input struct {
		DisplayName        string          `json:"displayName"`
		RepositoryURL      string          `json:"repositoryUrl"`
		DefaultBranch      string          `json:"defaultBranch"`
		GitHubRepositoryID *int64          `json:"githubRepositoryId"`
		Config             json.RawMessage `json:"config"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.RepositoryURL = strings.TrimSpace(input.RepositoryURL)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	if input.DisplayName == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROJECT", "A project name is required.")
		return
	}
	if s.githubMode() == "github-app" {
		if input.GitHubRepositoryID == nil || *input.GitHubRepositoryID <= 0 || s.githubStore == nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PROJECT", "An authorized GitHub repository is required.")
			return
		}
		repositories, err := s.githubStore.ListActiveGitHubRepositories(
			r.Context(),
			clouddomain.OrgID(account.ID),
		)
		if err != nil {
			s.internalError(w, r, "load authorized GitHub repository", err)
			return
		}
		var selected *clouddomain.GitHubGrantedRepository
		for index := range repositories {
			if repositories[index].Repository.ID == *input.GitHubRepositoryID {
				selected = &repositories[index]
				break
			}
		}
		if selected == nil {
			writeError(w, r, http.StatusForbidden, "REPOSITORY_NOT_AUTHORIZED", "The GitHub repository is not authorized for this organization.")
			return
		}
		input.RepositoryURL = selected.Repository.HTMLURL
		input.DefaultBranch = selected.Repository.DefaultBranch
	} else if !validGitHubRepositoryURL(input.RepositoryURL) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROJECT", "A name and HTTPS GitHub repository URL are required.")
		return
	}
	if input.DefaultBranch == "" {
		input.DefaultBranch = "main"
	}
	agentConnected, err := s.hasAgentConnection(r.Context(), account.ID)
	if err != nil {
		s.internalError(w, r, "check agent connection", err)
		return
	}
	if !agentConnected {
		writeError(
			w,
			r,
			http.StatusBadRequest,
			"AGENT_CONNECTION_REQUIRED",
			"Connect a coding agent before creating a cloud project.",
		)
		return
	}
	project, err := s.store.CreateProject(r.Context(), account.ID, cloudpostgres.CreateProjectInput{
		DisplayName:        input.DisplayName,
		RepositoryURL:      input.RepositoryURL,
		DefaultBranch:      input.DefaultBranch,
		GitHubRepositoryID: input.GitHubRepositoryID,
		Config:             input.Config,
	})
	if errors.Is(err, cloudpostgres.ErrProjectExists) {
		writeError(w, r, http.StatusConflict, "PROJECT_EXISTS", "This repository is already registered.")
		return
	}
	if err != nil {
		s.internalError(w, r, "create project", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": project})
}

func (s *Server) createProjectShareLink(w http.ResponseWriter, r *http.Request) {
	if _, shared := sharedProjectAccessFromContext(r.Context()); shared {
		writeError(w, r, http.StatusForbidden, "PROJECT_SHARE_FORBIDDEN", "Shared project access cannot be reshared.")
		return
	}
	account, _ := accountFromContext(r.Context())
	org, _ := orgFromContext(r.Context())
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	projectID := clouddomain.ProjectID(strings.TrimSpace(chi.URLParam(r, "projectId")))
	if projectID == "" {
		writeError(w, r, http.StatusBadRequest, "PROJECT_REQUIRED", "A project is required.")
		return
	}
	var input struct {
		SessionID       clouddomain.SessionID `json:"sessionId"`
		Role            string                `json:"role"`
		AccessScope     string                `json:"accessScope"`
		RecipientEmails []string              `json:"recipientEmails"`
		RecipientOrgIDs []clouddomain.OrgID   `json:"recipientOrgIds"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	role := strings.TrimSpace(input.Role)
	if role == "" {
		role = "viewer"
	}
	if !validProjectShareRole(role) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SHARE_ROLE", "Share role must be viewer or editor.")
		return
	}
	accessScope := strings.TrimSpace(input.AccessScope)
	if accessScope == "" {
		accessScope = "anyone"
	}
	if accessScope != "anyone" && accessScope != "restricted" {
		writeError(w, r, http.StatusBadRequest, "INVALID_SHARE_SCOPE", "Share access must be anyone or restricted.")
		return
	}
	if accessScope == "restricted" && len(input.RecipientEmails) == 0 && len(input.RecipientOrgIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "SHARE_RECIPIENT_REQUIRED", "Restricted links require at least one email or organization.")
		return
	}
	if len(input.RecipientOrgIDs) > 0 {
		organizations, err := s.store.ListUserOrganizations(r.Context(), principal.UserID)
		if err != nil {
			s.internalError(w, r, "validate share recipient organizations", err)
			return
		}
		allowed := map[clouddomain.OrgID]struct{}{}
		for _, organization := range organizations {
			allowed[organization.Organization.ID] = struct{}{}
		}
		for _, orgID := range input.RecipientOrgIDs {
			if _, ok := allowed[orgID]; !ok {
				writeError(w, r, http.StatusForbidden, "SHARE_ORG_FORBIDDEN", "You can only restrict links to organizations you belong to.")
				return
			}
		}
	}
	if _, err := s.store.GetProject(r.Context(), account.ID, projectID); errors.Is(err, cloudpostgres.ErrProjectNotFound) {
		writeError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "The cloud project does not exist.")
		return
	} else if err != nil {
		s.internalError(w, r, "load share project", err)
		return
	}
	if input.SessionID != "" {
		session, err := s.store.GetSession(r.Context(), account.ID, input.SessionID)
		if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist in this project.")
			return
		}
		if err != nil {
			s.internalError(w, r, "load share session", err)
			return
		}
		if session.ProjectID != projectID {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist in this project.")
			return
		}
	}
	token, err := newShareToken()
	if err != nil {
		s.internalError(w, r, "generate share token", err)
		return
	}
	link, err := s.store.CreateProjectShareLink(r.Context(), cloudpostgres.CreateProjectShareLinkInput{
		OrgID:           org.Organization.ID,
		ProjectID:       projectID,
		SessionID:       input.SessionID,
		CreatedByUserID: clouddomain.UserID(principal.UserID),
		Role:            role,
		Token:           token,
		AccessScope:     accessScope,
		RecipientEmails: input.RecipientEmails,
		RecipientOrgIDs: input.RecipientOrgIDs,
	})
	if errors.Is(err, cloudpostgres.ErrProjectShareInvalidRecipient) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SHARE_RECIPIENT", "Enter valid email recipients.")
		return
	}
	if err != nil {
		s.internalError(w, r, "create project share link", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"shareLink": link, "token": token})
}

func (s *Server) listProjectShareAccess(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	projectID := clouddomain.ProjectID(strings.TrimSpace(chi.URLParam(r, "projectId")))
	if projectID == "" {
		writeError(w, r, http.StatusBadRequest, "PROJECT_REQUIRED", "A project is required.")
		return
	}
	access, err := s.store.ListProjectShareAccess(r.Context(), org.Organization.ID, projectID)
	if err != nil {
		s.internalError(w, r, "list project share access", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access": access})
}

func (s *Server) updateProjectShareGrant(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	projectID := clouddomain.ProjectID(strings.TrimSpace(chi.URLParam(r, "projectId")))
	grantID := strings.TrimSpace(chi.URLParam(r, "grantId"))
	var input struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	role := strings.TrimSpace(input.Role)
	if !validProjectShareRole(role) {
		writeError(w, r, http.StatusBadRequest, "INVALID_SHARE_ROLE", "Share role must be viewer or editor.")
		return
	}
	grant, err := s.store.UpdateProjectShareGrantRole(r.Context(), org.Organization.ID, projectID, grantID, role)
	if errors.Is(err, cloudpostgres.ErrProjectShareGrantNotFound) {
		writeError(w, r, http.StatusNotFound, "SHARE_GRANT_NOT_FOUND", "That shared access no longer exists.")
		return
	}
	if err != nil {
		s.internalError(w, r, "update project share grant", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant})
}

func (s *Server) revokeProjectShareGrant(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	projectID := clouddomain.ProjectID(strings.TrimSpace(chi.URLParam(r, "projectId")))
	grantID := strings.TrimSpace(chi.URLParam(r, "grantId"))
	err := s.store.RevokeProjectShareGrant(r.Context(), org.Organization.ID, projectID, grantID)
	if errors.Is(err, cloudpostgres.ErrProjectShareGrantNotFound) {
		writeError(w, r, http.StatusNotFound, "SHARE_GRANT_NOT_FOUND", "That shared access no longer exists.")
		return
	}
	if err != nil {
		s.internalError(w, r, "revoke project share grant", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeProjectShareLink(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	projectID := clouddomain.ProjectID(strings.TrimSpace(chi.URLParam(r, "projectId")))
	linkID := strings.TrimSpace(chi.URLParam(r, "linkId"))
	err := s.store.RevokeProjectShareLink(r.Context(), org.Organization.ID, projectID, linkID)
	if errors.Is(err, cloudpostgres.ErrProjectShareLinkNotFound) {
		writeError(w, r, http.StatusNotFound, "SHARE_LINK_NOT_FOUND", "That share link no longer exists.")
		return
	}
	if err != nil {
		s.internalError(w, r, "revoke project share link", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) redeemProjectShareLink(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" {
		writeError(w, r, http.StatusBadRequest, "SHARE_TOKEN_REQUIRED", "A share token is required.")
		return
	}
	grant, err := s.store.RedeemProjectShareLink(r.Context(), token, principal.UserID)
	if errors.Is(err, cloudpostgres.ErrProjectShareLinkNotFound) {
		writeError(w, r, http.StatusNotFound, "SHARE_LINK_NOT_FOUND", "This share link is no longer available.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrProjectShareSelfRedeem) {
		writeError(w, r, http.StatusBadRequest, "SHARE_SELF_REDEEM", "You already own this shared project.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrProjectShareUnauthorized) {
		writeError(w, r, http.StatusForbidden, "SHARE_RECIPIENT_REQUIRED", "This share link is not available for your account.")
		return
	}
	if err != nil {
		s.internalError(w, r, "redeem project share link", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": grant})
}

func (s *Server) listSharedProjects(w http.ResponseWriter, r *http.Request) {
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	grants, err := s.store.ListSharedProjectGrants(r.Context(), principal.UserID)
	if err != nil {
		s.internalError(w, r, "list shared projects", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": grants})
}

func newShareToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	projects, err := s.store.ListProjects(r.Context(), account.ID)
	if err != nil {
		s.internalError(w, r, "list projects", err)
		return
	}
	if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
		filtered := projects[:0]
		for _, project := range projects {
			if shared.allowsProject(project.ID) {
				filtered = append(filtered, project)
			}
		}
		projects = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required.")
		return
	}
	var input struct {
		ProjectID            clouddomain.ProjectID       `json:"projectId"`
		Kind                 string                      `json:"kind"`
		Harness              string                      `json:"harness"`
		DisplayName          string                      `json:"displayName"`
		Branch               string                      `json:"branch"`
		Prompt               string                      `json:"prompt"`
		Resource             clouddomain.ResourceProfile `json:"resource"`
		ProviderConnectionID string                      `json:"providerConnectionId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Kind == "" {
		input.Kind = "worker"
	}
	if input.Kind != "worker" && input.Kind != "orchestrator" {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_KIND", "kind must be worker or orchestrator.")
		return
	}
	if !shareddomain.AgentHarness(input.Harness).IsKnown() {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT", "The selected coding agent is not supported.")
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.ProjectID == "" || input.DisplayName == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION", "projectId and displayName are required.")
		return
	}
	if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
		if !shared.allowsProject(input.ProjectID) {
			writeError(w, r, http.StatusForbidden, "PROJECT_FORBIDDEN", "This shared link does not grant access to that project.")
			return
		}
		if role, allowed := shared.projectRole(input.ProjectID); !allowed || role != "editor" {
			writeError(w, r, http.StatusForbidden, "ORG_ROLE_REQUIRED", "Viewer access is read-only for this project.")
			return
		}
	}
	defaultResource := clouddomain.DefaultResourceProfile()
	if input.Resource == (clouddomain.ResourceProfile{}) {
		input.Resource = defaultResource
	}
	if input.Resource != defaultResource {
		writeError(
			w,
			r,
			http.StatusBadRequest,
			"INVALID_RESOURCE_PROFILE",
			"Cloud V1 requires 4 CPU, 8 GiB memory, and 10 GiB disk.",
		)
		return
	}
	credential, err := s.loadAgentCredential(r.Context(), account.ID, input.Harness)
	if errors.Is(err, errAgentConnectionRequired) {
		writeError(
			w,
			r,
			http.StatusBadRequest,
			"AGENT_CONNECTION_REQUIRED",
			"Connect "+input.Harness+" before creating a cloud session.",
		)
		return
	}
	if err != nil {
		s.internalError(w, r, "validate agent connection", err)
		return
	}
	if credential != nil {
		credential.Secret = ""
	}
	result, err := s.store.CreateSession(r.Context(), account.ID, cloudpostgres.CreateSessionInput{
		IdempotencyKey:           idempotencyKey,
		ProjectID:                input.ProjectID,
		Kind:                     input.Kind,
		Harness:                  input.Harness,
		DisplayName:              input.DisplayName,
		Branch:                   strings.TrimSpace(input.Branch),
		Prompt:                   input.Prompt,
		Resource:                 input.Resource,
		Provider:                 s.sandboxProvider,
		ProviderConnectionID:     providerConnectionID(s.sandboxProvider, input.ProviderConnectionID),
		MaxActiveSandboxesPerOrg: s.maxActiveSandboxesPerOrg,
	})
	if errors.Is(err, cloudpostgres.ErrIdempotencyConflict) {
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used for another command.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrProjectNotFound) {
		writeError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "The cloud project does not exist.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrProviderConnectionNotFound) {
		writeError(w, r, http.StatusBadRequest, "PROVIDER_CONNECTION_NOT_FOUND", "The selected Daytona connection does not exist.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrActiveOrchestrator) {
		writeError(w, r, http.StatusConflict, "ORCHESTRATOR_EXISTS", "This project already has an active orchestrator.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrSandboxQuotaExceeded) {
		writeError(
			w,
			r,
			http.StatusConflict,
			"SANDBOX_QUOTA_EXCEEDED",
			fmt.Sprintf(
				"This organization already has %d active sandboxes. Delete a worker machine to make room before starting another one.",
				s.maxActiveSandboxesPerOrg,
			),
		)
		return
	}
	if err != nil {
		s.internalError(w, r, "create session", err)
		return
	}
	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func providerConnectionID(provider, connectionID string) string {
	if provider != "daytona" {
		return ""
	}
	return connectionID
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	sessions, err := s.store.ListSessions(r.Context(), account.ID)
	if err != nil {
		s.internalError(w, r, "list sessions", err)
		return
	}
	if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
		filtered := sessions[:0]
		for _, session := range sessions {
			if _, allowed := shared.sessionRole(session.ProjectID, session.ID); allowed {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	session, err := s.store.GetSession(
		r.Context(),
		account.ID,
		clouddomain.SessionID(chi.URLParam(r, "sessionId")),
	)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "get session", err)
		return
	}
	if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
		if _, allowed := shared.sessionRole(session.ProjectID, session.ID); !allowed {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session})
}

func (s *Server) activeTurn(w http.ResponseWriter, r *http.Request) {
	_, session, ok := s.authorizedSession(w, r, "read active turn")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"turn": session.ActiveTurn})
}

func (s *Server) chatEvents(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "authorize chat event replay")
	if !ok {
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AFTER", "after must be a non-negative integer.")
		return
	}
	limit, err := parseLimit(r, 500)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer no greater than 500.")
		return
	}
	events, err := s.events.ReplayChat(r.Context(), account.ID, session.ID, after, limit)
	if err != nil {
		s.internalError(w, r, "replay chat events", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "authorize cloud message")
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required.")
		return
	}
	var input struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Text) == "" || len([]byte(input.Text)) > 64<<10 {
		writeError(w, r, http.StatusBadRequest, "INVALID_MESSAGE", "text must be non-empty and at most 64 KiB.")
		return
	}
	event, err := s.events.AppendUserMessage(
		r.Context(),
		account.ID,
		session.ID,
		idempotencyKey,
		input.Text,
	)
	if errors.Is(err, cloudpostgres.ErrIdempotencyConflict) {
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used for another command.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrActiveTurn) {
		writeError(w, r, http.StatusConflict, "TURN_ACTIVE", "The cloud session already has a response in progress.")
		return
	}
	if err != nil {
		s.internalError(w, r, "append cloud message", err)
		return
	}
	if err := s.wakeSessionForMessage(r.Context(), account.ID, session.ID); err != nil {
		s.internalError(w, r, "wake cloud session for message", err)
		return
	}
	command := cloudworkerhub.Command{
		Type:     "prompt",
		Data:     base64.StdEncoding.EncodeToString([]byte(input.Text)),
		Sequence: event.Sequence,
	}
	if err := s.workerHub.Send(session.ID, command); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "WORKER_BACKPRESSURE", "The message was saved but worker delivery is temporarily unavailable.")
		return
	}
	s.log.Info("cloud turn queued",
		"request_id", middleware.GetReqID(r.Context()),
		"session_id", session.ID,
		"message_sequence", event.Sequence,
	)
	writeJSON(w, http.StatusAccepted, map[string]any{"event": event})
}

func (s *Server) wakeSessionForMessage(
	ctx context.Context,
	accountID clouddomain.AccountID,
	sessionID clouddomain.SessionID,
) error {
	sandbox, err := s.store.GetSandbox(ctx, accountID, sessionID)
	if err != nil {
		return err
	}
	if sandbox.DesiredState != "running" {
		if err := s.store.SetSandboxDesiredState(ctx, accountID, sessionID, "running"); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{
			"state":  "running",
			"source": "message",
		})
		if _, err := s.events.Append(
			ctx,
			accountID,
			sessionID,
			"sandbox.desired_state_changed",
			payload,
		); err != nil {
			return err
		}
	}
	if _, err := s.store.TransitionActiveTurn(ctx, accountID, sessionID, "provisioning", ""); err != nil {
		return err
	}
	return s.store.UpdateSessionActivity(ctx, accountID, sessionID, "active")
}

func (s *Server) interruptSession(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	account.ID = tenantAccountIDFromContext(r.Context())
	sessionID := clouddomain.SessionID(chi.URLParam(r, "sessionId"))
	session, err := s.store.GetSession(r.Context(), account.ID, sessionID)
	if err != nil {
		if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
			return
		}
		s.internalError(w, r, "authorize cloud interrupt", err)
		return
	}
	payloadData := map[string]string{"source": "browser"}
	if session.ActiveTurn != nil {
		payloadData["turnId"] = session.ActiveTurn.ID
	}
	payload, _ := json.Marshal(payloadData)
	event, err := s.events.Append(
		r.Context(),
		account.ID,
		sessionID,
		"chat.interrupt_requested",
		payload,
	)
	if err != nil {
		s.internalError(w, r, "append interrupt request", err)
		return
	}
	nextState := "cancel_requested"
	if !session.RuntimeConnected {
		nextState = "completed"
	}
	if _, err := s.store.TransitionActiveTurn(
		r.Context(),
		account.ID,
		sessionID,
		nextState,
		"",
	); err != nil {
		s.internalError(w, r, "request turn cancellation", err)
		return
	}
	if !session.RuntimeConnected {
		if err := s.store.UpdateSessionActivity(r.Context(), account.ID, sessionID, "idle"); err != nil {
			s.internalError(w, r, "update offline interrupt activity", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"event": event})
		return
	}
	if err := s.store.UpdateSessionActivity(r.Context(), account.ID, sessionID, "active"); err != nil {
		s.internalError(w, r, "update interrupt activity", err)
		return
	}
	if err := s.workerHub.Send(sessionID, cloudworkerhub.Command{
		Type:     "interrupt",
		Sequence: event.Sequence,
	}); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "WORKER_BACKPRESSURE", "The interrupt was saved but worker delivery is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"event": event})
}

func (s *Server) sessionSCM(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "read cloud SCM")
	if !ok {
		return
	}
	scm, err := s.store.SessionSCM(r.Context(), account.ID, session.ID)
	if err != nil {
		s.internalError(w, r, "read cloud SCM", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scm": scm})
}

func (s *Server) authorizedSession(
	w http.ResponseWriter,
	r *http.Request,
	action string,
) (clouddomain.Account, clouddomain.Session, bool) {
	account, _ := accountFromContext(r.Context())
	account.ID = tenantAccountIDFromContext(r.Context())
	session, err := s.store.GetSession(
		r.Context(),
		account.ID,
		clouddomain.SessionID(chi.URLParam(r, "sessionId")),
	)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
		return clouddomain.Account{}, clouddomain.Session{}, false
	}
	if err != nil {
		s.internalError(w, r, action, err)
		return clouddomain.Account{}, clouddomain.Session{}, false
	}
	if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
		if _, allowed := shared.sessionRole(session.ProjectID, session.ID); !allowed {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
			return clouddomain.Account{}, clouddomain.Session{}, false
		}
	}
	return account, session, true
}

func (s *Server) setDesiredState(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "set cloud session desired state")
	if !ok {
		return
	}
	var input struct {
		State string `json:"state"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	switch input.State {
	case "running", "paused", "deleted":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_DESIRED_STATE", "state must be running, paused, or deleted.")
		return
	}
	if input.State == "deleted" && !canDeleteSessionSandbox(session.Kind) {
		writeError(w, r, http.StatusConflict, "SESSION_DELETE_UNSUPPORTED", "This session kind cannot delete a sandbox.")
		return
	}
	sessionID := session.ID
	if err := s.store.SetSandboxDesiredState(r.Context(), account.ID, sessionID, input.State); err != nil {
		if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
			return
		}
		s.internalError(w, r, "set desired state", err)
		return
	}
	if input.State == "deleted" {
		if _, err := s.store.TransitionActiveTurn(
			r.Context(),
			account.ID,
			sessionID,
			"failed",
			"Worker machine deleted.",
		); err != nil {
			s.internalError(w, r, "finish deleted worker turn", err)
			return
		}
		if err := s.store.UpdateSessionActivity(r.Context(), account.ID, sessionID, "idle"); err != nil {
			s.internalError(w, r, "reset deleted worker activity", err)
			return
		}
	}
	eventPayload, _ := json.Marshal(map[string]string{"state": input.State})
	if _, err := s.events.Append(r.Context(), account.ID, sessionID, "sandbox.desired_state_changed", eventPayload); err != nil {
		s.internalError(w, r, "append desired-state event", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "state": input.State})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "delete cloud session")
	if !ok {
		return
	}
	if !canDeleteSessionSandbox(session.Kind) {
		writeError(w, r, http.StatusConflict, "SESSION_DELETE_UNSUPPORTED", "This session kind cannot delete a sandbox.")
		return
	}
	if err := s.deleteSessionSandbox(r.Context(), account.ID, session.ID); err != nil {
		s.internalError(w, r, "delete session sandbox", err)
		return
	}
	if err := s.store.DeleteSession(r.Context(), account.ID, session.ID); err != nil {
		if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
			return
		}
		s.internalError(w, r, "delete cloud session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func canDeleteSessionSandbox(kind string) bool {
	return kind == "worker" || kind == "orchestrator"
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	account.ID = tenantAccountIDFromContext(r.Context())
	projectID := clouddomain.ProjectID(chi.URLParam(r, "projectId"))
	if projectID == "" {
		writeError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "projectId is required.")
		return
	}
	if _, err := s.store.GetProject(r.Context(), account.ID, projectID); err != nil {
		if errors.Is(err, cloudpostgres.ErrProjectNotFound) {
			writeError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "The cloud project does not exist.")
			return
		}
		s.internalError(w, r, "load project for deletion", err)
		return
	}
	sessions, err := s.store.ListSessions(r.Context(), account.ID)
	if err != nil {
		s.internalError(w, r, "list project sessions for deletion", err)
		return
	}
	for _, session := range sessions {
		if session.ProjectID != projectID {
			continue
		}
		if err := s.deleteSessionSandbox(r.Context(), account.ID, session.ID); err != nil {
			s.internalError(w, r, "delete project session sandbox", err)
			return
		}
	}
	if err := s.store.DeleteProject(r.Context(), account.ID, projectID); err != nil {
		if errors.Is(err, cloudpostgres.ErrProjectNotFound) {
			writeError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "The cloud project does not exist.")
			return
		}
		s.internalError(w, r, "delete cloud project", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSessionSandbox(
	ctx context.Context,
	accountID clouddomain.AccountID,
	sessionID clouddomain.SessionID,
) error {
	sandbox, err := s.store.GetSandbox(ctx, accountID, sessionID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sandbox.ProviderEnvironmentID == "" {
		return nil
	}
	if s.sandboxProviders == nil {
		return errors.New("sandbox provider resolver is not configured")
	}
	provider, err := s.sandboxProviders.Resolve(ctx, sandbox)
	if err != nil {
		return err
	}
	if err := provider.Delete(ctx, cloudsandbox.ID(sandbox.ProviderEnvironmentID)); err != nil &&
		!errors.Is(err, cloudsandbox.ErrNotFound) {
		return err
	}
	return nil
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "authorize event stream")
	if !ok {
		return
	}
	sessionID := session.ID
	after, err := parseAfter(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AFTER", "after must be a non-negative integer.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "SSE_UNSUPPORTED", "Streaming is unavailable.")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	live := make(chan clouddomain.Event, 1024)
	unsubscribe := s.events.Subscribe(account.ID, sessionID, func(event clouddomain.Event) {
		select {
		case live <- event:
		default:
			cancel()
		}
	})
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sent := after
	if err := s.writeEventReplay(ctx, w, flusher, account.ID, sessionID, &sent); err != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-live:
			if err := s.writeEventReplay(ctx, w, flusher, account.ID, sessionID, &sent); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) writeEventReplay(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	accountID clouddomain.AccountID,
	sessionID clouddomain.SessionID,
	sent *int64,
) error {
	for {
		replayed, err := s.events.Replay(ctx, accountID, sessionID, *sent, 500)
		if err != nil {
			return err
		}
		for _, event := range replayed {
			if err := writeSSE(w, flusher, event, sent); err != nil {
				return err
			}
		}
		if len(replayed) < 500 {
			return nil
		}
	}
}

func (s *Server) workerBootstrap(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BootstrapToken string   `json:"bootstrapToken"`
		Version        string   `json:"version"`
		Capabilities   []string `json:"capabilities"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ticket, err := s.store.RedeemWorkerBootstrapTicket(
		r.Context(),
		input.BootstrapToken,
	)
	if errors.Is(err, cloudpostgres.ErrInvalidTicket) {
		writeError(w, r, http.StatusUnauthorized, "INVALID_BOOTSTRAP", "Worker bootstrap token is invalid or expired.")
		return
	}
	if err != nil {
		s.internalError(w, r, "consume worker bootstrap", err)
		return
	}
	launchSpec, err := s.store.WorkerLaunchSpec(r.Context(), ticket.AccountID, ticket.SessionID)
	if err != nil {
		s.internalError(w, r, "load worker launch spec", err)
		return
	}
	agentCredential, err := s.loadAgentCredential(
		r.Context(),
		ticket.AccountID,
		launchSpec.Session.Harness,
	)
	if errors.Is(err, errAgentConnectionRequired) {
		writeError(
			w,
			r,
			http.StatusBadRequest,
			"AGENT_CONNECTION_REQUIRED",
			"The coding-agent connection required by this session is unavailable.",
		)
		return
	}
	if err != nil {
		s.internalError(w, r, "load worker agent credential", err)
		return
	}
	if agentCredential != nil {
		defer func() { agentCredential.Secret = "" }()
	}
	localGitHubToken := ""
	if includeLocalGitHubToken(s.sandboxProvider, s.githubMode(), s.localGitHub != nil) {
		localGitHubToken, err = s.localGitHub.Token(r.Context())
		if err != nil {
			s.internalError(w, r, "load local GitHub credential for worker", err)
			return
		}
	}
	epoch := ticket.WorkerEpoch
	if epoch <= 0 {
		writeError(
			w,
			r,
			http.StatusInternalServerError,
			"BOOTSTRAP_FAILED",
			"Worker bootstrap identity was not assigned.",
		)
		return
	}
	workerID := cloudworker.NextWorkerID(ticket.SessionID, epoch)
	if err := s.store.RegisterWorkerBootstrap(
		r.Context(),
		ticket.AccountID,
		ticket.SessionID,
		workerID,
		input.Version,
		epoch,
		input.Capabilities,
	); err != nil {
		s.internalError(w, r, "register worker bootstrap", err)
		return
	}
	token, err := s.workerTokens.Issue(cloudworker.Claims{
		AccountID: ticket.AccountID,
		SessionID: ticket.SessionID,
		WorkerID:  workerID,
		Epoch:     epoch,
		Scopes:    ticket.Scopes,
	}, 15*time.Minute)
	if err != nil {
		s.internalError(w, r, "issue worker token", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"workerId": workerID, "epoch": epoch})
	_, _ = s.events.Append(r.Context(), ticket.AccountID, ticket.SessionID, "worker.connected", payload)
	writeJSON(w, http.StatusOK, cloudworker.BootstrapResponse{
		WorkerToken:      token,
		WorkerID:         workerID,
		Epoch:            epoch,
		ExpiresIn:        900,
		SessionID:        string(ticket.SessionID),
		Launch:           launchSpec,
		AgentCredential:  agentCredential,
		LocalGitHubToken: localGitHubToken,
	})
}

func includeLocalGitHubToken(sandboxProvider, githubMode string, githubConfigured bool) bool {
	return sandboxProvider == "docker" && githubMode == "local-gh" && githubConfigured
}

type workerContextKey struct{}

func (s *Server) workerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
		switch {
		case ok && strings.EqualFold(scheme, "Worker"):
		case ok && strings.EqualFold(scheme, "Basic") && isWorkerGitProxyRequest(r):
			username, password, basicOK := r.BasicAuth()
			if !basicOK || username != cloudworker.GitProxyUsername {
				writeWorkerAuthError(w, r, "INVALID_WORKER_TOKEN", "Worker credential is invalid or expired.")
				return
			}
			token = password
		default:
			writeWorkerAuthError(w, r, "WORKER_AUTH_REQUIRED", "A valid worker credential is required.")
			return
		}
		claims, err := s.workerTokens.Verify(token)
		if err != nil {
			writeWorkerAuthError(w, r, "INVALID_WORKER_TOKEN", "Worker credential is invalid or expired.")
			return
		}
		current, err := s.store.WorkerConnectionCurrent(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			claims.WorkerID,
			claims.Epoch,
		)
		if err != nil {
			s.internalError(w, r, "validate worker epoch", err)
			return
		}
		if !current {
			writeWorkerAuthError(w, r, "STALE_WORKER_TOKEN", "Worker credential has been replaced.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), workerContextKey{}, claims)))
	})
}

func writeWorkerAuthError(w http.ResponseWriter, r *http.Request, code, message string) {
	if isWorkerGitProxyRequest(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="ao-worker-git"`)
	}
	writeError(w, r, http.StatusUnauthorized, code, message)
}

func isWorkerGitProxyRequest(r *http.Request) bool {
	const prefix = "/api/cloud/v1/git/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == r.URL.Path {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 ||
		parts[0] == "" ||
		parts[0] == "." ||
		parts[0] == ".." ||
		!strings.HasSuffix(parts[1], ".git") ||
		strings.TrimSuffix(parts[1], ".git") == "" {
		return false
	}
	_, err := cloudlocalgh.GitOperation(r.Method, strings.Join(parts[2:], "/"), r.URL.Query())
	return err == nil
}

func workerFromContext(ctx context.Context) cloudworker.Claims {
	claims, _ := ctx.Value(workerContextKey{}).(cloudworker.Claims)
	return claims
}

func (s *Server) workerOrchestrator(
	w http.ResponseWriter,
	r *http.Request,
) (cloudworker.Claims, clouddomain.Session, bool) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:orchestrate") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:orchestrate scope is required.")
		return cloudworker.Claims{}, clouddomain.Session{}, false
	}
	if strings.TrimSpace(r.Header.Get("X-AO-Session-ID")) != string(claims.SessionID) {
		writeError(w, r, http.StatusForbidden, "SESSION_ID_MISMATCH", "Worker session identity does not match its credential.")
		return cloudworker.Claims{}, clouddomain.Session{}, false
	}
	session, err := s.store.GetSession(r.Context(), claims.AccountID, claims.SessionID)
	if err != nil {
		if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
			writeError(w, r, http.StatusForbidden, "ORCHESTRATOR_REQUIRED", "Only an active orchestrator may coordinate workers.")
			return cloudworker.Claims{}, clouddomain.Session{}, false
		}
		s.internalError(w, r, "authorize worker orchestration", err)
		return cloudworker.Claims{}, clouddomain.Session{}, false
	}
	if session.Kind != "orchestrator" || session.IsTerminated {
		writeError(w, r, http.StatusForbidden, "ORCHESTRATOR_REQUIRED", "Only an active orchestrator may coordinate workers.")
		return cloudworker.Claims{}, clouddomain.Session{}, false
	}
	return claims, session, true
}

func (s *Server) workerCreateSession(w http.ResponseWriter, r *http.Request) {
	claims, parent, ok := s.workerOrchestrator(w, r)
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required.")
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
		Prompt      string `json:"prompt"`
		Harness     string `json:"harness"`
		IssueNumber int    `json:"issueNumber"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName != "" && len(input.DisplayName) > 200 {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION", "displayName must be at most 200 characters.")
		return
	}
	if input.IssueNumber < 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_ISSUE", "issueNumber must be a positive integer.")
		return
	}
	var issue *cloudlocalgh.Issue
	if input.IssueNumber > 0 {
		if s.localGitHub == nil {
			writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
			return
		}
		project, err := s.store.GetProject(r.Context(), claims.AccountID, parent.ProjectID)
		if err != nil {
			s.internalError(w, r, "load orchestrator project for issue", err)
			return
		}
		githubCtx, ok := s.githubOperationContext(
			w,
			r,
			project,
			cloudlocalgh.OperationIssueRead,
			"authorize GitHub issue lookup",
		)
		if !ok {
			return
		}
		resolved, err := s.localGitHub.GetIssue(githubCtx, project.RepositoryURL, input.IssueNumber)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ISSUE", "The GitHub issue could not be found in this project.")
			return
		}
		issue = &resolved
		if input.DisplayName == "" {
			input.DisplayName = fmt.Sprintf("issue-%d", resolved.Number)
		}
		if strings.TrimSpace(input.Prompt) == "" {
			input.Prompt = issuePrompt(resolved)
		}
	}
	if input.DisplayName == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION", "displayName is required unless issueNumber is provided.")
		return
	}
	if strings.TrimSpace(input.Prompt) == "" || len([]byte(input.Prompt)) > 64<<10 {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROMPT", "prompt must be non-empty and at most 64 KiB.")
		return
	}
	input.Harness = strings.TrimSpace(input.Harness)
	if input.Harness == "" {
		input.Harness = parent.Harness
	}
	if !cloudWorkerHarness(input.Harness) {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT", "harness must be claude-code, codex, or cursor.")
		return
	}
	credential, err := s.loadAgentCredential(r.Context(), claims.AccountID, input.Harness)
	if credential != nil {
		credential.Secret = ""
	}
	if errors.Is(err, errAgentConnectionRequired) {
		writeError(w, r, http.StatusBadRequest, "AGENT_CONNECTION_REQUIRED", "Connect "+input.Harness+" before spawning this worker.")
		return
	}
	if err != nil {
		s.internalError(w, r, "validate spawned worker agent connection", err)
		return
	}
	parentSandbox, err := s.store.GetSandbox(r.Context(), claims.AccountID, parent.ID)
	if err != nil {
		s.internalError(w, r, "load orchestrator sandbox", err)
		return
	}
	result, err := s.store.CreateSession(r.Context(), claims.AccountID, cloudpostgres.CreateSessionInput{
		IdempotencyKey:           idempotencyKey,
		ProjectID:                parent.ProjectID,
		Kind:                     "worker",
		Harness:                  input.Harness,
		DisplayName:              input.DisplayName,
		Prompt:                   input.Prompt,
		Resource:                 parentSandbox.ResourceProfile,
		Provider:                 parentSandbox.Provider,
		ProviderConnectionID:     parentSandbox.ProviderConnectionID,
		MaxActiveSandboxesPerOrg: s.maxActiveSandboxesPerOrg,
	})
	if errors.Is(err, cloudpostgres.ErrIdempotencyConflict) {
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used for another command.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrProviderConnectionNotFound) {
		writeError(w, r, http.StatusBadRequest, "PROVIDER_CONNECTION_NOT_FOUND", "The orchestrator sandbox provider connection is unavailable.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrSandboxQuotaExceeded) {
		writeError(
			w,
			r,
			http.StatusConflict,
			"SANDBOX_QUOTA_EXCEEDED",
			fmt.Sprintf(
				"This organization already has %d active sandboxes. Delete a worker machine to make room before starting another one.",
				s.maxActiveSandboxesPerOrg,
			),
		)
		return
	}
	if err != nil {
		s.internalError(w, r, "spawn orchestrated worker", err)
		return
	}
	if issue != nil {
		snapshot, err := s.store.UpsertIssueSnapshot(r.Context(), claims.AccountID, clouddomain.Issue{
			ProjectID:  parent.ProjectID,
			Provider:   "github",
			Repository: issue.Repository,
			Number:     issue.Number,
			URL:        issue.URL,
			Title:      issue.Title,
			Body:       issue.Body,
			State:      issue.State,
			ObservedAt: time.Now().UTC(),
		})
		if err != nil {
			s.internalError(w, r, "store GitHub issue snapshot", err)
			return
		}
		if err := s.store.LinkSessionIssue(r.Context(), claims.AccountID, result.Session.ID, snapshot.ID); err != nil {
			s.internalError(w, r, "link worker to GitHub issue", err)
			return
		}
	}
	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func issuePrompt(issue cloudlocalgh.Issue) string {
	prompt := fmt.Sprintf("Implement GitHub issue #%d: %s", issue.Number, issue.Title)
	if body := strings.TrimSpace(issue.Body); body != "" {
		prompt += "\n\n" + body
	}
	return prompt
}

func (s *Server) workerListSessions(w http.ResponseWriter, r *http.Request) {
	claims, parent, ok := s.workerOrchestrator(w, r)
	if !ok {
		return
	}
	sessions, err := s.store.ListSessions(r.Context(), claims.AccountID)
	if err != nil {
		s.internalError(w, r, "list orchestrated sessions", err)
		return
	}
	projectSessions := make([]clouddomain.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ProjectID == parent.ProjectID {
			projectSessions = append(projectSessions, session)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": projectSessions})
}

func (s *Server) workerClaimPullRequest(w http.ResponseWriter, r *http.Request) {
	claims, parent, ok := s.workerOrchestrator(w, r)
	if !ok {
		return
	}
	targetID, validTarget := sessionIDParam(w, r)
	if !validTarget {
		return
	}
	target, err := s.store.GetSession(r.Context(), claims.AccountID, targetID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) ||
		err == nil && (target.ProjectID != parent.ProjectID || target.Kind != "worker") {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The project worker does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "authorize pull request claim", err)
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	var input struct {
		Reference string `json:"reference"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	project, err := s.store.GetProject(r.Context(), claims.AccountID, parent.ProjectID)
	if err != nil {
		s.internalError(w, r, "load project for pull request claim", err)
		return
	}
	githubCtx, ok := s.githubOperationContext(
		w,
		r,
		project,
		cloudlocalgh.OperationPullRequestRead,
		"authorize GitHub pull request lookup",
	)
	if !ok {
		return
	}
	pull, err := s.localGitHub.GetPullRequest(githubCtx, project.RepositoryURL, input.Reference)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PULL_REQUEST", "The pull request must belong to this project.")
		return
	}
	claim, err := s.store.ClaimPullRequest(r.Context(), claims.AccountID, clouddomain.PRClaim{
		SessionID:  target.ID,
		Provider:   "github",
		Repository: pull.Repository,
		Number:     pull.Number,
		URL:        pull.URL,
	})
	if errors.Is(err, cloudpostgres.ErrPRClaimed) {
		writeError(w, r, http.StatusConflict, "PR_ALREADY_CLAIMED", "Another active worker already owns this pull request.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		writeError(w, r, http.StatusConflict, "WORKER_INACTIVE", "Only an active worker can claim a pull request.")
		return
	}
	if err != nil {
		s.internalError(w, r, "claim pull request", err)
		return
	}
	s.refreshClaimedPullRequest(r.Context(), project)
	writeJSON(w, http.StatusOK, map[string]any{"claim": claim})
}

func (s *Server) workerMergePullRequest(w http.ResponseWriter, r *http.Request) {
	claims, parent, target, ok := s.authorizedProjectWorker(w, r, "authorize pull request merge")
	if !ok {
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	scm, err := s.store.SessionSCM(r.Context(), claims.AccountID, target.ID)
	if err != nil {
		s.internalError(w, r, "load pull request for merge", err)
		return
	}
	if scm == nil || scm.PullRequest.Number <= 0 {
		writeError(w, r, http.StatusConflict, "PULL_REQUEST_REQUIRED", "The worker does not have an observed pull request.")
		return
	}
	project, err := s.store.GetProject(r.Context(), claims.AccountID, parent.ProjectID)
	if err != nil {
		s.internalError(w, r, "load project for pull request merge", err)
		return
	}
	githubCtx, ok := s.githubOperationContext(
		w,
		r,
		project,
		cloudlocalgh.OperationMerge,
		"authorize GitHub pull request merge",
	)
	if !ok {
		return
	}
	pull, err := s.localGitHub.MergePullRequest(githubCtx, project.RepositoryURL, scm.PullRequest.Number)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "PULL_REQUEST_MERGE_FAILED", err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"repository": pull.Repository,
		"number":     pull.Number,
		"url":        pull.URL,
		"source":     "orchestrator",
	})
	if _, err := s.events.Append(r.Context(), claims.AccountID, target.ID, "repository.pull_request_merged", payload); err != nil {
		s.internalError(w, r, "append pull request merge event", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pullRequest": pull})
}

func (s *Server) workerResolveReviewThread(w http.ResponseWriter, r *http.Request) {
	claims, parent, target, ok := s.authorizedProjectWorker(w, r, "authorize review thread resolution")
	if !ok {
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	threadID := strings.TrimSpace(chi.URLParam(r, "threadId"))
	if threadID == "" || len(threadID) > 300 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REVIEW_THREAD", "review thread id is invalid.")
		return
	}
	scm, err := s.store.SessionSCM(r.Context(), claims.AccountID, target.ID)
	if err != nil {
		s.internalError(w, r, "load review threads", err)
		return
	}
	if scm == nil || !sessionHasReviewThread(scm.ReviewThreads, threadID) {
		writeError(w, r, http.StatusNotFound, "REVIEW_THREAD_NOT_FOUND", "The review thread does not belong to this worker.")
		return
	}
	project, err := s.store.GetProject(r.Context(), claims.AccountID, parent.ProjectID)
	if err != nil {
		s.internalError(w, r, "load project for review thread resolution", err)
		return
	}
	githubCtx, ok := s.githubOperationContext(
		w,
		r,
		project,
		cloudlocalgh.OperationResolveReviewThread,
		"authorize GitHub review thread resolution",
	)
	if !ok {
		return
	}
	if err := s.localGitHub.ResolveReviewThread(githubCtx, threadID); err != nil {
		writeError(w, r, http.StatusBadGateway, "REVIEW_THREAD_RESOLVE_FAILED", err.Error())
		return
	}
	if err := s.store.MarkReviewThreadResolved(r.Context(), claims.AccountID, target.ID, threadID); err != nil {
		if errors.Is(err, cloudpostgres.ErrReviewThreadNotFound) {
			writeError(w, r, http.StatusNotFound, "REVIEW_THREAD_NOT_FOUND", "The review thread does not belong to this worker.")
			return
		}
		s.internalError(w, r, "mark review thread resolved", err)
		return
	}
	payload, _ := json.Marshal(map[string]string{"threadId": threadID, "source": "orchestrator"})
	if _, err := s.events.Append(r.Context(), claims.AccountID, target.ID, "repository.review_thread_resolved", payload); err != nil {
		s.internalError(w, r, "append review thread resolved event", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "threadId": threadID})
}

func sessionHasReviewThread(threads []cloudlocalgh.ReviewThreadObservation, threadID string) bool {
	for _, thread := range threads {
		if thread.ID == threadID {
			return true
		}
	}
	return false
}

func (s *Server) authorizedProjectWorker(
	w http.ResponseWriter,
	r *http.Request,
	action string,
) (cloudworker.Claims, clouddomain.Session, clouddomain.Session, bool) {
	claims, parent, ok := s.workerOrchestrator(w, r)
	if !ok {
		return cloudworker.Claims{}, clouddomain.Session{}, clouddomain.Session{}, false
	}
	targetID, validTarget := sessionIDParam(w, r)
	if !validTarget {
		return cloudworker.Claims{}, clouddomain.Session{}, clouddomain.Session{}, false
	}
	target, err := s.store.GetSession(r.Context(), claims.AccountID, targetID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) ||
		err == nil && (target.ProjectID != parent.ProjectID || target.Kind != "worker") {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The project worker does not exist.")
		return cloudworker.Claims{}, clouddomain.Session{}, clouddomain.Session{}, false
	}
	if err != nil {
		s.internalError(w, r, action, err)
		return cloudworker.Claims{}, clouddomain.Session{}, clouddomain.Session{}, false
	}
	return claims, parent, target, true
}

func (s *Server) workerKillSession(w http.ResponseWriter, r *http.Request) {
	claims, parent, ok := s.workerOrchestrator(w, r)
	if !ok {
		return
	}
	targetID, validTarget := sessionIDParam(w, r)
	if !validTarget {
		return
	}
	target, err := s.store.GetSession(r.Context(), claims.AccountID, targetID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) ||
		err == nil && (target.ProjectID != parent.ProjectID || target.Kind != "worker") {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The project worker does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "authorize worker deletion", err)
		return
	}
	sandbox, err := s.store.GetSandbox(r.Context(), claims.AccountID, target.ID)
	if err != nil {
		s.internalError(w, r, "load worker sandbox for deletion", err)
		return
	}
	if sandbox.DesiredState == "deleted" {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "state": "deleted"})
		return
	}
	if err := s.store.SetSandboxDesiredState(r.Context(), claims.AccountID, target.ID, "deleted"); err != nil {
		s.internalError(w, r, "delete worker sandbox", err)
		return
	}
	if _, err := s.store.TransitionActiveTurn(r.Context(), claims.AccountID, target.ID, "failed", "Worker machine deleted."); err != nil {
		s.internalError(w, r, "finish deleted worker turn", err)
		return
	}
	if err := s.store.UpdateSessionActivity(r.Context(), claims.AccountID, target.ID, "idle"); err != nil {
		s.internalError(w, r, "reset deleted worker activity", err)
		return
	}
	payload, _ := json.Marshal(map[string]string{"state": "deleted", "source": "orchestrator"})
	if _, err := s.events.Append(r.Context(), claims.AccountID, target.ID, "sandbox.desired_state_changed", payload); err != nil {
		s.internalError(w, r, "append deleted worker event", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "state": "deleted"})
}

func (s *Server) workerSendMessage(w http.ResponseWriter, r *http.Request) {
	claims, parent, ok := s.workerOrchestrator(w, r)
	if !ok {
		return
	}
	targetID, ok := sessionIDParam(w, r)
	if !ok {
		return
	}
	target, err := s.store.GetSession(r.Context(), claims.AccountID, targetID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) ||
		err == nil && target.ProjectID != parent.ProjectID {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The project session does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "authorize orchestrated message", err)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required.")
		return
	}
	var input struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Text) == "" || len([]byte(input.Text)) > 64<<10 {
		writeError(w, r, http.StatusBadRequest, "INVALID_MESSAGE", "text must be non-empty and at most 64 KiB.")
		return
	}
	event, err := s.events.AppendUserMessage(r.Context(), claims.AccountID, targetID, idempotencyKey, input.Text)
	if errors.Is(err, cloudpostgres.ErrIdempotencyConflict) {
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used for another command.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrActiveTurn) {
		writeError(w, r, http.StatusConflict, "TURN_ACTIVE", "The target session already has a response in progress.")
		return
	}
	if err != nil {
		s.internalError(w, r, "append orchestrated message", err)
		return
	}
	if err := s.wakeSessionForMessage(r.Context(), claims.AccountID, targetID); err != nil {
		s.internalError(w, r, "wake orchestrated session for message", err)
		return
	}
	if err := s.workerHub.Send(targetID, cloudworkerhub.Command{
		Type:     "prompt",
		Data:     base64.StdEncoding.EncodeToString([]byte(input.Text)),
		Sequence: event.Sequence,
	}); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "WORKER_BACKPRESSURE", "The message was saved but worker delivery is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"event": event})
}

func sessionIDParam(w http.ResponseWriter, r *http.Request) (clouddomain.SessionID, bool) {
	value := strings.TrimSpace(chi.URLParam(r, "sessionId"))
	if _, err := uuid.Parse(value); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "sessionId must be a UUID.")
		return "", false
	}
	return clouddomain.SessionID(value), true
}

func cloudWorkerHarness(harness string) bool {
	return harness == "claude-code" || harness == "codex" || harness == "cursor"
}

func (s *Server) workerReportBlocker(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:event") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:event scope is required.")
		return
	}
	if strings.TrimSpace(r.Header.Get("X-AO-Session-ID")) != string(claims.SessionID) {
		writeError(w, r, http.StatusForbidden, "SESSION_ID_MISMATCH", "Worker session identity does not match its credential.")
		return
	}
	session, err := s.store.GetSession(r.Context(), claims.AccountID, claims.SessionID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "authorize worker blocker", err)
		return
	}
	if session.Kind != "worker" || session.IsTerminated {
		writeError(w, r, http.StatusForbidden, "WORKER_REQUIRED", "Only an active worker can report a blocker.")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required.")
		return
	}
	var input struct {
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" || len([]byte(input.Message)) > 64<<10 {
		writeError(w, r, http.StatusBadRequest, "INVALID_BLOCKER", "message must be non-empty and at most 64 KiB.")
		return
	}
	orchestrator, ok, err := s.projectOrchestrator(r.Context(), claims.AccountID, session.ProjectID)
	if err != nil {
		s.internalError(w, r, "find project orchestrator", err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusConflict, "ORCHESTRATOR_UNAVAILABLE", "No active orchestrator is available for this project.")
		return
	}
	text := fmt.Sprintf("Worker %s (%s) is blocked:\n\n%s", session.DisplayName, session.ID, input.Message)
	event, err := s.events.AppendUserMessage(
		r.Context(),
		claims.AccountID,
		orchestrator.ID,
		"worker-blocker:"+string(session.ID)+":"+idempotencyKey,
		text,
	)
	if errors.Is(err, cloudpostgres.ErrIdempotencyConflict) {
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used for another command.")
		return
	}
	if errors.Is(err, cloudpostgres.ErrActiveTurn) {
		writeError(w, r, http.StatusConflict, "ORCHESTRATOR_BUSY", "The project orchestrator already has a response in progress.")
		return
	}
	if err != nil {
		s.internalError(w, r, "append worker blocker message", err)
		return
	}
	if err := s.wakeSessionForMessage(r.Context(), claims.AccountID, orchestrator.ID); err != nil {
		s.internalError(w, r, "wake orchestrator for blocker", err)
		return
	}
	if err := s.workerHub.Send(orchestrator.ID, cloudworkerhub.Command{
		Type:     "prompt",
		Data:     base64.StdEncoding.EncodeToString([]byte(text)),
		Sequence: event.Sequence,
	}); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "WORKER_BACKPRESSURE", "The blocker was saved but orchestrator delivery is temporarily unavailable.")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"orchestratorSessionId": orchestrator.ID,
		"messageSequence":       event.Sequence,
	})
	if _, err := s.events.Append(r.Context(), claims.AccountID, session.ID, "worker.blocker_reported", payload); err != nil {
		s.internalError(w, r, "append worker blocker event", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"event": event})
}

func (s *Server) workerClaimOwnPullRequest(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:git scope is required.")
		return
	}
	if strings.TrimSpace(r.Header.Get("X-AO-Session-ID")) != string(claims.SessionID) {
		writeError(w, r, http.StatusForbidden, "SESSION_ID_MISMATCH", "Worker session identity does not match its credential.")
		return
	}
	session, err := s.store.GetSession(r.Context(), claims.AccountID, claims.SessionID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "authorize worker pull request claim", err)
		return
	}
	if session.Kind != "worker" || session.IsTerminated {
		writeError(w, r, http.StatusForbidden, "WORKER_REQUIRED", "Only an active worker can claim its pull request.")
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	var input struct {
		Reference string `json:"reference"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	project, err := s.store.GetProject(r.Context(), claims.AccountID, session.ProjectID)
	if err != nil {
		s.internalError(w, r, "load project for worker pull request claim", err)
		return
	}
	githubCtx, ok := s.githubOperationContext(
		w,
		r,
		project,
		cloudlocalgh.OperationPullRequestRead,
		"authorize GitHub pull request lookup",
	)
	if !ok {
		return
	}
	pull, err := s.localGitHub.GetPullRequest(githubCtx, project.RepositoryURL, input.Reference)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PULL_REQUEST", "The pull request must belong to this project.")
		return
	}
	claim, err := s.store.ClaimPullRequest(r.Context(), claims.AccountID, clouddomain.PRClaim{
		SessionID:  session.ID,
		Provider:   "github",
		Repository: pull.Repository,
		Number:     pull.Number,
		URL:        pull.URL,
	})
	if errors.Is(err, cloudpostgres.ErrPRClaimed) {
		writeError(w, r, http.StatusConflict, "PR_ALREADY_CLAIMED", "Another active worker already owns this pull request.")
		return
	}
	if err != nil {
		s.internalError(w, r, "claim worker pull request", err)
		return
	}
	s.refreshClaimedPullRequest(r.Context(), project)
	payload, _ := json.Marshal(map[string]any{
		"repository": pull.Repository,
		"number":     pull.Number,
		"url":        pull.URL,
		"source":     "worker",
	})
	if _, err := s.events.Append(r.Context(), claims.AccountID, session.ID, "repository.pull_request_claimed", payload); err != nil {
		s.internalError(w, r, "append worker pull request claim event", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"claim": claim})
}

func (s *Server) refreshClaimedPullRequest(ctx context.Context, project clouddomain.Project) {
	if s.githubApp == nil ||
		s.githubApp.repositoryRefresh == nil ||
		project.GitHubRepositoryID == nil {
		return
	}
	if err := s.githubApp.repositoryRefresh(ctx, project.OrgID, *project.GitHubRepositoryID); err != nil {
		s.log.Warn("claimed pull request refresh failed",
			"org_id", project.OrgID,
			"project_id", project.ID,
			"github_repository_id", *project.GitHubRepositoryID,
			"err", err,
		)
	}
}

func (s *Server) workerGitHubToken(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:git scope is required.")
		return
	}
	if strings.TrimSpace(r.Header.Get("X-AO-Session-ID")) != string(claims.SessionID) {
		writeError(w, r, http.StatusForbidden, "SESSION_ID_MISMATCH", "Worker session identity does not match its credential.")
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	session, err := s.store.GetSession(r.Context(), claims.AccountID, claims.SessionID)
	if errors.Is(err, cloudpostgres.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "The cloud session does not exist.")
		return
	}
	if err != nil {
		s.internalError(w, r, "authorize worker GitHub token", err)
		return
	}
	if session.IsTerminated {
		writeError(w, r, http.StatusForbidden, "WORKER_REQUIRED", "Only an active worker can request a GitHub token.")
		return
	}
	project, err := s.store.GetProject(r.Context(), claims.AccountID, session.ProjectID)
	if err != nil {
		s.internalError(w, r, "load project for worker GitHub token", err)
		return
	}
	githubCtx, ok := s.githubOperationContext(
		w,
		r,
		project,
		cloudlocalgh.OperationPullRequestWrite,
		"authorize worker GitHub token",
	)
	if !ok {
		return
	}
	token, err := s.localGitHub.Token(githubCtx)
	if err != nil {
		s.internalError(w, r, "mint worker GitHub token", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token})
}

func (s *Server) projectOrchestrator(
	ctx context.Context,
	accountID clouddomain.AccountID,
	projectID clouddomain.ProjectID,
) (clouddomain.Session, bool, error) {
	sessions, err := s.store.ListSessions(ctx, accountID)
	if err != nil {
		return clouddomain.Session{}, false, err
	}
	for _, session := range sessions {
		if session.ProjectID == projectID && session.Kind == "orchestrator" && !session.IsTerminated {
			return session, true, nil
		}
	}
	return clouddomain.Session{}, false, nil
}

func (s *Server) workerHeartbeat(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:connect") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:connect scope is required.")
		return
	}
	var input struct {
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.store.MarkWorkerSeen(
		r.Context(),
		claims.AccountID,
		claims.SessionID,
		claims.WorkerID,
		input.Version,
		claims.Epoch,
		input.Capabilities,
	); err != nil {
		s.internalError(w, r, "record worker heartbeat", err)
		return
	}
	renewed, err := s.workerTokens.Issue(claims, 15*time.Minute)
	if err != nil {
		s.internalError(w, r, "renew worker token", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workerToken": renewed, "expiresIn": 900})
}

func (s *Server) workerEvent(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:event") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:event scope is required.")
		return
	}
	var input struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Type = strings.TrimSpace(input.Type)
	if input.Type == "" || len(input.Type) > 100 || !strings.HasPrefix(input.Type, "worker.") &&
		!strings.HasPrefix(input.Type, "agent.") && !strings.HasPrefix(input.Type, "terminal.") &&
		!strings.HasPrefix(input.Type, "workspace_terminal.") &&
		!strings.HasPrefix(input.Type, "repository.") && !strings.HasPrefix(input.Type, "preview.") &&
		!strings.HasPrefix(input.Type, "chat.") {
		writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_TYPE", "Worker event type is not allowed.")
		return
	}
	var activeTurn *clouddomain.Turn
	if strings.HasPrefix(input.Type, "chat.") {
		var err error
		activeTurn, err = s.store.GetActiveTurn(r.Context(), claims.AccountID, claims.SessionID)
		if err != nil {
			s.internalError(w, r, "load worker event turn", err)
			return
		}
		if activeTurn != nil {
			payload := make(map[string]any)
			if len(input.Payload) > 0 && json.Unmarshal(input.Payload, &payload) != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CHAT_EVENT", "Chat event payload must be a JSON object.")
				return
			}
			payload["turnId"] = activeTurn.ID
			input.Payload, _ = json.Marshal(payload)
		}
	}
	if input.Type == "worker.prompt_accepted" {
		var acknowledgement struct {
			Sequence int64 `json:"sequence"`
		}
		if json.Unmarshal(input.Payload, &acknowledgement) != nil ||
			acknowledgement.Sequence <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_PROMPT_ACKNOWLEDGEMENT", "Prompt acknowledgement is invalid.")
			return
		}
		if err := s.store.ClaimActiveTurn(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			acknowledgement.Sequence,
			claims.Epoch,
		); err != nil {
			s.internalError(w, r, "claim durable chat turn", err)
			return
		}
	}
	if input.Type == "agent.activity" {
		var activity struct {
			Event       string          `json:"event"`
			State       string          `json:"state"`
			HasActivity bool            `json:"hasActivity"`
			Native      json.RawMessage `json:"native"`
		}
		if err := json.Unmarshal(input.Payload, &activity); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ACTIVITY_EVENT", "Agent activity payload is invalid.")
			return
		}
		if activity.Event == "session-start" {
			nativeSessionID := activityNativeSessionID(activity.Native)
			if nativeSessionID != "" {
				if err := s.store.SetAgentSessionID(
					r.Context(),
					claims.AccountID,
					claims.SessionID,
					nativeSessionID,
				); err != nil {
					s.internalError(w, r, "store agent session ID from activity", err)
					return
				}
			}
			if err := s.workerHub.Send(
				claims.SessionID,
				cloudworkerhub.Command{Type: "agent_ready"},
			); err != nil {
				s.internalError(w, r, "signal worker agent readiness", err)
				return
			}
		}
		if activity.HasActivity {
			switch activity.State {
			case "active", "idle", "waiting_input", "blocked", "exited":
			default:
				writeError(w, r, http.StatusBadRequest, "INVALID_ACTIVITY_STATE", "Agent activity state is invalid.")
				return
			}
			if err := s.store.UpdateSessionActivity(
				r.Context(),
				claims.AccountID,
				claims.SessionID,
				activity.State,
			); err != nil {
				s.internalError(w, r, "update worker activity", err)
				return
			}
			if activityStartsTurn(activity.Event, activity.State) {
				if _, err := s.store.TransitionActiveTurn(
					r.Context(),
					claims.AccountID,
					claims.SessionID,
					"running",
					"",
				); err != nil {
					s.internalError(w, r, "start turn from worker activity", err)
					return
				}
			}
			if activityCompletesTurn(activity.Event, activity.State) {
				activeTurn, err := s.store.GetActiveTurn(
					r.Context(),
					claims.AccountID,
					claims.SessionID,
				)
				if err != nil {
					s.internalError(w, r, "load turn for worker activity", err)
					return
				}
				if activeTurn != nil && activeTurn.State == "running" {
					if _, err := s.store.TransitionActiveTurn(
						r.Context(),
						claims.AccountID,
						claims.SessionID,
						"completed",
						"",
					); err != nil {
						s.internalError(w, r, "complete turn from worker activity", err)
						return
					}
				}
			}
		}
	}
	if input.Type == "agent.started" {
		if err := s.store.UpdateSessionActivity(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			"idle",
		); err != nil {
			s.internalError(w, r, "reset started agent activity", err)
			return
		}
	}
	switch input.Type {
	case "chat.turn_started":
		if activeTurn != nil {
			if err := s.store.ClaimActiveTurn(
				r.Context(),
				claims.AccountID,
				claims.SessionID,
				activeTurn.UserMessageSequence,
				claims.Epoch,
			); err != nil {
				s.internalError(w, r, "claim started chat turn", err)
				return
			}
		}
		if _, err := s.store.TransitionActiveTurn(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			"running",
			"",
		); err != nil {
			s.internalError(w, r, "start durable chat turn", err)
			return
		}
		if err := s.store.UpdateSessionActivity(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			"active",
		); err != nil {
			s.internalError(w, r, "update chat turn activity", err)
			return
		}
	case "chat.turn_completed", "chat.turn_interrupted", "chat.turn_aborted":
		state := "completed"
		errorMessage := ""
		switch input.Type {
		case "chat.turn_completed":
			var completion struct {
				IsError bool   `json:"isError"`
				Error   string `json:"error"`
			}
			if json.Unmarshal(input.Payload, &completion) == nil && completion.IsError {
				state = "failed"
				errorMessage = completion.Error
			}
		case "chat.turn_aborted":
			state = "failed"
			errorMessage = "agent turn aborted"
		}
		if _, err := s.store.TransitionActiveTurn(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			state,
			errorMessage,
		); err != nil {
			s.internalError(w, r, "finish durable chat turn", err)
			return
		}
		if err := s.store.UpdateSessionActivity(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			"idle",
		); err != nil {
			s.internalError(w, r, "update chat turn activity", err)
			return
		}
	}
	if input.Type == "chat.session_started" {
		var session struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(input.Payload, &session); err != nil ||
			strings.TrimSpace(session.SessionID) == "" ||
			len(session.SessionID) > 200 {
			writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_SESSION", "Agent session payload is invalid.")
			return
		}
		if err := s.store.SetAgentSessionID(
			r.Context(),
			claims.AccountID,
			claims.SessionID,
			session.SessionID,
		); err != nil {
			s.internalError(w, r, "store agent session ID", err)
			return
		}
	}
	input.Payload = redactWorkerEventPayload(input.Type, input.Payload)
	event, err := s.events.Append(r.Context(), claims.AccountID, claims.SessionID, input.Type, input.Payload)
	if err != nil {
		s.internalError(w, r, "append worker event", err)
		return
	}
	if logWorkerLifecycleEvent(input.Type) {
		s.log.Info("cloud worker lifecycle event",
			"session_id", claims.SessionID,
			"worker_id", claims.WorkerID,
			"worker_epoch", claims.Epoch,
			"event_type", input.Type,
			"event_sequence", event.Sequence,
		)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"event": event})
}

func activityStartsTurn(event, state string) bool {
	return event == "user-prompt-submit" && state == "active"
}

func redactWorkerEventPayload(eventType string, payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 {
		return payload
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return payload
	}
	value = redactJSONValue(value)
	if eventType == "terminal.output" || eventType == "workspace_terminal.output" {
		if object, ok := value.(map[string]any); ok {
			if data, ok := object["data"].(string); ok {
				if encoding, _ := object["encoding"].(string); encoding == "base64" {
					if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
						object["data"] = base64.StdEncoding.EncodeToString([]byte(redactSecretText(string(decoded))))
					}
				}
			}
		}
	}
	redacted, err := json.Marshal(value)
	if err != nil {
		return payload
	}
	return redacted
}

func redactJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitivePayloadKey(key) {
				typed[key] = "[redacted]"
				continue
			}
			typed[key] = redactJSONValue(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = redactJSONValue(child)
		}
		return typed
	case string:
		return redactSecretText(typed)
	default:
		return value
	}
}

func sensitivePayloadKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "authorization")
}

func redactSecretText(value string) string {
	prefixes := []string{ // #nosec G101 -- these are environment variable names, not secret values.
		"ANTHROPIC_API_KEY=",
		"AO_LOCAL_GITHUB_TOKEN=",
		"AO_WORKER_BOOTSTRAP_TOKEN=",
		"AO_WORKER_TOKEN=",
		"CLAUDE_CODE_OAUTH_TOKEN=",
		"CURSOR_API_KEY=",
		"OPENAI_API_KEY=",
	}
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		for _, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				lines[index] = prefix + "[redacted]"
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func activityNativeSessionID(native json.RawMessage) string {
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(native, &payload) != nil {
		return ""
	}
	sessionID := strings.TrimSpace(payload.SessionID)
	if len(sessionID) > 200 {
		return ""
	}
	return sessionID
}

func activityCompletesTurn(event, state string) bool {
	return state == "idle" && (event == "stop" || event == "after-agent")
}

func logWorkerLifecycleEvent(eventType string) bool {
	switch eventType {
	case "worker.prompt_accepted",
		"worker.command_stream_disconnected",
		"repository.ready",
		"agent.started",
		"agent.exited",
		"chat.session_started",
		"chat.turn_started",
		"chat.turn_completed",
		"chat.turn_interrupted",
		"chat.turn_aborted":
		return true
	default:
		return false
	}
}

func (s *Server) workerConnect(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:terminal") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:terminal scope is required.")
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AFTER", "after must be a non-negative integer.")
		return
	}
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer func() { _ = socket.Close(websocket.StatusNormalClosure, "worker connection closed") }()
	commands, unregister := s.workerHub.Register(
		claims.SessionID,
		claims.WorkerID,
		claims.Epoch,
	)
	defer unregister()
	s.log.Info("cloud worker command stream connected",
		"session_id", claims.SessionID,
		"worker_id", claims.WorkerID,
		"worker_epoch", claims.Epoch,
		"replay_after", after,
	)
	accepted, err := s.store.LatestPromptAcceptedSequence(
		r.Context(),
		claims.AccountID,
		claims.SessionID,
	)
	if err != nil {
		return
	}
	interrupted, err := s.store.LatestEventSequenceByType(
		r.Context(),
		claims.AccountID,
		claims.SessionID,
		"chat.interrupt_requested",
	)
	if err != nil {
		return
	}
	replayedAfter := max(after, accepted, interrupted)
	retrySequence, err := s.store.PrepareActiveTurnForWorker(
		r.Context(),
		claims.AccountID,
		claims.SessionID,
		claims.Epoch,
	)
	if err != nil {
		return
	}
	if retrySequence > 0 && retrySequence <= replayedAfter {
		replayedAfter = retrySequence - 1
	}
	if err := s.writePromptReplay(r.Context(), socket, claims, &replayedAfter); err != nil {
		return
	}
	activeTurn, err := s.store.GetActiveTurn(r.Context(), claims.AccountID, claims.SessionID)
	if err != nil {
		return
	}
	if activeTurn != nil && activeTurn.State == "cancel_requested" {
		encoded, _ := json.Marshal(cloudworkerhub.Command{
			Type:     "interrupt",
			Sequence: interrupted,
		})
		if err := s.writeWorkerSocket(r.Context(), socket, encoded); err != nil {
			return
		}
	}
	ticker := time.NewTicker(s.workerReplayWait)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case command, ok := <-commands:
			if !ok {
				_ = socket.Close(websocket.StatusPolicyViolation, "worker replaced")
				return
			}
			if command.Type == "prompt" {
				if err := s.writePromptReplay(r.Context(), socket, claims, &replayedAfter); err != nil {
					return
				}
				continue
			}
			encoded, _ := json.Marshal(command)
			if err := s.writeWorkerSocket(r.Context(), socket, encoded); err != nil {
				s.workerHub.DisconnectAndRequeue(
					claims.SessionID,
					claims.WorkerID,
					claims.Epoch,
					command,
				)
				return
			}
		case <-ticker.C:
			if err := s.writePromptReplay(r.Context(), socket, claims, &replayedAfter); err != nil {
				return
			}
			encoded, _ := json.Marshal(cloudworkerhub.Command{Type: "keepalive"})
			if err := s.writeWorkerSocket(r.Context(), socket, encoded); err != nil {
				return
			}
		}
	}
}

func (s *Server) writePromptReplay(
	ctx context.Context,
	socket *websocket.Conn,
	claims cloudworker.Claims,
	replayedAfter *int64,
) error {
	for {
		replayed, err := s.events.ReplayActivePrompts(
			ctx,
			claims.AccountID,
			claims.SessionID,
			*replayedAfter,
			500,
		)
		if err != nil {
			return err
		}
		for _, event := range replayed {
			var payload struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil ||
				payload.Text == "" {
				if event.Sequence > *replayedAfter {
					*replayedAfter = event.Sequence
				}
				continue
			}
			command := cloudworkerhub.Command{
				Type:     "prompt",
				Data:     base64.StdEncoding.EncodeToString([]byte(payload.Text)),
				Sequence: event.Sequence,
			}
			encoded, marshalErr := json.Marshal(command)
			if marshalErr != nil {
				return marshalErr
			}
			if err := s.writeWorkerSocket(ctx, socket, encoded); err != nil {
				return err
			}
			if event.Sequence > *replayedAfter {
				*replayedAfter = event.Sequence
			}
		}
		if len(replayed) < 500 {
			return nil
		}
	}
}

func (s *Server) writeWorkerSocket(
	ctx context.Context,
	socket *websocket.Conn,
	payload []byte,
) error {
	writeCtx, cancel := context.WithTimeout(ctx, s.workerWriteWait)
	defer cancel()
	return socket.Write(writeCtx, websocket.MessageText, payload)
}

func (s *Server) issueTerminalTicket(w http.ResponseWriter, r *http.Request) {
	account, session, ok := s.authorizedSession(w, r, "authorize terminal ticket")
	if !ok {
		return
	}
	var input struct {
		Kind string `json:"kind"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	kind := terminalKind(input.Kind)
	if kind == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_TERMINAL_KIND", "terminal kind must be agent or workspace.")
		return
	}
	scopes := []string{"terminal:read"}
	canOperate := false
	if shared, ok := sharedProjectAccessFromContext(r.Context()); ok {
		canOperate = shared.canOperateSession(session.ProjectID, session.ID)
	} else if org, ok := orgFromContext(r.Context()); !ok || orgRoleAtLeast(org.Membership.Role, "member") {
		canOperate = true
	}
	if canOperate {
		scopes = append(scopes, "terminal:operate")
	}
	ticket, err := s.store.IssueAccessTicket(
		r.Context(),
		account.ID,
		session.ID,
		terminalTicketPurpose(kind),
		scopes,
		60*time.Second,
	)
	if err != nil {
		s.internalError(w, r, "issue terminal ticket", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ticket":    ticket,
		"expiresIn": 60,
		"scopes":    scopes,
	})
}

type terminalClientCommand struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

type terminalServerMessage struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
	Message  string `json:"message,omitempty"`
}

func terminalKind(value string) string {
	switch value {
	case "", "agent":
		return "agent"
	case "workspace":
		return "workspace"
	default:
		return ""
	}
}

func terminalTicketPurpose(kind string) string {
	if kind == "workspace" {
		return "workspace_terminal"
	}
	return "terminal"
}

func terminalOutputEvent(kind string) string {
	if kind == "workspace" {
		return "workspace_terminal.output"
	}
	return "terminal.output"
}

func ticketHasScope(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if scope == expected {
			return true
		}
	}
	return false
}

func (s *Server) terminalSocket(w http.ResponseWriter, r *http.Request) {
	kind := terminalKind(r.URL.Query().Get("kind"))
	if kind == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_TERMINAL_KIND", "terminal kind must be agent or workspace.")
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AFTER", "after must be a non-negative integer.")
		return
	}
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{s.webOriginHost},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer func() { _ = socket.Close(websocket.StatusNormalClosure, "terminal closed") }()

	ticket, err := s.store.ConsumeAccessTicket(
		r.Context(),
		r.URL.Query().Get("ticket"),
		terminalTicketPurpose(kind),
	)
	if errors.Is(err, cloudpostgres.ErrInvalidTicket) {
		_ = socket.Close(websocket.StatusPolicyViolation, "terminal ticket is invalid or expired")
		return
	}
	if err != nil {
		_ = socket.Close(websocket.StatusInternalError, "terminal ticket could not be consumed")
		return
	}
	canOperateTerminal := ticketHasScope(ticket.Scopes, "terminal:operate")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	live := make(chan clouddomain.Event, 1024)
	unsubscribe := s.events.Subscribe(ticket.AccountID, ticket.SessionID, func(event clouddomain.Event) {
		if event.Type != terminalOutputEvent(kind) && event.Type != "worker.connected" {
			return
		}
		select {
		case live <- event:
		default:
			cancel()
		}
	})
	defer unsubscribe()

	resetSequence, err := s.store.LatestEventSequenceByType(
		ctx,
		ticket.AccountID,
		ticket.SessionID,
		"worker.connected",
	)
	if err != nil {
		return
	}
	if resetSequence > after {
		if err := writeTerminalMessage(ctx, socket, terminalServerMessage{
			Type:     "reset",
			Sequence: resetSequence,
		}); err != nil {
			return
		}
		after = resetSequence
	}
	sent := after
	for {
		replayed, err := s.events.Replay(ctx, ticket.AccountID, ticket.SessionID, sent, 500)
		if err != nil {
			return
		}
		for _, event := range replayed {
			if event.Type == terminalOutputEvent(kind) {
				if err := writeTerminalEvent(ctx, socket, event, &sent); err != nil {
					return
				}
			} else if event.Sequence > sent {
				sent = event.Sequence
			}
		}
		if len(replayed) < 500 {
			break
		}
	}

	clientCommands := make(chan terminalClientCommand, 64)
	readErrors := make(chan error, 1)
	go func() {
		for {
			_, data, err := socket.Read(ctx)
			if err != nil {
				readErrors <- err
				return
			}
			var command terminalClientCommand
			if err := json.Unmarshal(data, &command); err != nil {
				readErrors <- err
				return
			}
			select {
			case clientCommands <- command:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readErrors:
			return
		case event := <-live:
			if event.Type == "worker.connected" {
				if event.Sequence <= sent {
					continue
				}
				if err := writeTerminalMessage(ctx, socket, terminalServerMessage{
					Type:     "reset",
					Sequence: event.Sequence,
				}); err != nil {
					return
				}
				sent = event.Sequence
			} else {
				if err := writeTerminalEvent(ctx, socket, event, &sent); err != nil {
					return
				}
			}
		case command := <-clientCommands:
			if !canOperateTerminal {
				_ = writeTerminalMessage(ctx, socket, terminalServerMessage{
					Type:    "error",
					Message: "Terminal is read-only for viewers.",
				})
				continue
			}
			workerCommand, err := validateTerminalCommand(command)
			if err != nil {
				_ = writeTerminalMessage(ctx, socket, terminalServerMessage{
					Type:    "error",
					Message: err.Error(),
				})
				continue
			}
			if kind == "workspace" {
				workerCommand.Type = "workspace_terminal_" + workerCommand.Type
			}
			if err := s.workerHub.Send(ticket.SessionID, workerCommand); err != nil {
				message := "Terminal command could not be queued."
				if errors.Is(err, cloudworkerhub.ErrWorkerBackpressure) {
					message = "Terminal input queue is full. Wait for the worker to catch up."
				}
				_ = writeTerminalMessage(ctx, socket, terminalServerMessage{
					Type:    "error",
					Message: message,
				})
			}
		}
	}
}

func validateTerminalCommand(command terminalClientCommand) (cloudworkerhub.Command, error) {
	switch command.Type {
	case "input":
		decoded, err := base64.StdEncoding.DecodeString(command.Data)
		if err != nil || len(decoded) == 0 || len(decoded) > 64<<10 {
			return cloudworkerhub.Command{}, errors.New("terminal input is invalid")
		}
		return cloudworkerhub.Command{Type: "input", Data: command.Data}, nil
	case "resize":
		if command.Rows == 0 || command.Cols == 0 || command.Rows > 1000 || command.Cols > 1000 {
			return cloudworkerhub.Command{}, errors.New("terminal size is invalid")
		}
		return cloudworkerhub.Command{Type: "resize", Rows: command.Rows, Cols: command.Cols}, nil
	default:
		return cloudworkerhub.Command{}, errors.New("terminal command type is invalid")
	}
}

func writeTerminalEvent(
	ctx context.Context,
	socket *websocket.Conn,
	event clouddomain.Event,
	sent *int64,
) error {
	if event.Sequence <= *sent {
		return nil
	}
	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if err := writeTerminalMessage(ctx, socket, terminalServerMessage{
		Type:     "output",
		Data:     payload.Data,
		Sequence: event.Sequence,
	}); err != nil {
		return err
	}
	*sent = event.Sequence
	return nil
}

func writeTerminalMessage(
	ctx context.Context,
	socket *websocket.Conn,
	message terminalServerMessage,
) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return socket.Write(ctx, websocket.MessageText, encoded)
}

func (s *Server) listProviderConnections(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	connections, err := s.store.ListProviderConnections(r.Context(), account.ID)
	if err != nil {
		s.internalError(w, r, "list provider connections", err)
		return
	}
	settings, err := s.store.OrgProviderSettings(r.Context(), clouddomain.OrgID(account.ID))
	if err != nil {
		s.internalError(w, r, "load provider settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providerConnections":  connections,
		"agentCredentialsMode": settings.AgentCredentialsMode,
	})
}

const maxAgentCredentialBytes = 64 << 10

type agentConnectionConfig struct {
	CredentialType string `json:"credentialType"`
}

func (s *Server) putAgentConnection(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if _, ok := agentCredentialTypes[agent]; !ok {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT", "The selected coding agent is not supported.")
		return
	}
	var input struct {
		CredentialType string `json:"credentialType"`
		Secret         string `json:"secret"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.CredentialType = strings.TrimSpace(input.CredentialType)
	secret := normalizeAgentCredentialSecret(input.Secret)
	input.Secret = ""
	defer clearBytes(secret)
	if !validAgentCredentialType(agent, input.CredentialType) {
		writeError(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_TYPE", "credentialType is not supported for this coding agent.")
		return
	}
	if len(secret) == 0 || len(secret) > maxAgentCredentialBytes {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_CREDENTIAL", "A non-empty coding-agent credential of at most 64 KiB is required.")
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	validationErr := s.agentCredentials.Validate(validateCtx, agent, input.CredentialType, secret)
	cancel()
	if errors.Is(validationErr, errInvalidAgentCredential) {
		writeError(
			w,
			r,
			http.StatusBadRequest,
			"AGENT_CONNECTION_INVALID",
			"The coding-agent provider rejected this credential. Generate a fresh credential and try again.",
		)
		return
	}
	if validationErr != nil {
		writeError(
			w,
			r,
			http.StatusBadGateway,
			"AGENT_CONNECTION_VALIDATION_UNAVAILABLE",
			"AO Cloud could not validate this credential with the provider.",
		)
		return
	}
	associatedData := string(account.ID) + ":" + agent + ":default"
	encrypted, nonce, err := s.secretCipher.Encrypt(secret, associatedData)
	if err != nil {
		s.internalError(w, r, "encrypt agent connection", err)
		return
	}
	config, _ := json.Marshal(agentConnectionConfig{CredentialType: input.CredentialType})
	connection, err := s.store.UpsertProviderConnection(
		r.Context(),
		account.ID,
		agent,
		"default",
		encrypted,
		nonce,
		config,
	)
	if err != nil {
		s.internalError(w, r, "save agent connection", err)
		return
	}
	if org, ok := orgFromContext(r.Context()); ok {
		if org.Organization.Kind == "personal" {
			if err := s.syncDefaultAgentConnectionToOwnedOrgs(
				r.Context(),
				principal.UserID,
				agent,
				secret,
				config,
			); err != nil {
				s.internalError(w, r, "sync personal provider defaults", err)
				return
			}
		} else if org.Membership.Role == "owner" || org.Membership.Role == "admin" {
			if _, err := s.store.SetOrgProviderSettings(r.Context(), org.Organization.ID, "custom"); err != nil {
				s.internalError(w, r, "set custom provider mode", err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerConnection": connection})
}

func (s *Server) updateProviderSettings(w http.ResponseWriter, r *http.Request) {
	org, _ := orgFromContext(r.Context())
	principal, _ := cloudauth.PrincipalFromContext(r.Context())
	var input struct {
		AgentCredentialsMode string `json:"agentCredentialsMode"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	mode := strings.TrimSpace(input.AgentCredentialsMode)
	if org.Organization.Kind == "personal" && mode != "custom" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROVIDER_SETTINGS", "Personal workspaces always use their own provider credentials.")
		return
	}
	settings, err := s.store.SetOrgProviderSettings(r.Context(), org.Organization.ID, mode)
	if errors.Is(err, cloudpostgres.ErrInvalidProviderSettings) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROVIDER_SETTINGS", "agentCredentialsMode must be custom or personal_default.")
		return
	}
	if err != nil {
		s.internalError(w, r, "save provider settings", err)
		return
	}
	if mode == "personal_default" {
		if err := s.syncPersonalDefaultAgentConnections(
			r.Context(),
			principal.UserID,
			org.Organization.ID,
		); err != nil {
			s.internalError(w, r, "sync provider defaults", err)
			return
		}
	}
	connections, err := s.store.ListProviderConnections(r.Context(), clouddomain.AccountID(org.Organization.ID))
	if err != nil {
		s.internalError(w, r, "list provider connections", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providerSettings":     settings,
		"providerConnections":  connections,
		"agentCredentialsMode": settings.AgentCredentialsMode,
	})
}

func (s *Server) deleteAgentConnection(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if _, ok := agentCredentialTypes[agent]; !ok {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT", "The selected coding agent is not supported.")
		return
	}
	if err := s.store.DeleteProviderConnection(r.Context(), account.ID, agent, "default"); err != nil {
		s.internalError(w, r, "delete agent connection", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var agentCredentialTypes = map[string]map[string]struct{}{
	"claude-code": {
		"oauth_token": {},
		"api_key":     {},
	},
	"codex": {
		"api_key":      {},
		"access_token": {},
	},
	"cursor": {
		"api_key": {},
	},
}

func validAgentCredentialType(agent, credentialType string) bool {
	credentialTypes, ok := agentCredentialTypes[agent]
	if !ok {
		return false
	}
	_, ok = credentialTypes[credentialType]
	return ok
}

func (s *Server) hasAgentConnection(
	ctx context.Context,
	accountID clouddomain.AccountID,
) (bool, error) {
	connections, err := s.store.ListProviderConnections(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, connection := range connections {
		if connection.Label != "default" {
			continue
		}
		if _, ok := agentCredentialTypes[connection.Provider]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) loadAgentCredential(
	ctx context.Context,
	accountID clouddomain.AccountID,
	harness string,
) (*cloudworker.AgentCredential, error) {
	if _, ok := agentCredentialTypes[harness]; !ok {
		return nil, nil
	}
	encrypted, nonce, configJSON, err := s.store.ProviderConnectionSecretByProvider(
		ctx,
		accountID,
		harness,
		"default",
	)
	if errors.Is(err, cloudpostgres.ErrProviderConnectionNotFound) {
		return nil, errAgentConnectionRequired
	}
	if err != nil {
		return nil, err
	}
	var config agentConnectionConfig
	if err := json.Unmarshal(configJSON, &config); err != nil ||
		!validAgentCredentialType(harness, config.CredentialType) {
		return nil, errAgentConnectionRequired
	}
	plaintext, err := s.secretCipher.Decrypt(
		encrypted,
		nonce,
		string(accountID)+":"+harness+":default",
	)
	if err != nil || len(plaintext) == 0 {
		clearBytes(plaintext)
		return nil, errAgentConnectionRequired
	}
	credential := &cloudworker.AgentCredential{
		Provider:       harness,
		CredentialType: config.CredentialType,
		Secret:         string(plaintext),
	}
	clearBytes(plaintext)
	return credential, nil
}

func (s *Server) syncPersonalDefaultAgentConnections(
	ctx context.Context,
	userID string,
	targetOrgID clouddomain.OrgID,
) error {
	personalOrgID, err := s.store.PersonalOrgIDForUser(ctx, userID)
	if errors.Is(err, cloudpostgres.ErrOrganizationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if personalOrgID == targetOrgID {
		return nil
	}
	connections, err := s.store.ListProviderConnections(ctx, clouddomain.AccountID(personalOrgID))
	if err != nil {
		return err
	}
	for _, connection := range connections {
		if connection.Label != "default" || connection.ValidationState != "valid" {
			continue
		}
		if _, ok := agentCredentialTypes[connection.Provider]; !ok {
			continue
		}
		encrypted, nonce, config, err := s.store.ProviderConnectionSecretByProvider(
			ctx,
			clouddomain.AccountID(personalOrgID),
			connection.Provider,
			"default",
		)
		if errors.Is(err, cloudpostgres.ErrProviderConnectionNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		plaintext, err := s.secretCipher.Decrypt(
			encrypted,
			nonce,
			string(personalOrgID)+":"+connection.Provider+":default",
		)
		if err != nil {
			clearBytes(plaintext)
			return err
		}
		if err := s.upsertAgentSecretForOrg(ctx, targetOrgID, connection.Provider, plaintext, config); err != nil {
			clearBytes(plaintext)
			return err
		}
		clearBytes(plaintext)
	}
	return nil
}

func (s *Server) syncDefaultAgentConnectionToOwnedOrgs(
	ctx context.Context,
	userID string,
	agent string,
	secret []byte,
	config json.RawMessage,
) error {
	orgIDs, err := s.store.ListDefaultProviderOrgsForUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, orgID := range orgIDs {
		if err := s.upsertAgentSecretForOrg(ctx, orgID, agent, secret, config); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) upsertAgentSecretForOrg(
	ctx context.Context,
	orgID clouddomain.OrgID,
	agent string,
	secret []byte,
	config json.RawMessage,
) error {
	associatedData := string(orgID) + ":" + agent + ":default"
	encrypted, nonce, err := s.secretCipher.Encrypt(secret, associatedData)
	if err != nil {
		return err
	}
	_, err = s.store.UpsertProviderConnection(
		ctx,
		clouddomain.AccountID(orgID),
		agent,
		"default",
		encrypted,
		nonce,
		config,
	)
	return err
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

var errAgentConnectionRequired = errors.New("coding-agent connection required")
var errExternalSignupDisabled = errors.New("external account signup is disabled")

func (s *Server) listRepositories(w http.ResponseWriter, r *http.Request) {
	if s.githubApp != nil && s.githubApp.mode == "github-app" {
		org, ok := orgFromContext(r.Context())
		if !ok || s.githubStore == nil {
			writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
			return
		}
		repositories, err := s.githubStore.ListActiveGitHubRepositories(r.Context(), org.Organization.ID)
		if err != nil {
			s.internalError(w, r, "list GitHub App repositories", err)
			return
		}
		response := make([]map[string]any, 0, len(repositories))
		for _, repository := range repositories {
			response = append(response, map[string]any{
				"id":            repository.Repository.ID,
				"fullName":      repository.Repository.FullName,
				"url":           repository.Repository.HTMLURL,
				"defaultBranch": repository.Repository.DefaultBranch,
				"private":       repository.Repository.Private,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"repositories": response})
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	repositories, err := s.localGitHub.ListRepositories(r.Context())
	if err != nil {
		s.internalError(w, r, "list local GitHub repositories", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}

func (s *Server) githubOperationContext(
	w http.ResponseWriter,
	r *http.Request,
	project clouddomain.Project,
	operation cloudlocalgh.CredentialOperation,
	action string,
) (context.Context, bool) {
	if s.githubMode() != "github-app" {
		return r.Context(), true
	}
	if project.GitHubRepositoryID == nil || s.githubStore == nil {
		writeError(w, r, http.StatusForbidden, "REPOSITORY_NOT_AUTHORIZED", "This project is not linked to an authorized GitHub repository.")
		return nil, false
	}
	_, err := s.githubStore.FindActiveGitHubRepositoryGrant(
		r.Context(),
		project.OrgID,
		*project.GitHubRepositoryID,
	)
	if errors.Is(err, cloudpostgres.ErrGitHubRepositoryGrantNotFound) {
		writeError(w, r, http.StatusForbidden, "REPOSITORY_NOT_AUTHORIZED", "This project's GitHub repository grant is no longer active.")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, action, err)
		return nil, false
	}
	scoped, err := cloudlocalgh.ContextWithCredentialScope(
		r.Context(),
		project.OrgID,
		*project.GitHubRepositoryID,
		operation,
	)
	if err != nil {
		s.internalError(w, r, action, err)
		return nil, false
	}
	return scoped, true
}

func (s *Server) gitProxy(w http.ResponseWriter, r *http.Request) {
	claims := workerFromContext(r.Context())
	if !cloudworker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "worker:git scope is required.")
		return
	}
	if s.localGitHub == nil {
		writeError(w, r, http.StatusNotImplemented, "GITHUB_CONNECTION_REQUIRED", "GitHub is not configured for this deployment.")
		return
	}
	suffix := chi.URLParam(r, "*")
	operation, err := cloudlocalgh.GitOperation(r.Method, suffix, r.URL.Query())
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_GIT_REQUEST", "Only Git upload-pack and receive-pack requests are supported.")
		return
	}
	launch, err := s.store.WorkerLaunchSpec(r.Context(), claims.AccountID, claims.SessionID)
	if err != nil {
		s.internalError(w, r, "authorize Git proxy", err)
		return
	}
	expectedOwner, expectedRepository, ok := cloudlocalgh.ParseRepositoryURL(launch.RepositoryURL)
	if !ok ||
		!strings.EqualFold(expectedOwner, chi.URLParam(r, "owner")) ||
		!strings.EqualFold(expectedRepository, chi.URLParam(r, "repository")) {
		writeError(w, r, http.StatusForbidden, "REPOSITORY_NOT_AUTHORIZED", "Worker is not authorized for this repository.")
		return
	}
	project, err := s.store.GetProject(r.Context(), claims.AccountID, launch.Session.ProjectID)
	if err != nil {
		s.internalError(w, r, "load project for Git proxy", err)
		return
	}
	githubCtx, ok := s.githubOperationContext(
		w,
		r,
		project,
		operation,
		"authorize GitHub repository proxy",
	)
	if !ok {
		return
	}
	if err := s.localGitHub.ProxyRepository(
		githubCtx,
		w,
		r,
		expectedOwner,
		expectedRepository,
		suffix,
	); err != nil {
		s.internalError(w, r, "proxy GitHub repository", err)
	}
}

func (s *Server) putDaytonaConnection(w http.ResponseWriter, r *http.Request) {
	account, _ := accountFromContext(r.Context())
	var input struct {
		Label  string `json:"label"`
		APIKey string `json:"apiKey"`
		APIURL string `json:"apiUrl"`
		Target string `json:"target"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Label = strings.TrimSpace(input.Label)
	if input.Label == "" {
		input.Label = "personal"
	}
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.APIKey == "" {
		writeError(w, r, http.StatusBadRequest, "DAYTONA_KEY_REQUIRED", "A Daytona API key is required.")
		return
	}
	input.APIURL = strings.TrimRight(strings.TrimSpace(input.APIURL), "/")
	if input.APIURL == "" {
		input.APIURL = s.daytonaAPIURL
	}
	if !validDaytonaAPIURL(input.APIURL, s.daytonaAPIURL) {
		writeError(w, r, http.StatusBadRequest, "INVALID_DAYTONA_URL", "Daytona API URL must use an approved HTTPS Daytona origin.")
		return
	}
	if input.Target == "" {
		input.Target = s.daytonaTarget
	}
	if input.Target != "us" && input.Target != "eu" {
		writeError(w, r, http.StatusBadRequest, "INVALID_DAYTONA_TARGET", "Daytona target must be us or eu.")
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := daytona.New(input.APIURL, input.APIKey, input.Target, nil).Validate(validateCtx); err != nil {
		writeError(w, r, http.StatusBadRequest, "DAYTONA_CONNECTION_INVALID", "Daytona rejected this connection.")
		return
	}
	associatedData := string(account.ID) + ":daytona:" + input.Label
	encrypted, nonce, err := s.secretCipher.Encrypt([]byte(input.APIKey), associatedData)
	if err != nil {
		s.internalError(w, r, "encrypt Daytona connection", err)
		return
	}
	config, _ := json.Marshal(map[string]string{"apiUrl": input.APIURL, "target": input.Target})
	connection, err := s.store.UpsertProviderConnection(
		r.Context(),
		account.ID,
		"daytona",
		input.Label,
		encrypted,
		nonce,
		config,
	)
	if err != nil {
		s.internalError(w, r, "save Daytona connection", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerConnection": connection})
}

func validDaytonaAPIURL(candidate, configured string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	configuredParsed, err := url.Parse(configured)
	if err == nil &&
		configuredParsed.Scheme == "https" &&
		strings.EqualFold(parsed.Host, configuredParsed.Host) {
		return true
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "app.daytona.io" || strings.HasSuffix(host, ".daytona.io")
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != s.webOrigin {
			writeError(w, r, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Request origin is not allowed.")
			return
		}
		if origin == s.webOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, Last-Event-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validGitHubRepositoryURL(value string) bool {
	return strings.HasPrefix(value, "https://github.com/") &&
		len(strings.TrimPrefix(value, "https://github.com/")) > 2
}

func decodeJSON(w http.ResponseWriter, r *http.Request, output any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body must be valid JSON.")
		return false
	}
	return true
}

func parseAfter(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 {
		return 0, errors.New("invalid after")
	}
	return after, nil
}

func parseLimit(r *http.Request, maximum int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return maximum, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > maximum {
		return 0, errors.New("invalid limit")
	}
	return limit, nil
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event clouddomain.Event, sent *int64) error {
	if event.Sequence <= *sent {
		return nil
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	eventName := strings.NewReplacer("\r", "_", "\n", "_").Replace(event.Type)
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, eventName, encoded); err != nil {
		return err
	}
	*sent = event.Sequence
	flusher.Flush()
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error":     http.StatusText(status),
		"code":      code,
		"message":   message,
		"requestId": middleware.GetReqID(r.Context()),
	})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	s.log.Error("AO Cloud request failed",
		"operation", operation,
		"request_id", middleware.GetReqID(r.Context()),
		"err", err,
	)
	writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.")
}
