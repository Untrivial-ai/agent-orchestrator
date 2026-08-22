package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
)

const maxRuntimeLaunchBytes = 48 << 20

type runtimeFileInput struct {
	Path       string `json:"path"`
	DataBase64 string `json:"dataBase64"`
}

type createRuntimeRequest struct {
	SessionID               string             `json:"sessionId"`
	Branch                  string             `json:"branch,omitempty"`
	SourceWorkspace         string             `json:"sourceWorkspace"`
	Argv                    []string           `json:"argv"`
	Env                     map[string]string  `json:"env,omitempty"`
	WorkspaceArchiveBase64  string             `json:"workspaceArchiveBase64,omitempty"`
	ClaudeCredentialsBase64 string             `json:"claudeCredentialsBase64"`
	Files                   []runtimeFileInput `json:"files,omitempty"`
}

type runtimeStatusResponse struct {
	Runtime domain.SessionRuntime `json:"runtime"`
	Alive   bool                  `json:"alive,omitempty"`
	Output  string                `json:"output,omitempty"`
}

func (s *Server) createSessionRuntime(w http.ResponseWriter, r *http.Request) {
	if s.sessionRuntimes == nil || s.sessionStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "CLOUD_RUNTIME_UNAVAILABLE", "cloud session runtime is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRuntimeLaunchBytes)
	var input createRuntimeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_RUNTIME_LAUNCH", "runtime launch is invalid")
		return
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.SourceWorkspace = strings.TrimSpace(input.SourceWorkspace)
	if input.SessionID == "" || input.SourceWorkspace == "" || len(input.Argv) == 0 {
		writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_RUNTIME_LAUNCH", "sessionId, sourceWorkspace, and argv are required")
		return
	}
	archive, err := base64.StdEncoding.DecodeString(input.WorkspaceArchiveBase64)
	input.WorkspaceArchiveBase64 = ""
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_WORKSPACE_ARCHIVE", "workspace archive is invalid")
		return
	}
	credentials, err := base64.StdEncoding.DecodeString(input.ClaudeCredentialsBase64)
	input.ClaudeCredentialsBase64 = ""
	if err != nil || len(credentials) == 0 || len(credentials) > 256<<10 || !validJSONObject(credentials) {
		clear(archive)
		clear(credentials)
		writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_CLAUDE_CREDENTIALS", "valid Claude credentials are required")
		return
	}
	files := make([]domain.RuntimeFile, 0, len(input.Files))
	for _, file := range input.Files {
		data, decodeErr := base64.StdEncoding.DecodeString(file.DataBase64)
		if decodeErr != nil || strings.TrimSpace(file.Path) == "" || len(data) > 1<<20 {
			clear(archive)
			clear(credentials)
			clearRuntimeFiles(files)
			writeError(w, r, http.StatusBadRequest, "bad_request", "INVALID_RUNTIME_FILE", "runtime file is invalid")
			return
		}
		files = append(files, domain.RuntimeFile{SourcePath: file.Path, Data: data})
	}
	principal, _ := principalFromContext(r.Context())
	workspace, _ := r.Context().Value(workspaceContextKey{}).(domain.Workspace)
	runtime, err := s.sessionStore.CreateSessionRuntime(r.Context(), principal, workspace, input.SessionID)
	if err != nil {
		clear(archive)
		clear(credentials)
		clearRuntimeFiles(files)
		s.internalError(w, r, "create cloud session runtime", err)
		return
	}
	launch := domain.RuntimeLaunch{
		SessionID: input.SessionID, Branch: strings.TrimSpace(input.Branch), SourceWorkspace: input.SourceWorkspace,
		Argv: append([]string(nil), input.Argv...), Env: input.Env, WorkspaceArchive: archive,
		ClaudeCredentials: credentials, Files: files,
	}
	// Provisioning deliberately outlives the HTTP request; the bounded worker
	// context below owns its cancellation and cleanup.
	go s.provisionSessionRuntime(context.WithoutCancel(r.Context()), principal, workspace, runtime, launch) //nolint:gosec // bounded provisioning intentionally outlives this request
	writeJSON(w, http.StatusAccepted, runtimeStatusResponse{Runtime: runtime})
}

