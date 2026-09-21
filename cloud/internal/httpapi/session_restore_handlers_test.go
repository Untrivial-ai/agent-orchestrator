package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

const testWorkerSessionID = testOrchestratorID

// stubTranscriptStore records the last Put and serves a canned Get, so handler
// tests can assert the base64 decode/encode boundary and the 404 path without a
// database.
type stubTranscriptStore struct {
	putOrg, putSession, putAgent, putHarness, putRef string
	putTranscript                                    []byte
	putErr                                           error

	getAgent, getHarness, getRef string
	getTranscript                []byte
	getErr                       error
}

func (s *stubTranscriptStore) Put(
	_ context.Context, orgID, sessionID, agentSessionID, harness string,
	transcript []byte, preservedGitRef string,
) error {
	s.putOrg = orgID
	s.putSession = sessionID
	s.putAgent = agentSessionID
	s.putHarness = harness
	s.putTranscript = transcript
	s.putRef = preservedGitRef
	return s.putErr
}

func (s *stubTranscriptStore) Get(
	_ context.Context, _, _ string,
) (string, string, []byte, string, error) {
	if s.getErr != nil {
		return "", "", nil, "", s.getErr
	}
	return s.getAgent, s.getHarness, s.getTranscript, s.getRef, nil
}

func transcriptServer(store TranscriptStore) *Server {
	return &Server{
		transcripts: store,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// A worker capture decodes the base64 transcript, stores it under the worker's
// own claimed org/session, and returns 204 with no body.
func TestWorkerPutTranscriptStoresDecodedBlob(t *testing.T) {
	store := &stubTranscriptStore{}
	srv := transcriptServer(store)
	raw := []byte("{\"type\":\"message\"}\n{\"type\":\"result\"}\n")
	body := `{"agentSessionId":"agent-7","harness":"claude-code","transcript":"` +
		base64.StdEncoding.EncodeToString(raw) + `","preservedGitRef":"refs/ao/preserved/abc"}`

	w := httptest.NewRecorder()
	srv.workerPutTranscript(w, workerRequest(t, http.MethodPut, "/worker/transcript", body, "worker:connect"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if store.putOrg != testOrgID || store.putSession != testWorkerSessionID {
		t.Fatalf("stored under org=%q session=%q, want the worker's own claims", store.putOrg, store.putSession)
	}
	if string(store.putTranscript) != string(raw) {
		t.Fatalf("stored transcript = %q, want the decoded bytes %q", store.putTranscript, raw)
	}
	if store.putAgent != "agent-7" || store.putHarness != "claude-code" || store.putRef != "refs/ao/preserved/abc" {
		t.Fatalf("stored metadata mismatch: agent=%q harness=%q ref=%q", store.putAgent, store.putHarness, store.putRef)
	}
}

// A non-base64 transcript is a client error, not a 500.
func TestWorkerPutTranscriptRejectsNonBase64(t *testing.T) {
	srv := transcriptServer(&stubTranscriptStore{})
	body := `{"agentSessionId":"a","harness":"h","transcript":"not base64!!","preservedGitRef":""}`
	w := httptest.NewRecorder()
	srv.workerPutTranscript(w, workerRequest(t, http.MethodPut, "/worker/transcript", body, "worker:connect"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// The worker:connect scope is required to push a transcript.
func TestWorkerPutTranscriptRequiresScope(t *testing.T) {
	srv := transcriptServer(&stubTranscriptStore{})
	body := `{"agentSessionId":"a","harness":"h","transcript":"","preservedGitRef":""}`
	w := httptest.NewRecorder()
	srv.workerPutTranscript(w, workerRequest(t, http.MethodPut, "/worker/transcript", body, "worker:git"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// A capture read re-encodes the stored bytes as base64 for the wire.
func TestWorkerGetTranscriptReturnsBase64(t *testing.T) {
	raw := []byte("transcript-bytes")
	store := &stubTranscriptStore{
		getAgent:      "agent-9",
		getHarness:    "codex",
		getTranscript: raw,
		getRef:        "refs/ao/preserved/def",
	}
	srv := transcriptServer(store)
	w := httptest.NewRecorder()
	srv.workerGetTranscript(w, workerRequest(t, http.MethodGet, "/worker/transcript", "", "worker:connect"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp workerTranscriptResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Transcript != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("transcript = %q, want base64 of stored bytes", resp.Transcript)
	}
	if resp.AgentSessionID != "agent-9" || resp.Harness != "codex" || resp.PreservedGitRef != "refs/ao/preserved/def" {
		t.Fatalf("metadata mismatch: %+v", resp)
	}
}

// No capture yet is a 404, distinct from a store failure.
func TestWorkerGetTranscriptMissingIs404(t *testing.T) {
	srv := transcriptServer(&stubTranscriptStore{getErr: postgres.ErrNotFound})
	w := httptest.NewRecorder()
	srv.workerGetTranscript(w, workerRequest(t, http.MethodGet, "/worker/transcript", "", "worker:connect"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// restoreStubStore captures the RestoreSession call so the handler test can
// assert the un-terminate reached the store with the right identifiers.
type restoreStubStore struct {
	Store
	gotOrg, gotSession string
	err                error
}

func (s *restoreStubStore) RestoreSession(
	_ context.Context, _ domain.Principal, orgID, sessionID string,
) error {
	s.gotOrg = orgID
	s.gotSession = sessionID
	return s.err
}

func restoreRequest(t *testing.T, orgID, sessionID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/cloud/v1/orgs/"+orgID+"/sessions/"+sessionID+"/restore", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", orgID)
	rctx.URLParams.Add("sessionId", sessionID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-0000000000f6"})
	return req.WithContext(ctx)
}

// Restore un-terminates the same session_id and reports 202 Accepted.
func TestRestoreSessionUnterminates(t *testing.T) {
	store := &restoreStubStore{}
	srv := testServer(store)
	w := httptest.NewRecorder()
	srv.restoreSession(w, restoreRequest(t, testOrgID, testWorkerSessionID))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if store.gotOrg != testOrgID || store.gotSession != testWorkerSessionID {
		t.Fatalf("RestoreSession called with org=%q session=%q, want %q/%q",
			store.gotOrg, store.gotSession, testOrgID, testWorkerSessionID)
	}
}

// A restore of an unknown session surfaces the store's not-found as 404.
func TestRestoreSessionNotFound(t *testing.T) {
	srv := testServer(&restoreStubStore{err: postgres.ErrNotFound})
	w := httptest.NewRecorder()
	srv.restoreSession(w, restoreRequest(t, testOrgID, testWorkerSessionID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// A bad UUID is rejected before the store is touched.
func TestRestoreSessionRejectsNonUUID(t *testing.T) {
	srv := testServer(&restoreStubStore{})
	w := httptest.NewRecorder()
	srv.restoreSession(w, restoreRequest(t, testOrgID, "not-a-uuid"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
