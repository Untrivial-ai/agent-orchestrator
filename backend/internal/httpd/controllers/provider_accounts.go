package controllers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProviderAccountsController serves Account Manager to the renderer.
type ProviderAccountsController struct {
	Svc ports.ProviderAccountAdmin
}

// Register mounts the account routes.
func (c *ProviderAccountsController) Register(r chi.Router) {
	r.Get("/provider-accounts", c.list)
	r.Post("/provider-accounts/{accountId}/actions", c.act)
	r.Post("/provider-accounts/login", c.startLogin)
	r.Get("/provider-accounts/login/{loginId}", c.loginStatus)
	r.Delete("/provider-accounts/login/{loginId}", c.cancelLogin)
	r.Get("/provider-accounts/sessions/{sessionId}", c.sessionAccount)
}

func (c *ProviderAccountsController) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.writeAccounts(w, r, q.Get("includeUsage") != "false", q.Get("refresh") == "true", "")
}

func (c *ProviderAccountsController) writeAccounts(w http.ResponseWriter, r *http.Request, usage, refresh bool, outcome string) {
	accounts, err := c.Svc.Accounts(r.Context(), usage, refresh)
	writeAnswer(w, r, ProviderAccountsResponse{Accounts: accounts, ResetOutcome: outcome}, err)
}

// writeAnswer writes an answer, or the error that took its place.
func writeAnswer(w http.ResponseWriter, r *http.Request, answer any, err error) {
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, answer)
}

func (c *ProviderAccountsController) act(w http.ResponseWriter, r *http.Request) {
	var in ports.ProviderAccountAction
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteError(w, r, apierr.Invalid("INVALID_JSON", "Invalid JSON body", nil))
		return
	}
	outcome, err := c.Svc.Act(r.Context(), chi.URLParam(r, "accountId"), in)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	c.writeAccounts(w, r, false, false, outcome)
}

func (c *ProviderAccountsController) startLogin(w http.ResponseWriter, r *http.Request) {
	var in ports.ProviderLoginRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteError(w, r, apierr.Invalid("INVALID_JSON", "Invalid JSON body", nil))
		return
	}
	login, err := c.Svc.StartLogin(r.Context(), in)
	writeLogin(w, r, login, err)
}

func (c *ProviderAccountsController) loginStatus(w http.ResponseWriter, r *http.Request) {
	login, err := c.Svc.LoginStatus(r.Context(), chi.URLParam(r, "loginId"))
	writeLogin(w, r, login, err)
}

func writeLogin(w http.ResponseWriter, r *http.Request, login ports.ProviderLogin, err error) {
	writeAnswer(w, r, ProviderLoginResponse{ID: login.ID, Provider: login.Provider, Mode: login.Mode, URL: login.URL, Code: login.Code, Status: login.Status, AccountID: login.AccountID}, err)
}

func (c *ProviderAccountsController) cancelLogin(w http.ResponseWriter, r *http.Request) {
	if err := c.Svc.CancelLogin(r.Context(), chi.URLParam(r, "loginId")); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ProviderAccountsController) sessionAccount(w http.ResponseWriter, r *http.Request) {
	route, managed, err := c.Svc.SessionAccount(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	writeAnswer(w, r, SessionProviderAccountResponse{Managed: managed, AccountID: route.AccountID}, err)
}
