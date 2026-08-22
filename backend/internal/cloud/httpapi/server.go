// Package httpapi serves the hosted control plane's public HTTP foundation.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
)

const maxRequestBodyBytes = 1 << 20

type principalContextKey struct{}
type workspaceContextKey struct{}

// IdentityVerifier validates one external identity token.
type IdentityVerifier interface {
	Verify(context.Context, string) (domain.Principal, error)
}

// AccountStore is the persistence boundary required by the auth foundation.
type AccountStore interface {
	Ping(context.Context) error
	UpsertGoogleUser(context.Context, domain.Principal) (domain.Principal, error)
	PrincipalByID(context.Context, string) (domain.Principal, error)
	CreateRefreshSession(context.Context, string, []byte, time.Time) error
	RotateRefreshSession(context.Context, []byte, []byte) (domain.Principal, error)
	RevokeRefreshSession(context.Context, []byte) error
	ListMemberships(context.Context, domain.Principal) ([]domain.Membership, error)
}

// WorkspaceStore persists the durable intent and observed provisioning state.
type WorkspaceStore interface {
	CreateWorkspace(context.Context, domain.Principal, string, string, string) (domain.Workspace, error)
	Workspace(context.Context, domain.Principal, string, string) (domain.Workspace, error)
	UpdateWorkspaceProvisioning(context.Context, domain.Workspace, string, string, string) error
}

// WorkspaceProvisioner owns the remote sandbox lifecycle used by this POC.
type WorkspaceProvisioner interface {
	Provision(context.Context, domain.Workspace, domain.WorkspaceBootstrap) (string, error)
	PreviewURL(context.Context, string) (string, error)
}

// SessionRuntimeStore persists the one-session/one-sandbox mapping.
type SessionRuntimeStore interface {
	RuntimeWorkspace(context.Context, domain.Principal, string, string) (domain.Workspace, error)
	CreateSessionRuntime(context.Context, domain.Principal, domain.Workspace, string) (domain.SessionRuntime, error)
	SessionRuntime(context.Context, domain.Principal, string, string, string) (domain.SessionRuntime, error)
	UpdateSessionRuntime(context.Context, domain.Principal, domain.SessionRuntime, string, string, string) error
}

// SessionRuntimeProvisioner owns isolated agent compute and terminal access.
type SessionRuntimeProvisioner interface {
	ProvisionSessionRuntime(context.Context, domain.Workspace, domain.RuntimeLaunch) (string, error)
	DeleteSessionRuntime(context.Context, string) error
	SessionRuntimeAlive(context.Context, string) (bool, error)
	SessionRuntimeOutput(context.Context, string, int) (string, error)
	SessionRuntimeInput(context.Context, string, string, bool) error
	SessionRuntimeInterrupt(context.Context, string) error
}

// Options supplies the dependencies for a control-plane HTTP server.
type Options struct {
	Store           AccountStore
	Google          IdentityVerifier
	AllowedEmails   []string
	AccessTokens    *auth.AccessTokenManager
	RefreshTokenTTL time.Duration
	Logger          *slog.Logger
	Workspaces      WorkspaceProvisioner
	WorkspaceStore  WorkspaceStore
	SessionRuntimes SessionRuntimeProvisioner
	SessionStore    SessionRuntimeStore
	PublicURL       string
}

// Server owns the Cloud foundation HTTP handler.
type Server struct {
	store           AccountStore
	google          IdentityVerifier
	allowedEmails   map[string]struct{}
	accessTokens    *auth.AccessTokenManager
	refreshTokenTTL time.Duration
	logger          *slog.Logger
	workspaces      WorkspaceProvisioner
	workspaceStore  WorkspaceStore
	sessionRuntimes SessionRuntimeProvisioner
	sessionStore    SessionRuntimeStore
	publicURL       string
	handler         http.Handler
}

