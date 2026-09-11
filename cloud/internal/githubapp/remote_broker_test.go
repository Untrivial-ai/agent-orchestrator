package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
)

// mockCapabilityStore provides a mock implementation of RemoteCapabilityStore for unit tests.
type mockCapabilityStore struct {
	workerRemoteCtx     func(context.Context, string, string) (domain.RemoteGitHubCheckoutContext, error)
	reviewRunPullReq    func(context.Context, string, string) (domain.ReviewRunPullRequest, error)
	failReviewRun       func(context.Context, string, string, string, string) (domain.ReviewRun, error)
	closeReviewTerminal func(context.Context, string, string, string) error
	createPRRecord      func(context.Context, string, string, string, string, string, int, string, string, string, string, string, int, int, int) (domain.PullRequest, error)
	claimPRRecord       func(context.Context, string, string, domain.PullRequest) (domain.PullRequest, error)
	completeDeliverRun  func(context.Context, string, string, string, domain.SubmitReviewResult, string) (domain.ReviewRun, error)
	createReviewRun     func(context.Context, string, string, string, string) (domain.ReviewRun, bool, error)
	openReviewTerminal  func(context.Context, string, string, string, string) error
}

func (m *mockCapabilityStore) WorkerRemoteGitHubCheckoutContext(ctx context.Context, orgID, sessionID string) (domain.RemoteGitHubCheckoutContext, error) {
	if m.workerRemoteCtx != nil {
		return m.workerRemoteCtx(ctx, orgID, sessionID)
	}
	return domain.RemoteGitHubCheckoutContext{}, errors.New("workerRemoteCtx not configured")
}
func (m *mockCapabilityStore) ReviewRunPullRequest(ctx context.Context, orgID, reviewRunID string) (domain.ReviewRunPullRequest, error) {
	if m.reviewRunPullReq != nil {
		return m.reviewRunPullReq(ctx, orgID, reviewRunID)
	}
	return domain.ReviewRunPullRequest{}, errors.New("reviewRunPullReq not configured")
}
func (m *mockCapabilityStore) FailReviewRun(ctx context.Context, orgID, runID, sessionID, lastErr string) (domain.ReviewRun, error) {
	if m.failReviewRun != nil {
		return m.failReviewRun(ctx, orgID, runID, sessionID, lastErr)
	}
	return domain.ReviewRun{}, nil
}
func (m *mockCapabilityStore) CloseReviewTerminal(ctx context.Context, orgID, sessionID, reviewRunID string) error {
	if m.closeReviewTerminal != nil {
		return m.closeReviewTerminal(ctx, orgID, sessionID, reviewRunID)
	}
	return nil
}
func (m *mockCapabilityStore) CreatePullRequestRecord(ctx context.Context, orgID, sessionID, provider, repository, author string, number int, url, sourceBranch, targetBranch, headSHA, title string, additions, deletions, changedFiles int) (domain.PullRequest, error) {
	if m.createPRRecord != nil {
		return m.createPRRecord(ctx, orgID, sessionID, provider, repository, author, number, url, sourceBranch, targetBranch, headSHA, title, additions, deletions, changedFiles)
	}
	return domain.PullRequest{}, nil
}
func (m *mockCapabilityStore) ClaimPullRequestRecord(ctx context.Context, orgID, sessionID string, input domain.PullRequest) (domain.PullRequest, error) {
	if m.claimPRRecord != nil {
		return m.claimPRRecord(ctx, orgID, sessionID, input)
	}
	return domain.PullRequest{}, nil
}
func (m *mockCapabilityStore) CompleteAndDeliverReviewRun(ctx context.Context, orgID, reviewRunID, reviewSessionID string, result domain.SubmitReviewResult, providerReviewID string) (domain.ReviewRun, error) {
	if m.completeDeliverRun != nil {
		return m.completeDeliverRun(ctx, orgID, reviewRunID, reviewSessionID, result, providerReviewID)
	}
	return domain.ReviewRun{}, nil
}
func (m *mockCapabilityStore) CreateReviewRun(ctx context.Context, orgID, pullRequestID, reviewSessionID, targetSHA string) (domain.ReviewRun, bool, error) {
	if m.createReviewRun != nil {
		return m.createReviewRun(ctx, orgID, pullRequestID, reviewSessionID, targetSHA)
	}
	return domain.ReviewRun{}, false, nil
}
func (m *mockCapabilityStore) OpenReviewTerminal(ctx context.Context, orgID, sessionID, reviewRunID, prompt string) error {
	if m.openReviewTerminal != nil {
		return m.openReviewTerminal(ctx, orgID, sessionID, reviewRunID, prompt)
	}
	return nil
}

// Test helpers.


func newTestCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	return c
}

// newTestBroker builds a broker pointed at srv (a TLS test server).
func newTestBroker(t *testing.T, srv *httptest.Server, store RemoteCapabilityStore) *RemoteCheckoutBroker {
	t.Helper()
	b, err := NewRemoteCheckoutBroker(
		store,
		newTestCipher(t),
		srv.URL,
		"staging",
		strings.Repeat("x", 32),
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewRemoteCheckoutBroker: %v", err)
	}
	return b
}

// encryptCapability encrypts a test capability token for the given context
// using the same cipher that will be in the broker.
func encryptCapability(t *testing.T, c *secrets.Cipher, auth domain.RemoteGitHubCheckoutContext) (ciphertext, nonce []byte) {
	t.Helper()
	ct, n, err := c.Encrypt([]byte("test-capability-token"), RepositoryCapabilityAssociatedData(auth))
	if err != nil {
		t.Fatalf("encrypt capability: %v", err)
	}
	return ct, n
}

// testCheckoutContext builds a minimal RemoteGitHubCheckoutContext and
// pre-encrypts its capability so it passes broker validation.
func testCheckoutContext(t *testing.T, c *secrets.Cipher, orgID, sessionID string) domain.RemoteGitHubCheckoutContext {
	t.Helper()
	auth := domain.RemoteGitHubCheckoutContext{
		OrgID:                orgID,
		SessionID:            sessionID,
		ProjectID:            "proj-1",
		GitHubInstallationID: 1001,
		GitHubRepositoryID:   2002,
		UserExternalID:       "user-ext-1",
		TargetEnvironment:    "staging",
		RepositoryURL:        "https://github.com/owner/repo",
	}
	ct, n := encryptCapability(t, c, auth)
	auth.CapabilityCiphertext = ct
	auth.CapabilityNonce = n
	return auth
}

// validWriteGrant returns a JSON body that satisfies IssuePushGrant's
// grant validation: non-empty token, future expiry, matching clone URL.
func validWriteGrant() []byte {
	g := CheckoutGrant{
		Token:    "ghp_test_write_token",
		CloneURL: "https://github.com/owner/repo.git",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	b, _ := json.Marshal(g)
	return b
}

// IssuePushGrant returns ErrRemoteWriteNotSupported for 4xx HTTP responses.


func TestIssuePushGrant_4xxIsTerminalSentinel(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)

			cipher := newTestCipher(t)
			orgID, sessionID := "org-1", "sess-1"
			auth := testCheckoutContext(t, cipher, orgID, sessionID)

			store := &mockCapabilityStore{
				workerRemoteCtx: func(_ context.Context, _, _ string) (domain.RemoteGitHubCheckoutContext, error) {
					return auth, nil
				},
			}
			// Rebuild broker with the same cipher used to encrypt the capability.
			b, err := NewRemoteCheckoutBroker(store, cipher, srv.URL, "staging", strings.Repeat("x", 32), srv.Client())
			if err != nil {
				t.Fatalf("broker: %v", err)
			}

			_, err = b.IssuePushGrant(context.Background(), orgID, sessionID)
			if !errors.Is(err, ErrRemoteWriteNotSupported) {
				t.Errorf("status %d: want errors.Is ErrRemoteWriteNotSupported, got %v", status, err)
			}
		})
	}
}

// IssuePushGrant returns a plain error (retryable) for 5xx HTTP responses.


func TestIssuePushGrant_5xxIsRetryable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	cipher := newTestCipher(t)
	orgID, sessionID := "org-1", "sess-1"
	auth := testCheckoutContext(t, cipher, orgID, sessionID)

	store := &mockCapabilityStore{
		workerRemoteCtx: func(_ context.Context, _, _ string) (domain.RemoteGitHubCheckoutContext, error) {
			return auth, nil
		},
	}
	b, err := NewRemoteCheckoutBroker(store, cipher, srv.URL, "staging", strings.Repeat("x", 32), srv.Client())
	if err != nil {
		t.Fatalf("broker: %v", err)
	}

	_, err = b.IssuePushGrant(context.Background(), orgID, sessionID)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, ErrRemoteWriteNotSupported) {
		t.Errorf("5xx must NOT wrap ErrRemoteWriteNotSupported, got: %v", err)
	}
}

// RaisePullRequest validates input parameters before performing I/O.


func TestRaisePullRequest_EmptyTitleReturnsInvalid(t *testing.T) {
	// A server that should never be reached.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request to production endpoint")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	b := newTestBroker(t, srv, &mockCapabilityStore{})
	_, err := b.RaisePullRequest(context.Background(), "org-1", "sess-1", domain.RaisePullRequest{
		Title:      "",
		HeadBranch: "feature/x",
	})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Errorf("empty title: want ErrInvalid, got %v", err)
	}
}

