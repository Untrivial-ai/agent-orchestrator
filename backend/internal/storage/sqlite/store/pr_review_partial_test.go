package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The pr.review_observed_at / review_partial pair travels together: a writer
// that did not fetch review threads passes a zero ReviewObservedAt (NULL) and
// must keep the stored pair, while a writer that fetched passes both. This is
// what keeps a failed review fetch (observer preserve mode, claim fallback)
// from manufacturing certainty it does not have.
func TestPRReviewCompletenessPairContract(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	base := domain.PullRequest{
		URL: "https://gh/pr/9", SessionID: r.ID, Number: 9,
		Review: domain.ReviewRequired, UpdatedAt: now, StateChangedAt: now,
	}

	// A partial review observation stores (timestamp, partial=true).
	partial := base
	partial.ReviewObservedAt = now.Add(time.Minute)
	partial.ReviewPartial = true
	if err := s.WriteSCMObservation(ctx, partial, nil, nil, nil, nil, ports.ReviewWriteMerge); err != nil {
		t.Fatal(err)
	}

	// A metadata-only pass in preserve mode has no review observation to offer
	// (zero pair in the domain row); it must keep the stored certainty.
	preserve := partial
	preserve.Title = "renamed"
	preserve.UpdatedAt = now.Add(2 * time.Minute)
	preserve.ReviewObservedAt = time.Time{}
	preserve.ReviewPartial = false
	if err := s.WriteSCMObservation(ctx, preserve, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, base.URL)
	if err != nil || !ok {
		t.Fatalf("get pr after preserve write: ok=%v err=%v", ok, err)
	}
	if got.Title != "renamed" {
		t.Fatalf("metadata-only pass lost its own facts: %+v", got)
	}
	if !got.ReviewObservedAt.Equal(partial.ReviewObservedAt) || !got.ReviewPartial {
		t.Fatalf("preserve-mode write clobbered review certainty: got (%v, %t), want (%v, true)",
			got.ReviewObservedAt, got.ReviewPartial, partial.ReviewObservedAt)
	}

	// A full review observation replaces the pair.
	full := preserve
	full.ReviewObservedAt = now.Add(3 * time.Minute)
	full.ReviewPartial = false
	if err := s.WriteSCMObservation(ctx, full, nil, nil, nil, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.GetPR(ctx, base.URL)
	if err != nil || !ok {
		t.Fatalf("get pr after full write: ok=%v err=%v", ok, err)
	}
	if !got.ReviewObservedAt.Equal(full.ReviewObservedAt) || got.ReviewPartial {
		t.Fatalf("full observation did not replace the pair: got (%v, %t), want (%v, false)",
			got.ReviewObservedAt, got.ReviewPartial, full.ReviewObservedAt)
	}

	// The legacy writer keeps the same contract on insert: no review
	// observation means the zero pair, not the column's conservative default.
	legacy := base
	legacy.URL = "https://gh/pr/10"
	legacy.Number = 10
	if err := s.WritePR(ctx, legacy, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.GetPR(ctx, legacy.URL)
	if err != nil || !ok {
		t.Fatalf("get legacy pr: ok=%v err=%v", ok, err)
	}
	if !got.ReviewObservedAt.IsZero() || got.ReviewPartial {
		t.Fatalf("legacy insert without a review observation must store the zero pair, got (%v, %t)",
			got.ReviewObservedAt, got.ReviewPartial)
	}
}
