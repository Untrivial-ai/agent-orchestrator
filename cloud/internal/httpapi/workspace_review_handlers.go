package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func (s *Server) getWorkspaceReview(w http.ResponseWriter, r *http.Request) {
	s.dispatchWorkspaceReview(w, r, "workspace.review.summary", struct{}{}, &worker.WorkspaceReviewResponse{})
}

func (s *Server) getWorkspaceReviewTree(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if len(path) > maxWorkspacePath {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The workspace path is too long.")
		return
	}
	s.dispatchWorkspaceReview(w, r, "workspace.review.tree", worker.WorkspaceReviewTreeRequest{Path: path}, &worker.WorkspaceReviewTreeResponse{})
}

func (s *Server) getWorkspaceReviewSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" || len(query) > maxWorkspacePath {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid workspace search query is required.")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100.")
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 1024 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The workspace search cursor is too long.")
		return
	}
	s.dispatchWorkspaceReview(w, r, "workspace.review.search", worker.WorkspaceReviewSearchRequest{
		Query: query, Cursor: cursor, Limit: limit,
	}, &worker.WorkspaceReviewSearchResponse{})
}

func (s *Server) getWorkspaceReviewFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" || len(path) > maxWorkspacePath {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid workspace-relative path is required.")
		return
	}
	scope, ok := reviewScopeQuery(w, r)
	if !ok {
		return
	}
	s.dispatchWorkspaceReview(w, r, "workspace.review.file", worker.WorkspaceReviewFileRequest{
		Path: path, Scope: scope, CommitSHA: r.URL.Query().Get("commitSha"),
	}, &worker.WorkspaceReviewFileResponse{})
}

func (s *Server) postWorkspaceReviewDiffs(w http.ResponseWriter, r *http.Request) {
	var input worker.WorkspaceReviewDiffsRequest
	if err := decodeJSONLimit(w, r, &input, 128*1024); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !validReviewScope(input.Scope, false) || len(input.Paths) < 1 || len(input.Paths) > 100 || input.ContextLines < 0 || input.ContextLines > 20 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The workspace diff request is invalid.")
		return
	}
	for _, path := range input.Paths {
		if strings.TrimSpace(path) == "" || len(path) > maxWorkspacePath {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The workspace diff path is invalid.")
			return
		}
	}
	s.dispatchWorkspaceReview(w, r, "workspace.review.diffs", input, &worker.WorkspaceReviewDiffsResponse{})
}

func (s *Server) getWorkspaceReviewRevision(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" || len(path) > maxWorkspacePath {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid workspace-relative path is required.")
		return
	}
	scope, ok := reviewScopeQuery(w, r)
	if !ok {
		return
	}
	side := worker.WorkspaceReviewSide(r.URL.Query().Get("side"))
	if side == "" {
		side = worker.WorkspaceReviewAfter
	}
	if side != worker.WorkspaceReviewBefore && side != worker.WorkspaceReviewAfter {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "side must be before or after.")
		return
	}
	s.dispatchWorkspaceReview(w, r, "workspace.review.revision", worker.WorkspaceReviewRevisionRequest{
		Path: path, Scope: scope, Side: side,
		WorkspaceVersion: r.URL.Query().Get("workspaceVersion"),
		ExpectedRevision: r.URL.Query().Get("expectedRevision"),
		CommitSHA:        r.URL.Query().Get("commitSha"),
	}, &worker.WorkspaceReviewRevisionResponse{})
}

func (s *Server) putWorkspaceReviewFile(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	var input worker.WorkspaceReviewWriteRequest
	if err := decodeJSONLimit(w, r, &input, maxWorkspaceFile+maxWorkspacePath+2048); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.Path) == "" || len(input.Path) > maxWorkspacePath || len(input.Content) > maxWorkspaceFile || input.ExpectedFileFingerprint == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The workspace file update is invalid.")
		return
	}
	var response worker.WorkspaceReviewWriteResponse
	if !s.dispatchWorkspaceReview(w, r, "workspace.review.write", input, &response) {
		return
	}
	s.appendSessionProjectionEvent(r.Context(), orgID, sessionID, "workspace.changed", map[string]string{"path": response.Path})
}

func (s *Server) dispatchWorkspaceReview(w http.ResponseWriter, r *http.Request, kind string, input any, response any) bool {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return false
	}
	payload, err := json.Marshal(input)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The workspace request could not be encoded.")
		return false
	}
	result, ok := s.runWorkspaceRequest(w, r, orgID, sessionID, kind, payload)
	if !ok {
		return false
	}
	if err := json.Unmarshal(result, response); err != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned an invalid workspace review response.")
		return false
	}
	writeJSON(w, http.StatusOK, response)
	return true
}

func reviewScopeQuery(w http.ResponseWriter, r *http.Request) (worker.WorkspaceReviewScope, bool) {
	scope := worker.WorkspaceReviewScope(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = worker.WorkspaceReviewCombined
	}
	if !validReviewScope(scope, true) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "scope must be combined, committed, staged, unstaged, or untracked.")
		return "", false
	}
	return scope, true
}

func validReviewScope(scope worker.WorkspaceReviewScope, allowEmpty bool) bool {
	if allowEmpty && scope == "" {
		return true
	}
	switch scope {
	case worker.WorkspaceReviewCombined, worker.WorkspaceReviewCommitted, worker.WorkspaceReviewStaged,
		worker.WorkspaceReviewUnstaged, worker.WorkspaceReviewUntracked:
		return true
	default:
		return false
	}
}