// ClaimPullRequest rejects unrecognized pull request states with ErrInvalid.


func TestClaimPullRequest_UnknownStateReturnsInvalid(t *testing.T) {
	// The /redeem-write call must succeed so IssuePushGrant returns a grant.
	// The subsequent GitHub PR fetch returns state="merged" which is not
	// open or closed, so the broker must reject it with ErrInvalid.
	callCount := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if strings.Contains(r.URL.Path, "redeem-write") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(validWriteGrant())
			return
		}
		// GitHub PR endpoint → state "merged" (not open/closed).
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/owner/repo/pull/42",
			"state":    "merged",
			"title":    "my PR",
			"user":     map[string]any{"login": "alice"},
			"head":     map[string]any{"sha": "abc123", "ref": "feature/x"},
			"base":     map[string]any{"ref": "main"},
		})
	}))
	t.Cleanup(srv.Close)

	cipher := newTestCipher(t)
	orgID, sessionID := "org-1", "sess-1"
	auth := testCheckoutContext(t, cipher, orgID, sessionID)

	store := &mockCapabilityStore{
		workerRemoteCtx: func(_ context.Context, _, _ string) (domain.RemoteGitHubCheckoutContext, error) {
			return auth, nil
		},
	}
	b, err := NewRemoteCheckoutBroker(store, cipher, srv.URL, "staging", strings.Repeat("x", 32), srv.Client())
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	// Route GitHub REST API calls to the test server too.
	b.githubBase = srv.URL

	_, err = b.ClaimPullRequest(context.Background(), orgID, sessionID, "42")
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Errorf("merged state: want ErrInvalid, got %v", err)
	}
}

// SubmitReview validates input parameters before performing I/O.


func TestSubmitReview_InvalidVerdictReturnsInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request — validation should fire first")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	b := newTestBroker(t, srv, &mockCapabilityStore{})
	_, err := b.SubmitReview(context.Background(), "org-1", "sess-1", "run-1", domain.SubmitReviewResult{
		Verdict: contract.AOReviewVerdict("nonsense"),
		Body:    "looks good",
	})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Errorf("invalid verdict: want ErrInvalid, got %v", err)
	}
}

func TestSubmitReview_EmptyBodyReturnsInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request — validation should fire first")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	b := newTestBroker(t, srv, &mockCapabilityStore{})
	_, err := b.SubmitReview(context.Background(), "org-1", "sess-1", "run-1", domain.SubmitReviewResult{
		Verdict: contract.AOReviewVerdictApproved,
		Body:    "   ", // whitespace only
	})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Errorf("empty body: want ErrInvalid, got %v", err)
	}
}

// SubmitReview verifies session ownership before contacting GitHub.


func TestSubmitReview_WrongSessionReturnsForbidden(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("GitHub should not be called before ownership is confirmed")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	store := &mockCapabilityStore{
		reviewRunPullReq: func(_ context.Context, _, _ string) (domain.ReviewRunPullRequest, error) {
			return domain.ReviewRunPullRequest{
				ReviewRun: domain.ReviewRun{
					ReviewSessionID: "different-session",
					Status:          contract.AOReviewRunRunning,
				},
				PullRequestRepository: "owner/repo",
			}, nil
		},
	}
	b := newTestBroker(t, srv, store)
	_, err := b.SubmitReview(context.Background(), "org-1", "sess-1", "run-1", domain.SubmitReviewResult{
		Verdict: contract.AOReviewVerdictApproved,
		Body:    "LGTM",
	})
	if !errors.Is(err, postgres.ErrForbidden) {
		t.Errorf("wrong session: want ErrForbidden, got %v", err)
	}
}

// SubmitReview rejects already-resolved review runs before contacting GitHub.


func TestSubmitReview_AlreadyResolvedReturnsInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("GitHub should not be called for an already-resolved run")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	store := &mockCapabilityStore{
		reviewRunPullReq: func(_ context.Context, _, _ string) (domain.ReviewRunPullRequest, error) {
			return domain.ReviewRunPullRequest{
				ReviewRun: domain.ReviewRun{
					ReviewSessionID: "sess-1",
					Status:          contract.AOReviewRunComplete, // already done
				},
				PullRequestRepository: "owner/repo",
			}, nil
		},
	}
	b := newTestBroker(t, srv, store)
	_, err := b.SubmitReview(context.Background(), "org-1", "sess-1", "run-1", domain.SubmitReviewResult{
		Verdict: contract.AOReviewVerdictApproved,
		Body:    "LGTM",
	})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Errorf("already resolved: want ErrInvalid, got %v", err)
	}
}
