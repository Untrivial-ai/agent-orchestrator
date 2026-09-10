package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	scmobserver "github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Only cheap discovery/review inputs are fixed. Check fetching and projection,
// observer persistence, lifecycle delivery, and session reads use real code.
type billingPipelineProvider struct{ *Provider }

func (*billingPipelineProvider) RepoPRListGuard(context.Context, ports.SCMRepo, string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{}, nil
}

func (*billingPipelineProvider) CommitChecksGuard(context.Context, ports.SCMRepo, string, string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{}, nil
}

func (*billingPipelineProvider) ListPRsByRepo(context.Context, ports.SCMRepo, time.Time) ([]ports.SCMPRObservation, error) {
	return nil, nil
}

func (*billingPipelineProvider) FetchReviewThreads(context.Context, ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	return ports.SCMReviewObservation{Decision: "approved"}, nil
}

type billingPipelineMessenger struct{ messages []string }

func (m *billingPipelineMessenger) Send(_ context.Context, _ domain.SessionID, message string) error {
	m.messages = append(m.messages, message)
	return nil
}

func TestBillingBlockedThroughPersistenceAndSessionRead(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	if err := store.UpsertProject(ctx(), domain.ProjectRecord{ID: "p", Path: t.TempDir(), RepoOriginURL: "https://github.com/octocat/hello.git", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	session, err := store.CreateSession(ctx(), domain.SessionRecord{
		ProjectID: "p", Kind: domain.KindWorker, Harness: domain.HarnessFake,
		Metadata:     domain.SessionMetadata{Branch: "feat/x"},
		Activity:     domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		AutoInjectCI: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	const prURL = "https://github.com/octocat/hello/pull/42"
	if err := store.WriteSCMObservation(ctx(), domain.PullRequest{
		URL: prURL, Number: 42, SessionID: session.ID, Provider: "github", Host: "github.com", Repo: "octocat/hello", SourceBranch: "feat/x", HeadSHA: "deadbeef", CI: domain.CIPassing, UpdatedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}

	fake := newFakeGH(t)
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"pr0": map[string]any{"pullRequest": checkFixture(billingBlockedCheck())}}})
	})
	provider := &billingPipelineProvider{newProviderForTest(t, fake)}
	for attempt := 0; attempt < 2; attempt++ {
		// New observer and lifecycle instances simulate a daemon restart while
		// the durable billing evidence remains in the same SQLite store.
		messenger := &billingPipelineMessenger{}
		observer := scmobserver.New(provider, store, lifecycle.New(store, messenger), scmobserver.Config{})
		if err := observer.Poll(ctx()); err != nil {
			t.Fatal(err)
		}
		checks, err := store.ListChecks(ctx(), prURL)
		if err != nil {
			t.Fatal(err)
		}
		if len(checks) != 1 || checks[0].Status != domain.PRCheckUnknown || checks[0].Conclusion != "failure" || checks[0].LogTail != domain.CIBillingBlockedReason {
			t.Fatalf("stored checks lost billing evidence: %#v", checks)
		}
		service := sessionsvc.New(nil, store)
		read, err := service.Get(ctx(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if read.Status == domain.StatusCIFailed || read.SCMStatus == domain.StatusCIFailed || len(read.PRs) != 1 || read.PRs[0].CI != domain.CIUnknown {
			t.Fatalf("billing became a failed session: %#v", read)
		}
		summaries, err := service.ListPRSummaries(ctx(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(summaries) != 1 || len(summaries[0].CI.BlockedChecks) != 1 || summaries[0].CI.BlockedChecks[0].Reason != domain.CIBillingBlockedReason {
			t.Fatalf("blocking reason not available to user: %#v", summaries)
		}
		if len(messenger.messages) != 0 {
			t.Fatalf("billing triggered repair: %#v", messenger.messages)
		}
	}
}
