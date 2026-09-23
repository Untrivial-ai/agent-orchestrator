package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

// TranscriptStore is the durable blob store for captured harness transcripts.
// The handlers depend on this narrow Put/Get abstraction rather than the
// concrete Postgres store so the backend can later be swapped for an object
// store (S3) without touching the HTTP layer.
type TranscriptStore interface {
	Put(ctx context.Context, orgID, sessionID, agentSessionID, harness string, transcript []byte, preservedGitRef string) error
	Get(ctx context.Context, orgID, sessionID string) (agentSessionID, harness string, transcript []byte, preservedGitRef string, err error)
}

// maxTranscriptBody bounds a transcript capture. Harness JSONL transcripts grow
// with conversation length, and base64 inflates the wire body ~33%, so this is
// deliberately generous relative to the 1 MiB default request cap.
const maxTranscriptBody = 64 << 20

type workerTranscriptRequest struct {
	AgentSessionID  string `json:"agentSessionId"`
	Harness         string `json:"harness"`
	Transcript      string `json:"transcript"`
	PreservedGitRef string `json:"preservedGitRef"`
}

type workerTranscriptResponse struct {
	AgentSessionID  string `json:"agentSessionId"`
	Harness         string `json:"harness"`
	Transcript      string `json:"transcript"`
	PreservedGitRef string `json:"preservedGitRef"`
}

// workerPutTranscript stores the latest transcript capture for the worker's own
// session. Org and session come from the worker's verified claims, never the
// request body, so a sandbox can only ever write its own session's transcript.
func (s *Server) workerPutTranscript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:connect") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:connect scope is required.")
		return
	}
	if s.transcripts == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "Transcript capture is not enabled.")
		return
	}
	var input workerTranscriptRequest
	if err := decodeJSONLimit(w, r, &input, maxTranscriptBody); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	transcript, err := base64.StdEncoding.DecodeString(input.Transcript)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "transcript must be base64-encoded.")
		return
	}
	if err := s.transcripts.Put(
		r.Context(),
		claims.OrgID,
		claims.SessionID,
		input.AgentSessionID,
		input.Harness,
		transcript,
		input.PreservedGitRef,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workerGetTranscript returns the latest transcript capture for the worker's
// own session so a re-provisioned worker can rehydrate its harness. 404 when no
// capture exists yet (a fresh session, or one that never captured).
func (s *Server) workerGetTranscript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:connect") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:connect scope is required.")
		return
	}
	if s.transcripts == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "Transcript capture is not enabled.")
		return
	}
	agentSessionID, harness, transcript, preservedGitRef, err := s.transcripts.Get(
		r.Context(), claims.OrgID, claims.SessionID,
	)
	if errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "No transcript has been captured for this session.")
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, workerTranscriptResponse{
		AgentSessionID:  agentSessionID,
		Harness:         harness,
		Transcript:      base64.StdEncoding.EncodeToString(transcript),
		PreservedGitRef: preservedGitRef,
	})
}

// restoreSession un-terminates a previously deleted session under the same
// session_id and queues a fresh sandbox provision. The reconciler and worker
// then re-provision and rehydrate from the captured transcript. Accepted (202)
// because the actual provision happens asynchronously.
func (s *Server) restoreSession(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	if err := s.store.RestoreSession(r.Context(), principalFrom(r), orgID, sessionID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"session": map[string]any{"id": sessionID, "restored": true},
	})
}
