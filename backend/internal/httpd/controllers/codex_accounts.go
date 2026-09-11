package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

// CodexAccountService is the HTTP controller's account-management boundary.
type CodexAccountService interface {
	CachedCodexAccounts(context.Context) (agentsvc.CodexAccounts, error)
	EnsureCodexAccounts(context.Context, []string, bool) (agentsvc.CodexAccounts, error)
	ConsumeCodexAccountResetCredit(context.Context, string, string) (agentsvc.CodexAccounts, error)
	SubscribeCodexAccounts(context.Context) (<-chan agentsvc.CodexAccounts, error)
	OpenCodexAccountLoginTerminal(context.Context) (agentsvc.CodexAccountLoginTerminalStart, error)
	OpenCodexAccountReauthenticationTerminal(context.Context, string) (agentsvc.CodexAccountLoginTerminalStart, error)
	LogoutCodexAccount(context.Context, string) (agentsvc.CodexAccounts, error)
	DeleteCodexAccount(context.Context, string) (agentsvc.CodexAccounts, error)
	VerifyCodexAccountLogin(context.Context, string) (domain.CodexAccountLoginOperation, error)
	CancelCodexAccountLogin(context.Context, string) (domain.CodexAccountLoginOperation, error)
	StartCodexAccountSwitch(context.Context, ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error)
	RecoverCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, error)
}

// CodexAccountsController exposes cached accounts, login, switching, and events.
type CodexAccountsController struct{ Svc CodexAccountService }

// Register adds request-timeout-bound Codex account routes.
func (c *CodexAccountsController) Register(r chi.Router) {
	r.Get("/agents/codex/accounts", c.list)
	r.Post("/agents/codex/accounts/ensure", c.ensure)
	r.Post("/agents/codex/accounts/{accountId}/reset-credit/consume", c.consumeResetCredit)
	r.Post("/agents/codex/accounts/{accountId}/login-terminal", c.openReauthenticationTerminal)
	r.Post("/agents/codex/accounts/{accountId}/logout", c.logoutAccount)
	r.Delete("/agents/codex/accounts/{accountId}", c.deleteAccount)
	r.Post("/agents/codex/accounts/login-terminal", c.openLoginTerminal)
	r.Post("/agents/codex/accounts/login-operations/{operationId}/verify", c.verifyLogin)
	r.Post("/agents/codex/accounts/login-operations/{operationId}/cancel", c.cancelLogin)
	r.Post("/agents/codex/account-switches", c.startSwitch)
	r.Post("/agents/codex/account-switches/{switchId}/recover", c.recoverSwitch)
}

func (c *CodexAccountsController) consumeResetCredit(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume")
		return
	}
	var request ConsumeCodexAccountResetCreditRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "IDEMPOTENCY_KEY_REQUIRED", "Idempotency key is required", nil)
		return
	}
	result, err := c.Svc.ConsumeCodexAccountResetCredit(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")), request.IdempotencyKey)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexAccountsResponse(result))
}

func (c *CodexAccountsController) startSwitch(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/account-switches")
		return
	}
	var request StartCodexAccountSwitchRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "IDEMPOTENCY_KEY_REQUIRED", "Idempotency key is required", nil)
		return
	}
	result, err := c.Svc.StartCodexAccountSwitch(r.Context(), ports.CodexAccountSwitchConfig{TargetAccountID: request.TargetAccountID, ExpectedAccountRevision: request.ExpectedAccountRevision, IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		writeCodexAccountSwitchError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, newCodexSwitchResponse(result))
}

func (c *CodexAccountsController) recoverSwitch(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/account-switches/{switchId}/recover")
		return
	}
	result, err := c.Svc.RecoverCodexAccountSwitch(r.Context(), strings.TrimSpace(chi.URLParam(r, "switchId")))
	if err != nil {
		writeCodexAccountSwitchError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexSwitchResponse(result))
}

