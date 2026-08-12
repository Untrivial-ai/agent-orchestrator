package scm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Regression for #3259: the first startup poll must remove a persisted
// namespace-only association created by the old branch-based behavior, while a
// deliberate foreign-author claim remains attached and explicit after refresh.
func TestPoll_ReconcilesLegacyForeignAttributionButPreservesExplicitClaim(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	projectID := domain.ProjectID("reconcile")
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:            string(projectID),
		Path:          t.TempDir(),
		RepoOriginURL: "https://github.com/o/r.git",
		RegisteredAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: projectID,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessFake,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			Branch:        "ao/reconcile-1/root",
			WorkspacePath: t.TempDir(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	legacyURL := "https://github.com/o/r/pull/1"
	legacy := domain.PullRequest{
		URL:              legacyURL,
		SessionID:        session.ID,
		Number:           1,
		Provider:         "github",
		Host:             "github.com",
		Repo:             "o/r",
		SourceBranch:     "ao/reconcile-1/foreign",
		TargetBranch:     "main",
		HeadSHA:          "sha1",
		Author:           "other",
		UpdatedAt:        now,
		AttachmentSource: domain.PRAttachmentLegacy,
	}
	if err := store.WriteSCMObservation(ctx, legacy, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}

	explicitURL := "https://github.com/o/r/pull/2"
	explicit := legacy
	explicit.URL = explicitURL
	explicit.Number = 2
	explicit.SourceBranch = "ao/reconcile-1/claimed"
	explicit.HeadSHA = "sha2"
	if _, err := store.ClaimPR(ctx, explicit, nil, nil, nil, nil, ports.ReviewWritePreserve, true); err != nil {
		t.Fatal(err)
	}

	explicitObs := testObs(2)
	explicitObs.PR.SourceBranch = explicit.SourceBranch
	explicitObs.PR.HeadSHA = explicit.HeadSHA
	explicitObs.PR.Author = "other"
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {}},
		openPRs:    map[string][]ports.SCMPRObservation{},
		observations: map[string]ports.SCMObservation{
			prKey(testRepo, 2): explicitObs,
		},
		identity: ports.SCMIdentity{Login: "alice", Human: true},
	}
	observer := New(provider, store, nil, Config{
		Clock:            func() time.Time { return now },
		Tick:             time.Hour,
		Logger:           quietSlog(),
		IdentityResolver: provider,
	})
	if err := observer.Poll(ctx); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := store.GetPR(ctx, legacyURL); err != nil || ok {
		t.Fatalf("legacy foreign attribution still attached: ok=%v err=%v", ok, err)
	}
	claimed, ok, err := store.GetPR(ctx, explicitURL)
	if err != nil || !ok {
		t.Fatalf("explicit foreign claim missing: ok=%v err=%v", ok, err)
	}
	if claimed.AttachmentSource != domain.PRAttachmentExplicit {
		t.Fatalf("explicit claim provenance after refresh = %q", claimed.AttachmentSource)
	}

	events, err := store.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var detachedInvalidation bool
	for _, event := range events {
		if event.Type == cdc.EventSessionUpdated && strings.Contains(string(event.Payload), `"pr":"`+legacyURL+`"`) {
			detachedInvalidation = true
		}
	}
	if !detachedInvalidation {
		t.Fatalf("missing session invalidation for detached PR: %+v", events)
	}
}

func TestMatchesNamespaceOnly(t *testing.T) {
	if !matchesNamespaceOnly("ao/project-1/root", "ao/project-1/child") {
		t.Fatal("root namespace child should match")
	}
	if matchesNamespaceOnly("ao/project-1/root", "ao/project-1/root") {
		t.Fatal("exact branch must not be treated as namespace-only")
	}
}
