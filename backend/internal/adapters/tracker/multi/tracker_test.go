package multi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeTracker is a minimal ports.Tracker implementation for testing dispatch
// routing. It records the IDs/repos it receives and returns canned results.
type fakeTracker struct {
	issue      domain.Issue
	getErr     error
	listErr    error
	preflight  error
	getCalls   []domain.TrackerID
	listCalls  []domain.TrackerRepo
	prefCalled bool
}

func (f *fakeTracker) Get(_ context.Context, id domain.TrackerID) (domain.Issue, error) {
	f.getCalls = append(f.getCalls, id)
	if f.getErr != nil {
		return domain.Issue{}, f.getErr
	}
	return f.issue, nil
}

func (f *fakeTracker) List(_ context.Context, repo domain.TrackerRepo, _ domain.ListFilter) ([]domain.Issue, error) {
	f.listCalls = append(f.listCalls, repo)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []domain.Issue{f.issue}, nil
}

func (f *fakeTracker) Preflight(_ context.Context) error {
	f.prefCalled = true
	return f.preflight
}

func ghIssue() domain.Issue {
	return domain.Issue{
		ID:    domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/repo#1"},
		Title: "gh issue",
	}
}

func glIssue() domain.Issue {
	return domain.Issue{
		ID:    domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "acme/repo#2"},
		Title: "gl issue",
	}
}

// ---------------------------------------------------------------------------
// Dispatch routing
// ---------------------------------------------------------------------------

func TestGet_RoutesByProvider(t *testing.T) {
	gh := &fakeTracker{issue: ghIssue()}
	gl := &fakeTracker{issue: glIssue()}
	m := New(
		NamedTracker{Key: "github", Tracker: gh},
		NamedTracker{Key: "gitlab", Tracker: gl},
	)

	// GitHub dispatch
	got, err := m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/repo#1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "gh issue" {
		t.Errorf("Get(github) = %q, want %q", got.Title, "gh issue")
	}
	if len(gh.getCalls) != 1 || len(gl.getCalls) != 0 {
		t.Errorf("github called %d times, gitlab called %d times; want 1/0", len(gh.getCalls), len(gl.getCalls))
	}

	// GitLab dispatch
	got, err = m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "acme/repo#2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "gl issue" {
		t.Errorf("Get(gitlab) = %q, want %q", got.Title, "gl issue")
	}
	if len(gh.getCalls) != 1 || len(gl.getCalls) != 1 {
		t.Errorf("github called %d times, gitlab called %d times; want 1/1", len(gh.getCalls), len(gl.getCalls))
	}
}

func TestList_RoutesByProvider(t *testing.T) {
	gh := &fakeTracker{issue: ghIssue()}
	gl := &fakeTracker{issue: glIssue()}
	m := New(
		NamedTracker{Key: "github", Tracker: gh},
		NamedTracker{Key: "gitlab", Tracker: gl},
	)

	_, err := m.List(context.Background(),
		domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "acme/repo"},
		domain.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.listCalls) != 0 || len(gl.listCalls) != 1 {
		t.Fatalf("github called %d times, gitlab called %d times; want 0/1", len(gh.listCalls), len(gl.listCalls))
	}

	_, err = m.List(context.Background(),
		domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "acme/repo"},
		domain.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.listCalls) != 1 || len(gl.listCalls) != 1 {
		t.Fatalf("github called %d times, gitlab called %d times; want 1/1", len(gh.listCalls), len(gl.listCalls))
	}
}

// ---------------------------------------------------------------------------
// Unknown provider
// ---------------------------------------------------------------------------

func TestGet_UnknownProviderReturnsError(t *testing.T) {
	m := New(NamedTracker{Key: "github", Tracker: &fakeTracker{}})

	_, err := m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "acme/repo#1"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownProvider)", err)
	}
}

func TestList_UnknownProviderReturnsError(t *testing.T) {
	m := New(NamedTracker{Key: "gitlab", Tracker: &fakeTracker{}})

	_, err := m.List(context.Background(),
		domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "acme/repo"},
		domain.ListFilter{})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownProvider)", err)
	}
}

