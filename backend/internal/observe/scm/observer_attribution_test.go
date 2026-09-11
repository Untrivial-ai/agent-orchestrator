package scm

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestPoll_AttributionRejectsAmbiguousSessions(t *testing.T) {
	for _, tt := range []struct {
		name     string
		branches []string
		want     domain.SessionID
	}{
		{"exact tie", []string{"feat/child", "feat/child"}, ""},
		{"namespace tie", []string{"feat", "feat/root"}, ""},
		{"longest prefix", []string{"feat", "feat/root", "feat/child/root"}, "p-3"},
		{"exact wins", []string{"feat", "feat/root", "feat/child"}, "p-3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				store := testStoreWithSession()
				store.sessions = nil
				for i, branch := range tt.branches {
					store.sessions = append(store.sessions, domain.SessionRecord{
						ID:        domain.SessionID([]string{"p-1", "p-2", "p-3"}[i]),
						ProjectID: "p", Metadata: domain.SessionMetadata{Branch: branch},
					})
				}
				if reverse {
					slices.Reverse(store.sessions)
				}
				pr := testObs(1)
				pr.PR.SourceBranch, pr.PR.HeadRepo, pr.PR.Author = "feat/child", "o/r", "alice"
				provider := &fakeProvider{
					openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {pr.PR}},
					observations: map[string]ports.SCMObservation{prKey(testRepo, 1): pr},
				}
				lc := &fakeLifecycle{}
				if err := newTestObserver(store, provider, lc, time.Now()).Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				if tt.want == "" {
					if len(store.writes) != 0 || len(lc.observed) != 0 {
						t.Fatalf("ambiguous PR was attached: writes=%+v lifecycle=%+v", store.writes, lc.observed)
					}
				} else {
					if len(store.writes) == 0 {
						t.Fatal("unambiguous PR was not attached")
					}
					for _, write := range store.writes {
						if write.pr.SessionID != tt.want {
							t.Fatalf("owner=%s, want %s", write.pr.SessionID, tt.want)
						}
					}
				}
			}
		})
	}
}

func TestMatchSession_DuplicateCandidateIsNotAmbiguous(t *testing.T) {
	for _, branch := range []string{"feat/child", "feat", "feat/root"} {
		sr := sessionRepo{session: domain.SessionRecord{ID: "p-1"}, branch: branch}
		if got, ok := matchSession([]sessionRepo{sr, sr}, "feat/child"); !ok || got.session.ID != sr.session.ID {
			t.Fatalf("duplicate candidate for %s rejected: %+v, %v", branch, got, ok)
		}
	}
}

// Model real conditional requests: an acknowledged ETag returns 304, and an
// incremental cursor excludes PRs last updated before it.
type discoveryRecoveryProvider struct {
	*fakeProvider
	listedAfter []time.Time
	guardETags  []string
}

func (p *discoveryRecoveryProvider) RepoPRListGuard(_ context.Context, _ ports.SCMRepo, etag string) (ports.SCMGuardResult, error) {
	p.guardETags = append(p.guardETags, etag)
	return ports.SCMGuardResult{ETag: "v2", NotModified: etag == "v2"}, nil
}

func (p *discoveryRecoveryProvider) ListPRsByRepo(ctx context.Context, repo ports.SCMRepo, after time.Time) ([]ports.SCMPRObservation, error) {
	p.listedAfter = append(p.listedAfter, after)
	prs, err := p.fakeProvider.ListPRsByRepo(ctx, repo, after)
	var out []ports.SCMPRObservation
	for _, pr := range prs {
		if after.IsZero() || !pr.UpdatedAtProvider.Before(after) {
			out = append(out, pr)
		}
	}
	return out, err
}