func writeCodexAccountSwitchError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ports.ErrCodexAccountSwitchNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "CODEX_ACCOUNT_SWITCH_NOT_FOUND", "Codex account switch not found", nil)
	case errors.Is(err, ports.ErrCodexAccountAlreadyActive):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_ACCOUNT_ALREADY_ACTIVE", "This Codex account is already active", nil)
	case errors.Is(err, ports.ErrCodexActiveAccountUnavailable):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_ACCOUNT_AUTH_UNVERIFIED", "The device's current Codex account is not available for switching", nil)
	case errors.Is(err, ports.ErrCodexAccountRevisionConflict):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_ACCOUNT_REVISION_CONFLICT", "The active Codex account changed", nil)
	case errors.Is(err, ports.ErrCodexAccountSwitchInProgress):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "A Codex account switch is already in progress", nil)
	case errors.Is(err, ports.ErrCodexAccountSwitchIdempotencyConflict):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_ACCOUNT_SWITCH_IDEMPOTENCY_CONFLICT", "The idempotency key belongs to another Codex account switch", nil)
	case errors.Is(err, ports.ErrCodexGlobalCredentialStoreUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials", nil)
	case errors.Is(err, ports.ErrCodexGlobalAccountChanged):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed during switching", nil)
	case errors.Is(err, ports.ErrCodexRunningSessionNotResumable):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_RUNNING_SESSION_NOT_RESUMABLE", "A running AO Codex session cannot be resumed exactly", nil)
	case errors.Is(err, ports.ErrCodexAccountLoginInProgress):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Codex account login before switching accounts", nil)
	default:
		envelope.WriteError(w, r, err)
	}
}

// RegisterStreams adds the long-lived Codex account event route.
func (c *CodexAccountsController) RegisterStreams(r chi.Router) {
	r.Get("/agents/codex/accounts/events", c.events)
}

func (c *CodexAccountsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/codex/accounts")
		return
	}
	result, err := c.Svc.CachedCodexAccounts(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexAccountsResponse(result))
}

func (c *CodexAccountsController) ensure(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/ensure")
		return
	}
	var request EnsureCodexAccountsRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.EnsureCodexAccounts(r.Context(), request.AccountIDs, request.IncludeUsage)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexAccountsResponse(result))
}

func (c *CodexAccountsController) openLoginTerminal(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/login-terminal")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.OpenCodexAccountLoginTerminal(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	writeCodexLoginTerminal(w, result)
}

func (c *CodexAccountsController) openReauthenticationTerminal(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/{accountId}/login-terminal")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.OpenCodexAccountReauthenticationTerminal(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	writeCodexLoginTerminal(w, result)
}

func writeCodexLoginTerminal(w http.ResponseWriter, result agentsvc.CodexAccountLoginTerminalStart) {
	envelope.WriteJSON(w, http.StatusAccepted, OpenCodexAccountLoginTerminalResponse{
		Operation: newCodexLoginResponse(result.Operation),
		ShellTerminal: CodexAccountLoginTerminalResponse{
			HandleID:  result.ShellTerminal.HandleID,
			Title:     result.ShellTerminal.Title,
			CreatedAt: result.ShellTerminal.CreatedAt,
		},
	})
}

func (c *CodexAccountsController) logoutAccount(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/{accountId}/logout")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.LogoutCodexAccount(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexAccountsResponse(result))
}

func (c *CodexAccountsController) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/agents/codex/accounts/{accountId}")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.DeleteCodexAccount(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexAccountsResponse(result))
}

func (c *CodexAccountsController) verifyLogin(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/login-operations/{operationId}/verify")
		return
	}
	result, err := c.Svc.VerifyCodexAccountLogin(r.Context(), strings.TrimSpace(chi.URLParam(r, "operationId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexLoginResponse(result))
}

func (c *CodexAccountsController) cancelLogin(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/codex/accounts/login-operations/{operationId}/cancel")
		return
	}
	result, err := c.Svc.CancelCodexAccountLogin(r.Context(), strings.TrimSpace(chi.URLParam(r, "operationId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newCodexLoginResponse(result))
}

func (c *CodexAccountsController) events(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/codex/accounts/events")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED", "Streaming is not supported by this server", nil)
		return
	}
	events, err := c.Svc.SubscribeCodexAccounts(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(newCodexAccountsResponse(event))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: codex_account\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