// ---------------------------------------------------------------------------
// Single-tracker fallback (degrade gracefully)
// ---------------------------------------------------------------------------

func TestSingleTracker_OnlyRegisteredProviderServes(t *testing.T) {
	gl := &fakeTracker{issue: glIssue()}
	m := New(NamedTracker{Key: "gitlab", Tracker: gl})

	// GitLab dispatch works
	got, err := m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "acme/repo#2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "gl issue" {
		t.Errorf("Get(gitlab) = %q, want %q", got.Title, "gl issue")
	}

	// GitHub dispatch fails clearly — the unregistered provider is not nil-dereferenced
	_, err = m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/repo#1"})
	if err == nil {
		t.Fatal("expected error for unregistered github provider")
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownProvider)", err)
	}
}

func TestSingleTracker_ListOnlyRegisteredProviderServes(t *testing.T) {
	gh := &fakeTracker{issue: ghIssue()}
	m := New(NamedTracker{Key: "github", Tracker: gh})

	issues, err := m.List(context.Background(),
		domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "acme/repo"},
		domain.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}

	_, err = m.List(context.Background(),
		domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "acme/repo"},
		domain.ListFilter{})
	if err == nil {
		t.Fatal("expected error for unregistered gitlab provider")
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownProvider)", err)
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

func TestPreflight_ProbesAllTrackers(t *testing.T) {
	gh := &fakeTracker{}
	gl := &fakeTracker{}
	m := New(
		NamedTracker{Key: "github", Tracker: gh},
		NamedTracker{Key: "gitlab", Tracker: gl},
	)

	if err := m.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !gh.prefCalled || !gl.prefCalled {
		t.Errorf("preflight must probe all trackers: gh=%v gl=%v", gh.prefCalled, gl.prefCalled)
	}
}

func TestPreflight_ReturnsFirstError(t *testing.T) {
	preflightErr := errors.New("gh preflight failed")
	gh := &fakeTracker{preflight: preflightErr}
	gl := &fakeTracker{}
	m := New(
		NamedTracker{Key: "github", Tracker: gh},
		NamedTracker{Key: "gitlab", Tracker: gl},
	)

	err := m.Preflight(context.Background())
	if err == nil {
		t.Fatal("expected error from preflight")
	}
	if !errors.Is(err, preflightErr) {
		t.Errorf("err = %v, want %v", err, preflightErr)
	}
}

func TestPreflight_NoTrackersReturnsNil(t *testing.T) {
	m := New()
	if err := m.Preflight(context.Background()); err != nil {
		t.Errorf("Preflight on empty multi: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error propagation from sub-trackers
// ---------------------------------------------------------------------------

func TestGet_PropagatesSubTrackerError(t *testing.T) {
	customErr := errors.New("api down")
	gh := &fakeTracker{getErr: customErr}
	m := New(NamedTracker{Key: "github", Tracker: gh})

	_, err := m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/repo#1"})
	if !errors.Is(err, customErr) {
		t.Errorf("err = %v, want %v", err, customErr)
	}
}

func TestList_PropagatesSubTrackerError(t *testing.T) {
	customErr := errors.New("api down")
	gl := &fakeTracker{listErr: customErr}
	m := New(NamedTracker{Key: "gitlab", Tracker: gl})

	_, err := m.List(context.Background(),
		domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "acme/repo"},
		domain.ListFilter{})
	if !errors.Is(err, customErr) {
		t.Errorf("err = %v, want %v", err, customErr)
	}
}

// Ensure the error message is descriptive (not just the sentinel alone).
func TestErrUnknownProvider_MessageIsDescriptive(t *testing.T) {
	m := New(NamedTracker{Key: "github", Tracker: &fakeTracker{}})

	_, err := m.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "acme/repo#1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("error message %q should contain the provider name", err.Error())
	}
}