func TestPoll_IdentityRecoveryRetriesMissedDiscoveries(t *testing.T) {
	for _, initialETag := range []string{"", "v1", "v2"} {
		t.Run("initial ETag="+initialETag, func(t *testing.T) {
			store := testStoreWithSession()
			store.prs["p-1"] = []domain.PullRequest{knownPR(1)}
			now := time.Unix(1_800_000_000, 0).UTC()
			existing := testObs(1)
			existing.PR.Merged, existing.PR.State = true, "merged"
			missed := testObs(2)
			missed.PR.Author, missed.PR.HeadRepo = "alice", "o/r"
			missed.PR.UpdatedAtProvider = now.Add(-2 * time.Hour)
			provider := &discoveryRecoveryProvider{fakeProvider: &fakeProvider{
				identityErr:  errors.New("identity temporarily unavailable"),
				openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {missed.PR}},
				observations: map[string]ports.SCMObservation{prKey(testRepo, 1): existing, prKey(testRepo, 2): missed},
			}}
			obs := New(provider, store, &fakeLifecycle{}, Config{Logger: quietSlog(), Clock: func() time.Time { return now }, ScopedIdentityResolver: provider})
			key := prKey(testRepo, 0)
			if initialETag != "" {
				obs.cacheSetString(obs.Cache.RepoPRListETag, &obs.Cache.repoOrder, key, initialETag)
				obs.Cache.LastSyncCursor[key] = now.Add(-time.Hour)
			}
			if err := obs.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(store.writes) == 0 || !store.writes[0].pr.Merged {
				t.Fatal("existing PR did not refresh during identity outage")
			}
			if obs.Cache.RepoPRListETag[key] != "" || !obs.Cache.LastSyncCursor[key].IsZero() {
				t.Fatal("successful existing PR refresh acknowledged incomplete discovery")
			}
			now = now.Add(time.Hour) // beyond the five-minute overlap
			provider.identityErr = nil
			provider.identity = ports.SCMIdentity{Login: "alice", Human: true}
			if err := obs.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(provider.listedAfter) != 1 || !provider.listedAfter[0].IsZero() || provider.guardETags[1] != "" {
				t.Fatalf("recovery did not retry full listing: cursors=%v etags=%v", provider.listedAfter, provider.guardETags)
			}
			found := false
			for _, write := range store.writes {
				if write.pr.Number == 2 {
					found = true
				}
			}
			if !found {
				t.Fatal("missed PR was not discovered after identity recovered")
			}
			if obs.Cache.RepoPRListETag[key] != "v2" || !obs.Cache.LastSyncCursor[key].Equal(now) {
				t.Fatal("successful recovery did not advance discovery cache")
			}
		})
	}
}

func TestPoll_ExistingAndExplicitAttachmentsStayStable(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: t.TempDir(), RepoOriginURL: "https://github.com/o/r.git", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "p", Kind: domain.KindWorker, Harness: domain.HarnessFake,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{Branch: "feat/root", WorkspacePath: t.TempDir()},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An older association no longer matches the owner's namespace. A second
	// session now exactly matches both branches; discovery must not take over.
	other := owner
	other.ID = ""
	other.Metadata.Branch = "renamed"
	other, err = store.CreateSession(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	pr := domain.PullRequest{URL: "https://github.com/o/r/pull/1", SessionID: owner.ID, Number: 1, Provider: "github", Host: "github.com", Repo: "o/r", SourceBranch: "feat/child", Author: "former-account", UpdatedAt: now}
	if err := store.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	pr.URL, pr.Number = "https://github.com/o/r/pull/2", 2
	pr.Author = "someone-else"
	if _, err := store.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, false); err != nil {
		t.Fatal(err)
	}
	for i, identity := range []ports.SCMIdentity{{Login: "original", Human: true}, {Login: "new-account", Human: true}, {}, {Login: "bot", Human: false}} {
		now = now.Add(time.Hour)
		provider := &fakeProvider{identity: identity, observations: map[string]ports.SCMObservation{}, openPRs: map[string][]ports.SCMPRObservation{}}
		for _, num := range []int{1, 2} {
			observed := testObs(num)
			observed.PR.SourceBranch, observed.PR.HeadRepo = "renamed", "o/r"
			observed.PR.Author = "new-account"
			observed.PR.Title = now.String()
			observed.Review.Decision = "approved"
			if i == 3 {
				observed.PR.Merged, observed.PR.State = true, "merged"
			}
			provider.observations[prKey(testRepo, num)] = observed
			if !observed.PR.Merged {
				provider.openPRs[prKey(testRepo, 0)] = append(provider.openPRs[prKey(testRepo, 0)], observed.PR)
			}
		}
		// Recreate the observer to prove ownership is durable, not cache state.
		obs := New(provider, store, nil, Config{Logger: quietSlog(), Clock: func() time.Time { return now }, ScopedIdentityResolver: provider})
		if err := obs.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := store.ListPRsBySession(ctx, owner.ID)
		if err != nil || len(got) != 2 {
			t.Fatalf("attachments changed: %+v, %v", got, err)
		}
		for _, attached := range got {
			if attached.SourceBranch != "renamed" || attached.CI != domain.CIPassing || attached.Merged != (i == 3) || attached.Title != now.String() || attached.Review != domain.ReviewApproved {
				t.Fatalf("existing attachment failed to refresh: %+v", attached)
			}
		}
		if got, err := store.ListPRsBySession(ctx, other.ID); err != nil || len(got) != 0 {
			t.Fatalf("attachment reassigned to matching session: %+v, %v", got, err)
		}
	}
}
