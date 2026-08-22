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

// Options supplies the dependencies for a control-plane HTTP server.
type Options struct {
	Store           AccountStore
	Google          IdentityVerifier
	AccessTokens    *auth.AccessTokenManager
	RefreshTokenTTL time.Duration
	Logger          *slog.Logger
}

// Server owns the Cloud foundation HTTP handler.
type Server struct {
	store           AccountStore
	google          IdentityVerifier
	accessTokens    *auth.AccessTokenManager
	refreshTokenTTL time.Duration
	logger          *slog.Logger
	handler         http.Handler
}

// New constructs the control-plane auth and account routes.
func New(options Options) (*Server, error) {
	if options.Store == nil || options.Google == nil || options.AccessTokens == nil {
		return nil, errors.New("cloud HTTP store, Google verifier, and access-token manager are required")
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
		accessTokens:    options.AccessTokens,
		refreshTokenTTL: options.RefreshTokenTTL,
		logger:          options.Logger,
	}
	server.handler = server.routes()
	return server, nil
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