// New constructs the control-plane auth and account routes.
func New(options Options) (*Server, error) {
	if options.Store == nil || options.Google == nil || options.AccessTokens == nil {
		return nil, errors.New("cloud HTTP store, Google verifier, and access-token manager are required")
	}
	if len(emailSet(options.AllowedEmails)) == 0 {
		return nil, errors.New("at least one cloud account email must be allowed")
	}
	if options.Workspaces != nil && options.WorkspaceStore == nil {
		return nil, errors.New("cloud workspace store is required when a workspace provisioner is configured")
	}
	if options.SessionRuntimes != nil && (options.SessionStore == nil || strings.TrimSpace(options.PublicURL) == "") {
		return nil, errors.New("cloud session store and public URL are required when session runtimes are configured")
	}
	if options.RefreshTokenTTL <= 0 {
		return nil, errors.New("cloud refresh-token lifetime must be positive")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	server := &Server{
		store:           options.Store,
		google:          options.Google,
		allowedEmails:   emailSet(options.AllowedEmails),
		accessTokens:    options.AccessTokens,
		refreshTokenTTL: options.RefreshTokenTTL,
		logger:          options.Logger,
		workspaces:      options.Workspaces,
		workspaceStore:  options.WorkspaceStore,
		sessionRuntimes: options.SessionRuntimes,
		sessionStore:    options.SessionStore,
		publicURL:       strings.TrimRight(strings.TrimSpace(options.PublicURL), "/"),
	}
	server.handler = server.routes()
	return server, nil
}

func emailSet(emails []string) map[string]struct{} {
	result := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		if normalized := strings.ToLower(strings.TrimSpace(email)); normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func (s *Server) emailAllowed(email string) bool {
	_, allowed := s.allowedEmails[strings.ToLower(strings.TrimSpace(email))]
	return allowed
}

// Handler returns the complete HTTP handler for the Cloud foundation.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) routes() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(s.recoverer)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "NOT_FOUND", "route not found")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "METHOD_NOT_ALLOWED", "method not allowed")
	})
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", s.ready)
	router.Post("/api/cloud/v1/auth/google", s.exchangeGoogle)
	router.Post("/api/cloud/v1/auth/refresh", s.refresh)
	router.Post("/api/cloud/v1/auth/logout", s.logout)
	router.With(s.requirePrincipal).Get("/api/cloud/v1/me", s.me)
	router.With(s.requirePrincipal).Post("/api/cloud/v1/orgs/{orgID}/workspaces", s.createWorkspace)
	router.With(s.requirePrincipal).Get("/api/cloud/v1/orgs/{orgID}/workspaces/{workspaceID}", s.getWorkspace)
	router.Route("/api/cloud/internal/v1/workspaces/{workspaceID}/runtimes", func(runtime chi.Router) {
		runtime.Use(s.requireWorkspace)
		runtime.Post("/", s.createSessionRuntime)
		runtime.Get("/{sessionID}", s.getSessionRuntime)
		runtime.Delete("/{sessionID}", s.deleteSessionRuntime)
		runtime.Post("/{sessionID}/input", s.inputSessionRuntime)
		runtime.Post("/{sessionID}/interrupt", s.interruptSessionRuntime)
	})
	return router
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("Cloud HTTP panic", "request_id", requestID(r), "panic", recovered)
				writeError(w, r, http.StatusInternalServerError, "internal_error", "INTERNAL_ERROR", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		s.internalError(w, r, "database readiness", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) requirePrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "AUTH_REQUIRED", "valid AO access token required")
			return
		}
		claims, err := s.accessTokens.Verify(parts[1])
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_ACCESS_TOKEN", "valid AO access token required")
			return
		}
		principal, err := s.store.PrincipalByID(r.Context(), claims.Subject)
		if err != nil {
			if !errors.Is(err, postgres.ErrNotFound) {
				s.logger.Error("resolve Cloud principal", "request_id", requestID(r), "error", err)
			}
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_ACCESS_TOKEN", "valid AO access token required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func (s *Server) requireWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "WORKSPACE_AUTH_REQUIRED", "valid workspace capability required")
			return
		}
		claims, err := s.accessTokens.VerifyWorkspace(parts[1])
		if err != nil || claims.WorkspaceID != chi.URLParam(r, "workspaceID") {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_WORKSPACE_TOKEN", "valid workspace capability required")
			return
		}
		principal := domain.Principal{UserID: claims.Subject}
		workspace, err := s.sessionStore.RuntimeWorkspace(r.Context(), principal, claims.OrgID, claims.WorkspaceID)
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_WORKSPACE_TOKEN", "valid workspace capability required")
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		ctx = context.WithValue(ctx, workspaceContextKey{}, workspace)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	s.logger.Error("Cloud request failed", "request_id", requestID(r), "operation", operation, "error", err)
	writeError(w, r, http.StatusInternalServerError, "internal_error", "INTERNAL_ERROR", "internal server error")
}

func principalFromContext(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(domain.Principal)
	return principal, ok
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "request body must be valid JSON")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "request body must contain one JSON value")
		return false
	}
	return true
}

type errorEnvelope struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, kind, code, message string) {
	writeJSON(w, status, errorEnvelope{
		Error:     kind,
		Code:      code,
		Message:   message,
		RequestID: requestID(r),
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func requestID(r *http.Request) string {
	requestID := middleware.GetReqID(r.Context())
	if requestID == "" {
		return "unknown"
	}
	return requestID
}

func requireValue(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}
