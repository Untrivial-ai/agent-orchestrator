package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestIndexWorkerBinaries(t *testing.T) {
	a := []byte("worker binary")
	b := []byte("helper binary")
	index := indexWorkerBinaries(a, b, nil, []byte{})
	if len(index) != 2 {
		t.Fatalf("index has %d entries, want 2 (empty binaries skipped)", len(index))
	}
	if string(index[sha256Hex(a)]) != string(a) {
		t.Fatal("worker binary not indexed by its hash")
	}
	if string(index[sha256Hex(b)]) != string(b) {
		t.Fatal("helper binary not indexed by its hash")
	}
}

func binaryServer(t *testing.T, binaries ...[]byte) *Server {
	t.Helper()
	return &Server{
		workerBinariesBySHA: indexWorkerBinaries(binaries...),
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func binaryRequest(sha string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sha256", sha)
	r := httptest.NewRequest(http.MethodGet, "/worker/binary/"+sha, nil)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestServeWorkerBinaryReturnsContentAddressed(t *testing.T) {
	workerBin := []byte("#!/bin/true\nao-worker\n")
	srv := binaryServer(t, workerBin)
	w := httptest.NewRecorder()
	srv.serveWorkerBinary(w, binaryRequest(sha256Hex(workerBin)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != string(workerBin) {
		t.Fatalf("body = %q, want the worker bytes", w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content-type = %q", got)
	}
}

func TestServeWorkerBinaryUnknownHashIs404(t *testing.T) {
	srv := binaryServer(t, []byte("ao-worker"))
	w := httptest.NewRecorder()
	srv.serveWorkerBinary(w, binaryRequest(sha256Hex([]byte("not served"))))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

type reconnectStore struct {
	Store
	launch domain.WorkerLaunch
	err    error
}

func (s *reconnectStore) WorkerLaunchSpec(context.Context, string, string) (domain.WorkerLaunch, error) {
	return s.launch, s.err
}

func TestWorkerReconnectReturnsLaunchContext(t *testing.T) {
	store := &reconnectStore{launch: domain.WorkerLaunch{
		SessionID:     testOrchestratorID,
		Harness:       "claude-code",
		Branch:        "ao/abc",
		RepositoryURL: "https://github.com/octo/widgets.git",
		DefaultBranch: "main",
	}}
	srv := testServer(store)
	w := httptest.NewRecorder()
	srv.workerReconnect(w, workerRequest(t, http.MethodGet, "/worker/reconnect", "", "worker:connect"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp worker.BootstrapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SessionID != testOrchestratorID || resp.WorkerID != "w1" || resp.Epoch != 1 {
		t.Fatalf("identity not carried from claims: %+v", resp)
	}
	if resp.Launch.Harness != "claude-code" || resp.Launch.Branch != "ao/abc" ||
		resp.Launch.RepositoryURL != "https://github.com/octo/widgets.git" {
		t.Fatalf("launch context not projected: %+v", resp.Launch)
	}
	if resp.WorkerToken != "" {
		t.Fatal("reconnect must not mint a new token; the worker keeps its rotating one")
	}
}

func TestWorkerReconnectRequiresConnectScope(t *testing.T) {
	srv := testServer(&reconnectStore{})
	w := httptest.NewRecorder()
	srv.workerReconnect(w, workerRequest(t, http.MethodGet, "/worker/reconnect", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