func (s *Server) provisionSessionRuntime(parent context.Context, principal domain.Principal, workspace domain.Workspace, runtime domain.SessionRuntime, launch domain.RuntimeLaunch) {
	defer func() {
		clear(launch.WorkspaceArchive)
		clear(launch.ClaudeCredentials)
		clearRuntimeFiles(launch.Files)
	}()
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	sandboxID, err := s.sessionRuntimes.ProvisionSessionRuntime(ctx, workspace, launch)
	if err != nil {
		_ = s.sessionStore.UpdateSessionRuntime(ctx, principal, runtime, domain.SessionRuntimeFailed, sandboxID, "session sandbox provisioning failed")
		s.logger.Error("provision isolated cloud session", "workspace_id", workspace.ID, "session_id", runtime.SessionID, "error", err)
		return
	}
	if err = s.sessionStore.UpdateSessionRuntime(ctx, principal, runtime, domain.SessionRuntimeRunning, sandboxID, ""); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = s.sessionRuntimes.DeleteSessionRuntime(cleanupCtx, sandboxID)
		s.logger.Error("mark isolated cloud session running", "session_id", runtime.SessionID, "error", err)
	}
}

func (s *Server) getSessionRuntime(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFromContext(r.Context())
	workspace, _ := r.Context().Value(workspaceContextKey{}).(domain.Workspace)
	runtime, err := s.sessionStore.SessionRuntime(r.Context(), principal, workspace.OrgID, workspace.ID, chi.URLParam(r, "sessionID"))
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "RUNTIME_NOT_FOUND", "session runtime not found")
			return
		}
		s.internalError(w, r, "get cloud session runtime", err)
		return
	}
	response := runtimeStatusResponse{Runtime: runtime}
	if runtime.State == domain.SessionRuntimeRunning {
		response.Alive, err = s.sessionRuntimes.SessionRuntimeAlive(r.Context(), runtime.SandboxID)
		lines := 200
		if parsed, parseErr := strconv.Atoi(r.URL.Query().Get("lines")); parseErr == nil && parsed > 0 && parsed <= 5000 {
			lines = parsed
		}
		if err == nil {
			response.Output, err = s.sessionRuntimes.SessionRuntimeOutput(r.Context(), runtime.SandboxID, lines)
		}
		if err != nil {
			s.internalError(w, r, "inspect cloud session runtime", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deleteSessionRuntime(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFromContext(r.Context())
	workspace, _ := r.Context().Value(workspaceContextKey{}).(domain.Workspace)
	runtime, err := s.sessionStore.SessionRuntime(r.Context(), principal, workspace.OrgID, workspace.ID, chi.URLParam(r, "sessionID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "RUNTIME_NOT_FOUND", "session runtime not found")
		return
	}
	if runtime.SandboxID != "" {
		if err = s.sessionRuntimes.DeleteSessionRuntime(r.Context(), runtime.SandboxID); err != nil {
			s.internalError(w, r, "delete cloud session runtime", err)
			return
		}
	}
	if err = s.sessionStore.UpdateSessionRuntime(r.Context(), principal, runtime, domain.SessionRuntimeStopped, runtime.SandboxID, ""); err != nil {
		s.internalError(w, r, "mark cloud session stopped", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type runtimeInputRequest struct {
	Input string `json:"input"`
	Enter bool   `json:"enter"`
}

func (s *Server) inputSessionRuntime(w http.ResponseWriter, r *http.Request) {
	var input runtimeInputRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	runtime, ok := s.authorizedRuntime(w, r)
	if !ok {
		return
	}
	if err := s.sessionRuntimes.SessionRuntimeInput(r.Context(), runtime.SandboxID, input.Input, input.Enter); err != nil {
		s.internalError(w, r, "input cloud session runtime", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) interruptSessionRuntime(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.authorizedRuntime(w, r)
	if !ok {
		return
	}
	if err := s.sessionRuntimes.SessionRuntimeInterrupt(r.Context(), runtime.SandboxID); err != nil {
		s.internalError(w, r, "interrupt cloud session runtime", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizedRuntime(w http.ResponseWriter, r *http.Request) (domain.SessionRuntime, bool) {
	principal, _ := principalFromContext(r.Context())
	workspace, _ := r.Context().Value(workspaceContextKey{}).(domain.Workspace)
	runtime, err := s.sessionStore.SessionRuntime(r.Context(), principal, workspace.OrgID, workspace.ID, chi.URLParam(r, "sessionID"))
	if err != nil || runtime.State != domain.SessionRuntimeRunning || runtime.SandboxID == "" {
		writeError(w, r, http.StatusNotFound, "not_found", "RUNTIME_NOT_FOUND", "running session runtime not found")
		return domain.SessionRuntime{}, false
	}
	return runtime, true
}

func clearRuntimeFiles(files []domain.RuntimeFile) {
	for index := range files {
		clear(files[index].Data)
	}
}
