package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// patServerStore is the Server's store: it resolves the session's encrypted PAT
// and swallows the success-path projection event.
type patServerStore struct {
	Store
	pat    domain.WorkerGitHubPAT
	patErr error
}

func (s *patServerStore) WorkerGitHubPAT(context.Context, string, string, string, int64) (domain.WorkerGitHubPAT, error) {
	if s.patErr != nil {
		return domain.WorkerGitHubPAT{}, s.patErr
	}
	return s.pat, nil
}

func (s *patServerStore) AppendSessionEvent(context.Context, string, string, string, json.RawMessage) (domain.ClientEvent, error) {
	return domain.ClientEvent{}, nil
}

// patRecordStore is the PAT write service's record store.
type patRecordStore struct{ created int }

func (s *patRecordStore) CreatePullRequestRecord(
	_ context.Context, _, _, _, repository, author string, number int,
	url, source, target, _, title string, _, _, _ int,
) (domain.PullRequest, error) {
	s.created++
	return domain.PullRequest{ID: "rec", Number: number, URL: url, Title: title, Repository: repository, Author: author, SourceBranch: source, TargetBranch: target}, nil
}

func (s *patRecordStore) ClaimPullRequestRecord(_ context.Context, _, _ string, input domain.PullRequest) (domain.PullRequest, error) {
	return input, nil
}

// recordingCheckoutBroker tracks whether a write reached the broker.
type recordingCheckoutBroker struct{ raiseCalls int }

func (b *recordingCheckoutBroker) IssueCheckoutGrant(context.Context, string, string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, nil
}
func (b *recordingCheckoutBroker) IssuePushGrant(context.Context, string, string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, nil
}
func (b *recordingCheckoutBroker) RaisePullRequest(context.Context, string, string, domain.RaisePullRequest) (domain.PullRequest, error) {
	b.raiseCalls++
	return domain.PullRequest{ID: "broker-pr", Number: 99, URL: "https://github.com/octo/widgets/pull/99"}, nil
}
func (b *recordingCheckoutBroker) ClaimPullRequest(context.Context, string, string, string) (domain.PullRequest, error) {
	return domain.PullRequest{}, nil
}
func (b *recordingCheckoutBroker) SubmitReview(context.Context, string, string, string, domain.SubmitReviewResult) (domain.ReviewRun, error) {
	return domain.ReviewRun{}, nil
}

