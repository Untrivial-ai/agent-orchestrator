package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// newCompleteOAuthGitHubServer fakes the GitHub endpoints repository sync
// touches. failList makes the repository enumeration
// return 500 so a test can drive the sync-failure path. Any unexpected request
// fails the test, keeping the call graph honest.
func newCompleteOAuthGitHubServer(t *testing.T, failList bool) *httptest.Server {
	t.Helper()
	encode := func(w http.ResponseWriter, status int, body map[string]any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1234/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s for access token mint", r.Method)
		}
		encode(w, http.StatusCreated, map[string]any{
			"token":      "ghs_testinstallationtoken",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		if failList {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		repository := func(id int64, name string) map[string]any {
			return map[string]any{
				"id": id,
				"owner": map[string]any{
					"id": 77, "login": "octo", "type": "User",
				},
				"name":           name,
				"full_name":      "octo/" + name,
				"html_url":       "https://github.com/octo/" + name,
				"clone_url":      "https://github.com/octo/" + name + ".git",
				"ssh_url":        "git@github.com:octo/" + name + ".git",
				"default_branch": "main",
				"visibility":     "private",
				"private":        true,
				"archived":       false,
				"disabled":       false,
				"updated_at":     "2026-01-01T00:00:00Z",
			}
		}
		encode(w, http.StatusOK, map[string]any{"repositories": []map[string]any{
			repository(42, "app"),
			repository(43, "lib"),
		}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

// completeOAuthSyncStore overrides only the methods SyncInstallation reaches;
// every other Store method stays nil and would panic if reached.
type completeOAuthSyncStore struct {
	Store

	beginCalls           int
	reconcileCalls       int
	markFailureCalls     int
	reconcileConflicts   int
	reconciled           []domain.GitHubRepository
	reconciledOrgID      string
	reconciledGeneration int64
}

func (s *completeOAuthSyncStore) GitHubInstallationForSync(
	context.Context, domain.Principal, string, string,
) (domain.GitHubInstallation, error) {
	return domain.GitHubInstallation{
		ID:                   "inst-1",
		OrgID:                "org-1",
		GitHubInstallationID: 1234,
		InstalledByUserID:    "user-1",
		Status:               "active",
		SyncStatus:           "pending",
		RepositorySelection:  "all",
	}, nil
}

func (s *completeOAuthSyncStore) BeginGitHubRepositorySync(
	context.Context, domain.GitHubInstallation,
) (int64, error) {
	s.beginCalls++
	return 7, nil
}

func (s *completeOAuthSyncStore) ReconcileGitHubRepositories(
	_ context.Context, orgID string, _ domain.GitHubInstallation,
	generation int64, repositories []domain.GitHubRepository,
) error {
	s.reconcileCalls++
	s.reconciledOrgID = orgID
	s.reconciledGeneration = generation
	if s.reconcileConflicts > 0 {
		// A concurrent trigger bumped sync_generation after this sync began, so
		// the store rejects the stale generation with ErrConflict.
		s.reconcileConflicts--
		return postgres.ErrConflict
	}
	s.reconciled = repositories
	return nil
}

func (s *completeOAuthSyncStore) MarkGitHubSyncFailure(
	context.Context, string, domain.GitHubInstallation, int64, string,
) error {
	s.markFailureCalls++
	return nil
}

func newCompleteOAuthTestService(t *testing.T, store Store, client *Client) *Service {
	t.Helper()
	svc, err := NewService(
		store, client,
		make([]byte, 32), make([]byte, 32),
		"webhook-secret", time.Hour,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// An explicit sync must populate repository grants before it reports ready.
func TestSyncInstallationReconcilesRepositories(t *testing.T) {
	server := newCompleteOAuthGitHubServer(t, false)
	defer server.Close()
	store := &completeOAuthSyncStore{}
	client := newExchangeTestClient(t, server.URL)
	service := newCompleteOAuthTestService(t, store, client)

	installation, err := service.SyncInstallation(context.Background(), domain.Principal{UserID: "user-1"}, "org-1", "inst-1")
	if err != nil {
		t.Fatalf("SyncInstallation: %v", err)
	}
	if store.beginCalls != 1 || store.reconcileCalls != 1 {
		t.Fatalf(
			"sync calls = (begin %d, reconcile %d), want (1, 1)",
			store.beginCalls, store.reconcileCalls,
		)
	}
	if store.markFailureCalls != 0 {
		t.Fatalf("MarkGitHubSyncFailure called %d times, want 0", store.markFailureCalls)
	}
	if store.reconciledOrgID != "org-1" || store.reconciledGeneration != 7 {
		t.Fatalf(
			"reconciled for org %s generation %d, want org-1 generation 7",
			store.reconciledOrgID, store.reconciledGeneration,
		)
	}
	fullNames := make([]string, 0, len(store.reconciled))
	for _, repository := range store.reconciled {
		fullNames = append(fullNames, repository.FullName)
	}
	if len(fullNames) != 2 || fullNames[0] != "octo/app" || fullNames[1] != "octo/lib" {
		t.Fatalf("reconciled repositories = %v, want [octo/app octo/lib]", fullNames)
	}
	if installation.SyncStatus != "ready" {
		t.Fatalf("returned SyncStatus = %q, want ready", installation.SyncStatus)
	}
	if installation.LastSyncedAt == nil {
		t.Fatal("returned LastSyncedAt is nil after a successful completion sync")
	}
}

// A transient GitHub failure is recorded so the durable worker can retry it.
func TestSyncInstallationRecordsSyncFailure(t *testing.T) {
	server := newCompleteOAuthGitHubServer(t, true)
	defer server.Close()
	store := &completeOAuthSyncStore{}
	client := newExchangeTestClient(t, server.URL)
	service := newCompleteOAuthTestService(t, store, client)

	_, err := service.SyncInstallation(context.Background(), domain.Principal{UserID: "user-1"}, "org-1", "inst-1")
	if err == nil {
		t.Fatal("SyncInstallation with failing sync returned nil error")
	}
	if store.beginCalls != 1 {
		t.Fatalf("BeginGitHubRepositorySync called %d times, want 1", store.beginCalls)
	}
	if store.reconcileCalls != 0 {
		t.Fatalf("ReconcileGitHubRepositories called %d times, want 0", store.reconcileCalls)
	}
	if store.markFailureCalls != 1 {
		t.Fatalf("MarkGitHubSyncFailure called %d times, want 1", store.markFailureCalls)
	}
}

// Two sync triggers can race on sync_generation. The loser's Reconcile sees a
// superseded generation, so sync must re-run with a fresh generation.
func TestSyncInstallationRetriesSyncAfterConflict(t *testing.T) {
	server := newCompleteOAuthGitHubServer(t, false)
	defer server.Close()
	store := &completeOAuthSyncStore{
		reconcileConflicts: 1,
	}
	client := newExchangeTestClient(t, server.URL)
	service := newCompleteOAuthTestService(t, store, client)

	installation, err := service.SyncInstallation(context.Background(), domain.Principal{UserID: "user-1"}, "org-1", "inst-1")
	if err != nil {
		t.Fatalf("SyncInstallation with a conflicted first sync: %v", err)
	}
	if store.beginCalls != 2 || store.reconcileCalls != 2 {
		t.Fatalf(
			"sync attempts = (begin %d, reconcile %d), want (2, 2): the conflicted attempt must retry",
			store.beginCalls, store.reconcileCalls,
		)
	}
	if len(store.reconciled) != 2 {
		t.Fatalf("reconciled repositories = %d, want 2 after the retry", len(store.reconciled))
	}
	if installation.SyncStatus != "ready" {
		t.Fatalf("returned SyncStatus = %q, want ready", installation.SyncStatus)
	}
}

// The conflict retry is bounded: a sync that keeps losing gives up instead of
// ping-ponging with the other trigger forever.
func TestSyncInstallationBoundsSyncConflictRetries(t *testing.T) {
	server := newCompleteOAuthGitHubServer(t, false)
	defer server.Close()
	store := &completeOAuthSyncStore{
		reconcileConflicts: 3,
	}
	client := newExchangeTestClient(t, server.URL)
	service := newCompleteOAuthTestService(t, store, client)

	_, err := service.SyncInstallation(context.Background(), domain.Principal{UserID: "user-1"}, "org-1", "inst-1")
	if !errors.Is(err, postgres.ErrConflict) {
		t.Fatalf("SyncInstallation error = %v, want ErrConflict", err)
	}
	if store.beginCalls != 3 || store.reconcileCalls != 3 {
		t.Fatalf(
			"sync attempts = (begin %d, reconcile %d), want (3, 3): retries must be bounded at three attempts",
			store.beginCalls, store.reconcileCalls,
		)
	}
}
