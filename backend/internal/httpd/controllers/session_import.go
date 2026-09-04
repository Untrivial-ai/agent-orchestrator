package controllers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimportsvc"
)

// defaultImportableWindowDays bounds discovery to recent conversations unless
// the caller widens it. It matches the reference onboarding behavior.
const defaultImportableWindowDays = 60

// maxImportablePerProvider caps how many conversations each provider returns so
// the list stays scannable and the scan stays cheap.
const maxImportablePerProvider = 100

// SessionImportService discovers on-disk agent conversations and imports one as
// a resumable AO session. It is provider-agnostic; the provider is carried on
// each record. Import returns the AO session and whether it already existed (an
// idempotent re-import returns the existing session with alreadyImported=true).
type SessionImportService interface {
	Discover(ctx context.Context, opts sessionimport.DiscoverOptions, projectID domain.ProjectID) ([]sessionimport.ImportableSession, error)
	Import(ctx context.Context, provider domain.AgentHarness, nativeSessionID string) (session domain.Session, alreadyImported bool, err error)
}

// ImportableSessionView is one on-disk conversation the user could import.
type ImportableSessionView struct {
	Provider        string `json:"provider" description:"Agent harness that wrote the transcript, e.g. claude-code or codex."`
	NativeSessionID string `json:"nativeSessionId" description:"The provider's own session id, used to bind and resume the imported session."`
	Title           string `json:"title" description:"Human label: the provider's title, else the first prompt, else the file name."`
	CWD             string `json:"cwd" description:"Working directory the conversation ran in, read from the transcript."`
	Branch          string `json:"branch,omitempty" description:"Git branch recorded in the transcript, when present."`
	LastActivity    string `json:"lastActivity" description:"RFC3339 timestamp of the most recent activity."`
	MessageCount    int    `json:"messageCount" description:"Best-effort visible message count; 0 when the transcript is too large to count cheaply."`
	SizeBytes       int64  `json:"sizeBytes" description:"Transcript size on disk in bytes."`
	AlreadyImported bool   `json:"alreadyImported" description:"True when an AO session is already bound to this native session id."`
	Meaning         string `json:"meaning" description:"Import verdict from the transcript's content: meaningful, or ambiguous when the local heuristic could not decide. Trivial conversations are withheld and never listed."`
}

// ListImportableSessionsQuery is the discovery query.
type ListImportableSessionsQuery struct {
	Days      int    `query:"days,omitempty" description:"Only include conversations active within the last N days (default 60, 0 disables the age filter)."`
	Provider  string `query:"provider,omitempty" description:"Restrict to one provider, e.g. claude-code or codex."`
	ProjectID string `query:"projectId,omitempty" description:"Restrict to conversations that ran inside this project. Empty lists every conversation on the machine."`
}

// ListImportableSessionsResponse is the discovery result.
type ListImportableSessionsResponse struct {
	Sessions []ImportableSessionView `json:"sessions"`
}

// ImportSessionRequest asks to import one discovered conversation.
type ImportSessionRequest struct {
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

	opts := sessionimport.DiscoverOptions{MaxPerProvider: maxImportablePerProvider}

	days := defaultImportableWindowDays
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", "days must be a non-negative integer", nil)
			return
		}
		days = parsed
	}
	if days > 0 {
		opts.Since = time.Now().AddDate(0, 0, -days)
	}

	projectID := domain.ProjectID(strings.TrimSpace(r.URL.Query().Get("projectId")))
	sessions, err := c.Import.Discover(r.Context(), opts, projectID)
	if err != nil {
		envelope.WriteError(w, r, err)
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
	if provider == "" || nativeID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BODY", "provider and nativeSessionId are required", nil)
		return
	}

	session, alreadyImported, err := c.Import.Import(r.Context(), provider, nativeID)
	if err != nil {
		switch {
		case errors.Is(err, sessionimportsvc.ErrImportSessionNotFound):
			envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "IMPORT_SESSION_NOT_FOUND", "no importable session with that id was found", nil)
		case errors.Is(err, sessionimportsvc.ErrImportProjectUnresolved):
			envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "IMPORT_PROJECT_UNRESOLVED", "this conversation ran in a folder that is not a git repository, so AO cannot create a project for it. Initialize the folder as a repository, then import again", nil)
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
		Provider:        string(s.Provider),
		NativeSessionID: s.NativeSessionID,
		Title:           s.Title,
		CWD:             s.CWD,
		Branch:          s.Branch,
		LastActivity:    last,
		MessageCount:    s.MessageCount,
		SizeBytes:       s.SizeBytes,
		AlreadyImported: s.AlreadyImported,
		Meaning:         string(s.Meaning),
	}
}