func newPATTestServer(t *testing.T, patErr error) (*Server, *recordingCheckoutBroker, *patRecordStore, *githubServerRecorder) {
	t.Helper()
	key := make([]byte, 32)
	cipher, err := secrets.New(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	const ownerID = "11111111-1111-1111-1111-111111111111"
	const pat = "ghp_HANDLERtestPAT0000000000000000000"
	encrypted, nonce, err := cipher.Encrypt([]byte(pat), providerSecretAssociatedData("user:"+ownerID, githubPATProvider))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	rec := &githubServerRecorder{}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.auth = r.Header.Get("Authorization")
		rec.hits++
		if r.Method == http.MethodPost && r.URL.Path == "/repos/octo/widgets/pulls" {
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "number": 7, "html_url": "https://github.com/octo/widgets/pull/7",
				"state": "open", "title": "Add logging", "user": map[string]any{"login": "octocat"},
				"head": map[string]any{"sha": "abc123", "ref": "feature"}, "base": map[string]any{"ref": "main"},
			})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/widgets/pulls/7" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "number": 7, "html_url": "https://github.com/octo/widgets/pull/7",
				"state": "open", "title": "Add logging", "user": map[string]any{"login": "octocat"},
				"head": map[string]any{"sha": "abc123", "ref": "feature"}, "base": map[string]any{"ref": "main"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(gh.Close)

	serverStore := &patServerStore{
		pat:    domain.WorkerGitHubPAT{OwnerUserID: ownerID, CloneURL: "https://github.com/octo/widgets.git", EncryptedSecret: encrypted, Nonce: nonce},
		patErr: patErr,
	}
	broker := &recordingCheckoutBroker{}
	recordStore := &patRecordStore{}
	patWrites := githubapp.NewPATWriteService(githubapp.NewRESTClient(gh.URL, gh.Client()), recordStore)

	srv := New(Options{
		Store:          serverStore,
		SecretCipher:   cipher,
		CheckoutBroker: broker,
		PATWrites:      patWrites,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv, broker, recordStore, rec
}

type githubServerRecorder struct {
	auth string
	hits int
}

func patRaiseRequest(t *testing.T) *http.Request {
	body := `{"title":"Add logging","headBranch":"feature","baseBranch":"main"}`
	return workerRequest(t, http.MethodPost, "/worker/pull-requests", body, "worker:git")
}

// With a configured PAT the write must go to GitHub with the PAT, and the
// (write-incapable) broker must never be touched.
func TestWorkerRaisePullRequestPrefersPAT(t *testing.T) {
	srv, broker, recordStore, gh := newPATTestServer(t, nil)
	w := httptest.NewRecorder()
	srv.workerRaisePullRequest(w, patRaiseRequest(t))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if broker.raiseCalls != 0 {
		t.Fatalf("broker.RaisePullRequest was called %d times; the PAT path must bypass it", broker.raiseCalls)
	}
	if gh.hits == 0 {
		t.Fatal("GitHub was never called; the PAT path did not run")
	}
	if gh.auth != "Bearer ghp_HANDLERtestPAT0000000000000000000" {
		t.Fatalf("GitHub Authorization = %q, want the PAT as bearer", gh.auth)
	}
	if recordStore.created != 1 {
		t.Fatalf("PR record created %d times, want 1", recordStore.created)
	}
	var resp worker.RaisePullRequestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Number != 7 {
		t.Fatalf("response PR number = %d, want the PAT-created 7 (not the broker's 99)", resp.Number)
	}
}

// Without a PAT the handler must fall through to the checkout broker.
func TestWorkerRaisePullRequestFallsBackToBrokerWithoutPAT(t *testing.T) {
	srv, broker, _, gh := newPATTestServer(t, postgres.ErrNotFound)
	w := httptest.NewRecorder()
	srv.workerRaisePullRequest(w, patRaiseRequest(t))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if broker.raiseCalls != 1 {
		t.Fatalf("broker.RaisePullRequest called %d times, want 1 (no PAT → broker)", broker.raiseCalls)
	}
	if gh.hits != 0 {
		t.Fatalf("GitHub was called %d times; without a PAT the PAT path must not run", gh.hits)
	}
}

// A configured PAT must work even on a deployment with no GitHub App wired up
// at all (CheckoutBroker == nil, e.g. because options.GitHub is nil in
// server.go). Before the PAT-first reordering fix, the handler's own
// checkoutBroker nil-guard short-circuited before ever trying the PAT.
func TestWorkerRaisePullRequestPrefersPATWithNoBroker(t *testing.T) {
	srv, _, _, gh := newPATTestServer(t, nil)
	srv.checkoutBroker = nil
	w := httptest.NewRecorder()
	srv.workerRaisePullRequest(w, patRaiseRequest(t))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if gh.hits == 0 {
		t.Fatal("GitHub was never called; the PAT path did not run with a nil broker")
	}
}

// Without a PAT and without a broker, the handler must fail closed with
// SCM_BROKER_UNAVAILABLE rather than nil-panicking on s.checkoutBroker.
func TestWorkerRaisePullRequestUnavailableWithNoPATAndNoBroker(t *testing.T) {
	srv, _, _, gh := newPATTestServer(t, postgres.ErrNotFound)
	srv.checkoutBroker = nil
	w := httptest.NewRecorder()
	srv.workerRaisePullRequest(w, patRaiseRequest(t))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if gh.hits != 0 {
		t.Fatalf("GitHub was called %d times; expected no network calls", gh.hits)
	}
}

func patClaimRequest(t *testing.T) *http.Request {
	body := `{"reference":"https://github.com/octo/widgets/pull/7"}`
	return workerRequest(t, http.MethodPost, "/worker/pull-requests/claim", body, "worker:git")
}

// Mirrors TestWorkerRaisePullRequestPrefersPATWithNoBroker for the claim path.
func TestWorkerClaimPullRequestPrefersPATWithNoBroker(t *testing.T) {
	srv, _, _, gh := newPATTestServer(t, nil)
	srv.checkoutBroker = nil
	w := httptest.NewRecorder()
	srv.workerClaimPullRequest(w, patClaimRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gh.hits == 0 {
		t.Fatal("GitHub was never called; the PAT path did not run with a nil broker")
	}
}

func TestWorkerClaimPullRequestUnavailableWithNoPATAndNoBroker(t *testing.T) {
	srv, _, _, gh := newPATTestServer(t, postgres.ErrNotFound)
	srv.checkoutBroker = nil
	w := httptest.NewRecorder()
	srv.workerClaimPullRequest(w, patClaimRequest(t))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if gh.hits != 0 {
		t.Fatalf("GitHub was called %d times; expected no network calls", gh.hits)
	}
}
