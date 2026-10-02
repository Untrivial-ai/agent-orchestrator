package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	opened int
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

func (s *patServerStore) RecordPullRequestOpened(context.Context, string, domain.PullRequest, string) error {
	s.opened++
	return nil
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

// recordingCheckoutBroker tracks whether a write reached the broker. raiseErr, when
// set, simulates a broker that cannot open PRs (e.g. the remote capability broker,
// which returns errRemotePushNotSupported), so the PAT fallback can be exercised.
type recordingCheckoutBroker struct {
	raiseCalls int
	raiseErr   error
	claimCalls int
	claimErr   error
}

func (b *recordingCheckoutBroker) IssueCheckoutGrant(context.Context, string, string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, nil
}
func (b *recordingCheckoutBroker) IssuePushGrant(context.Context, string, string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, nil
}
func (b *recordingCheckoutBroker) IssuePushGrantForRepo(context.Context, string, string, string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, nil
}
func (b *recordingCheckoutBroker) RaisePullRequest(context.Context, string, string, domain.RaisePullRequest) (domain.PullRequest, error) {
	b.raiseCalls++
	if b.raiseErr != nil {
		return domain.PullRequest{}, b.raiseErr
	}
	return domain.PullRequest{ID: "broker-pr", Number: 99, URL: "https://github.com/octo/widgets/pull/99"}, nil
}
func (b *recordingCheckoutBroker) ClaimPullRequest(context.Context, string, string, string) (domain.PullRequest, error) {
	b.claimCalls++
	if b.claimErr != nil {
		return domain.PullRequest{}, b.claimErr
	}
	return domain.PullRequest{ID: "broker-pr", Number: 7, URL: "https://github.com/octo/widgets/pull/7"}, nil
}
func (b *recordingCheckoutBroker) SubmitReview(context.Context, string, string, string, domain.SubmitReviewResult) (domain.ReviewRun, error) {
	return domain.ReviewRun{}, nil
}

func newPATTestServer(t *testing.T, patErr error) (*Server, *recordingCheckoutBroker, *patRecordStore, *githubServerRecorder, *patServerStore) {
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
		// PAT claim path fetches the PR (GetPullRequestRecord) before recording it.
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
	return srv, broker, recordStore, rec, serverStore
}

type githubServerRecorder struct {
	auth string
	hits int
}

func patRaiseRequest(t *testing.T) *http.Request {
	body := `{"title":"Add logging","headBranch":"feature","baseBranch":"main"}`
	return workerRequest(t, http.MethodPost, "/worker/pull-requests", body, "worker:git")
}

// The GitHub App (checkout broker) must win even when a PAT exists: preferring the
// PAT let a rotted-but-cached-valid PAT shadow a healthy App and fail every PR with
// "pull request could not be opened". When the broker can open the PR, the PAT path
// must not run.
func TestWorkerRaisePullRequestPrefersBrokerAppOverPAT(t *testing.T) {
	srv, broker, _, gh, serverStore := newPATTestServer(t, nil)
	w := httptest.NewRecorder()
	srv.workerRaisePullRequest(w, patRaiseRequest(t))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if broker.raiseCalls != 1 {
		t.Fatalf("broker.RaisePullRequest called %d times, want 1 (App tried first)", broker.raiseCalls)
	}
	if gh.hits != 0 {
		t.Fatalf("GitHub PAT path was called %d times; a healthy App grant must not be shadowed by a PAT", gh.hits)
	}
	if serverStore.opened != 1 {
		t.Fatalf("PR opened notifications = %d, want 1", serverStore.opened)
	}
	var resp worker.RaisePullRequestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Number != 99 {
		t.Fatalf("response PR number = %d, want the App/broker-created 99", resp.Number)
	}
}

// When the broker cannot open the PR (a repository authorized through the remote
// capability broker returns errRemotePushNotSupported), fall back to the PAT.
func TestWorkerRaisePullRequestFallsBackToPATWhenBrokerCannotWrite(t *testing.T) {
	srv, broker, recordStore, gh, serverStore := newPATTestServer(t, nil)
	broker.raiseErr = errors.New("raising a pull request is not supported for the remote capability broker")
	w := httptest.NewRecorder()
	srv.workerRaisePullRequest(w, patRaiseRequest(t))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if broker.raiseCalls != 1 {
		t.Fatalf("broker.RaisePullRequest called %d times, want 1 (App tried first)", broker.raiseCalls)
	}
	if gh.hits == 0 {
		t.Fatal("GitHub PAT path was never called; the fallback did not run")
	}
	if gh.auth != "Bearer ghp_HANDLERtestPAT0000000000000000000" {
		t.Fatalf("GitHub Authorization = %q, want the PAT as bearer on fallback", gh.auth)
	}
	if recordStore.created != 1 {
		t.Fatalf("PR record created %d times, want 1", recordStore.created)
	}
	if serverStore.opened != 1 {
		t.Fatalf("PR opened notifications = %d, want 1", serverStore.opened)
	}
	var resp worker.RaisePullRequestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Number != 7 {
		t.Fatalf("response PR number = %d, want the PAT-created 7 on fallback", resp.Number)
	}
}

// Without a PAT the handler must fall through to the checkout broker.
func TestWorkerRaisePullRequestFallsBackToBrokerWithoutPAT(t *testing.T) {
	srv, broker, _, gh, serverStore := newPATTestServer(t, postgres.ErrNotFound)
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
	if serverStore.opened != 1 {
		t.Fatalf("PR opened notifications = %d, want 1", serverStore.opened)
	}
}

func TestWorkerClaimPullRequestRecordsOpenedNotification(t *testing.T) {
	srv, _, _, _, serverStore := newPATTestServer(t, postgres.ErrNotFound)
	req := workerRequest(t, http.MethodPost, "/worker/pull-requests/claim", `{"reference":"https://github.com/octo/widgets/pull/7"}`, "worker:git")
	w := httptest.NewRecorder()
	srv.workerClaimPullRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if serverStore.opened != 1 {
		t.Fatalf("PR opened notifications = %d, want 1", serverStore.opened)
	}
}

// The GitHub App (checkout broker) must win the CLAIM too, even when a PAT exists.
// A PAT-first order let a rotted-but-cached-valid PAT shadow a healthy App and
// fail every `ao claim-pr` / gh-create claim with "The pull request could not be
// tracked" (GitHub 401). Regression test: a healthy App claim must not touch the PAT.
func TestWorkerClaimPullRequestPrefersBrokerAppOverPAT(t *testing.T) {
	srv, broker, _, gh, serverStore := newPATTestServer(t, nil) // PAT configured
	req := workerRequest(t, http.MethodPost, "/worker/pull-requests/claim", `{"reference":"https://github.com/octo/widgets/pull/7"}`, "worker:git")
	w := httptest.NewRecorder()
	srv.workerClaimPullRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if broker.claimCalls != 1 {
		t.Fatalf("broker.ClaimPullRequest called %d times, want 1 (App tried first)", broker.claimCalls)
	}
	if gh.hits != 0 {
		t.Fatalf("PAT GitHub server hit %d times, want 0 (a healthy App claim must not fall back to a possibly-stale PAT)", gh.hits)
	}
	if serverStore.opened != 1 {
		t.Fatalf("PR opened notifications = %d, want 1", serverStore.opened)
	}
}

// When the broker cannot claim (e.g. a remote capability broker is read-only),
// fall back to the PAT — the App-first/PAT-fallback contract, same as raise.
func TestWorkerClaimPullRequestFallsBackToPATWhenBrokerCannotWrite(t *testing.T) {
	srv, broker, _, gh, serverStore := newPATTestServer(t, nil) // PAT configured
	broker.claimErr = errors.New("claiming is not supported for the remote capability broker")
	req := workerRequest(t, http.MethodPost, "/worker/pull-requests/claim", `{"reference":"https://github.com/octo/widgets/pull/7"}`, "worker:git")
	w := httptest.NewRecorder()
	srv.workerClaimPullRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (PAT fallback); body=%s", w.Code, w.Body.String())
	}
	if broker.claimCalls != 1 {
		t.Fatalf("broker.ClaimPullRequest called %d times, want 1 (App tried first)", broker.claimCalls)
	}
	if gh.hits == 0 {
		t.Fatal("GitHub PAT path was never called; the claim fallback did not run")
	}
	if gh.auth != "Bearer ghp_HANDLERtestPAT0000000000000000000" {
		t.Fatalf("GitHub Authorization = %q, want the PAT as bearer on fallback", gh.auth)
	}
	if serverStore.opened != 1 {
		t.Fatalf("PR opened notifications = %d, want 1", serverStore.opened)
	}
}
