package controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimportsvc"
)

const defaultImportableWindowDays = sessionimportsvc.DiscoveryWindowDays

// SessionImportService discovers on-disk agent conversations and imports one as
// a resumable AO session. It is provider-agnostic; the provider is carried on
// each record. Import returns the AO session and whether it already existed (an
// idempotent re-import returns the existing session with alreadyImported=true).
type SessionImportService interface {
	Discover(ctx context.Context, opts sessionimport.DiscoverOptions, projectID domain.ProjectID) ([]sessionimport.ImportableSession, error)
	Import(ctx context.Context, provider domain.AgentHarness, nativeSessionID string, projectID domain.ProjectID) (session domain.Session, alreadyImported bool, err error)
}

// ImportableSessionView is one on-disk conversation the user could import.
type ImportableSessionView struct {
	TokenCount      int64  `json:"tokenCount" description:"Observed lower bound of cumulative provider usage including cached input; scanning may stop once the import threshold is met."`
	Provider        string `json:"provider" description:"Agent harness that wrote the transcript, e.g. claude-code or codex."`
	NativeSessionID string `json:"nativeSessionId" description:"The provider's own session id, used to bind and resume the imported session."`
	Title           string `json:"title" description:"Human label: the provider's title, else the first prompt, else the file name."`
	CWD             string `json:"cwd" description:"Working directory the conversation ran in, read from the transcript."`
	Branch          string `json:"branch,omitempty" description:"Git branch recorded in the transcript, when present."`
	LastActivity    string `json:"lastActivity" description:"RFC3339 timestamp of the most recent activity."`
	MessageCount    int    `json:"messageCount" description:"Retained for compatibility; discovery leaves this zero to avoid counting every message."`
	SizeBytes       int64  `json:"sizeBytes" description:"Transcript size on disk in bytes."`
	AlreadyImported bool   `json:"alreadyImported" description:"True when an AO session is already bound to this native session id."`
}

// ListImportableSessionsQuery is the discovery query.
type ListImportableSessionsQuery struct {
	Provider  string `query:"provider,omitempty" description:"Restrict to one provider, e.g. claude-code or codex."`
	ProjectID string `query:"projectId" required:"true" description:"Required registered project. Only conversations active within 15 days with at least 15000 provider tokens are listed."`
}

// ListImportableSessionsResponse is the discovery result.
type ListImportableSessionsResponse struct {
	Sessions []ImportableSessionView `json:"sessions"`
}

// ImportSessionRequest asks to import one discovered conversation.
type ImportSessionRequest struct {
	ProjectID       string `json:"projectId" description:"Registered project that owns this conversation."`
	Provider        string `json:"provider" description:"Agent harness of the conversation, e.g. claude-code or codex."`
	NativeSessionID string `json:"nativeSessionId" description:"The provider's own session id from the discovery list."`
}

// ImportSessionResponse is the imported AO session.
type ImportSessionResponse struct {
	Session         SessionView `json:"session"`
	AlreadyImported bool        `json:"alreadyImported" description:"True when the session already existed and was returned as-is."`
}

func (c *SessionsController) listImportable(w http.ResponseWriter, r *http.Request) {
	if c.Import == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/importable")
		return
	}

	projectID := domain.ProjectID(strings.TrimSpace(r.URL.Query().Get("projectId")))
	if projectID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", "projectId is required", nil)
		return
	}
	opts := sessionimport.DiscoverOptions{Since: time.Now().AddDate(0, 0, -defaultImportableWindowDays), MinTokens: sessionimportsvc.MinimumTokens}
	sessions, err := c.Import.Discover(r.Context(), opts, projectID)
	if err != nil {
		if errors.Is(err, sessionimportsvc.ErrImportProjectUnresolved) {
			envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "IMPORT_PROJECT_UNRESOLVED", "choose a registered project before importing its conversations", nil)
		} else {
			envelope.WriteError(w, r, err)
		}
		return
	}

	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	views := make([]ImportableSessionView, 0, len(sessions))
	for _, s := range sessions {
		if provider != "" && string(s.Provider) != provider {
			continue
		}
		views = append(views, importableView(s))
	}

	envelope.WriteJSON(w, http.StatusOK, ListImportableSessionsResponse{Sessions: views})
}

func (c *SessionsController) importSession(w http.ResponseWriter, r *http.Request) {
	if c.Import == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/import")
		return
	}

	var req ImportSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}

	provider := domain.AgentHarness(strings.TrimSpace(req.Provider))
	nativeID := strings.TrimSpace(req.NativeSessionID)
	projectID := domain.ProjectID(strings.TrimSpace(req.ProjectID))
	if provider == "" || nativeID == "" || projectID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BODY", "projectId, provider and nativeSessionId are required", nil)
		return
	}

	session, alreadyImported, err := c.Import.Import(r.Context(), provider, nativeID, projectID)
	if err != nil {
		switch {
		case errors.Is(err, sessionimportsvc.ErrImportSessionNotFound):
			envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "IMPORT_SESSION_NOT_FOUND", "no eligible conversation in this project: imports require activity within 15 days and at least 15000 recorded provider tokens", nil)
		case errors.Is(err, sessionimportsvc.ErrImportProjectUnresolved):
			envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "IMPORT_PROJECT_UNRESOLVED", "choose a registered project before importing its conversations", nil)
		default:
			envelope.WriteError(w, r, err)
		}
		return
	}

	status := http.StatusCreated
	if alreadyImported {
		status = http.StatusOK
	}
	envelope.WriteJSON(w, status, ImportSessionResponse{
		Session:         sessionView(session),
		AlreadyImported: alreadyImported,
	})
}

func importableView(s sessionimport.ImportableSession) ImportableSessionView {
	last := ""
	if !s.LastActivity.IsZero() {
		last = s.LastActivity.UTC().Format(time.RFC3339)
	}
	return ImportableSessionView{
		TokenCount:      s.TokenCount,
		Provider:        string(s.Provider),
		NativeSessionID: s.NativeSessionID,
		Title:           s.Title,
		CWD:             s.CWD,
		Branch:          s.Branch,
		LastActivity:    last,
		MessageCount:    s.MessageCount,
		SizeBytes:       s.SizeBytes,
		AlreadyImported: s.AlreadyImported,
	}
}
