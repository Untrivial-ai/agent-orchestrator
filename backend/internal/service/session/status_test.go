package session

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var statusNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// statusRec builds a session whose agent HAS delivered a hook signal; the
// no-signal cases below zero FirstSignalAt explicitly.
func statusRec(activity domain.ActivityState, terminated bool) domain.SessionRecord {
	return domain.SessionRecord{
		Activity:      domain.Activity{State: activity, LastActivityAt: statusNow},
		FirstSignalAt: statusNow,
		IsTerminated:  terminated,
	}
}

// silentRec builds a live session that has never delivered a hook signal,
// seeded (spawned/restored) `age` before the derivation time.
func silentRec(age time.Duration) domain.SessionRecord {
	return domain.SessionRecord{
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: statusNow.Add(-age)},
	}
}

// chatRec marks a record as a Chat-mode session, whose activity reaches AO
// through its own controller rather than an agent hook pipeline.
func chatRec(rec domain.SessionRecord) domain.SessionRecord {
	rec.Mode = domain.SessionModeChat
	return rec
}

func statusPR(facts domain.PRFacts) []domain.PRFacts { return []domain.PRFacts{facts} }

func TestServiceDerivesStatusFromSessionFactsAndPR(t *testing.T) {
	tests := []struct {
		name string
		rec  domain.SessionRecord
		pr   []domain.PRFacts
		// hookless marks a harness with no activity pipeline (signalCapable
		// false): silence is its permanent normal state, never no_signal.
		hookless bool
		want     domain.SessionStatus
	}{
		{"terminated", statusRec(domain.ActivityExited, true), nil, false, domain.StatusTerminated},
		{"merged-pr", statusRec(domain.ActivityIdle, true), statusPR(domain.PRFacts{Merged: true}), false, domain.StatusMerged},
		// Termination outranks activity state entirely, unaffected by the
		// waiting_input/PR precedence fix below: a terminated session with a
		// waiting_input activity signal still reports merged, exactly as an
		// idle terminated session does.
		{"terminated-waiting-input-merged-pr-still-merged", statusRec(domain.ActivityWaitingInput, true), statusPR(domain.PRFacts{Merged: true}), false, domain.StatusMerged},
		// waiting_input with no PR to derive a status from still falls back to needs_input.
		{"needs-input", statusRec(domain.ActivityWaitingInput, false), nil, false, domain.StatusNeedsInput},
		// waiting_input with an owned PR now surfaces the PR's pipeline status
		// instead of masking it behind needs_input.
		{"waiting-input-pr-derived-ci-failed", statusRec(domain.ActivityWaitingInput, false), statusPR(domain.PRFacts{CI: domain.CIFailing}), false, domain.StatusCIFailed},
		// blocked with no PR to derive a status from still falls back to needs_input.
		{"needs-input-blocked", statusRec(domain.ActivityBlocked, false), nil, false, domain.StatusNeedsInput},
		// blocked with an owned PR now surfaces the PR's pipeline status
		// instead of masking it behind needs_input (same fix as waiting_input,
		// since both share the same activity-precedence case).
		{"blocked-pr-derived-ci-failed", statusRec(domain.ActivityBlocked, false), statusPR(domain.PRFacts{CI: domain.CIFailing}), false, domain.StatusCIFailed},
		{"exited", statusRec(domain.ActivityExited, false), statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable}), false, domain.StatusExited},
		{"ci-failed", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{CI: domain.CIFailing}), false, domain.StatusCIFailed},
		{"draft", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{Draft: true}), false, domain.StatusDraft},
		{"changes-requested", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{Review: domain.ReviewChangesRequest}), false, domain.StatusChangesRequested},
		{"mergeable", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable}), false, domain.StatusMergeable},
		{"approved", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{Review: domain.ReviewApproved}), false, domain.StatusApproved},
		{"review-pending", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{Review: domain.ReviewRequired}), false, domain.StatusReviewPending},
		{"pr-open", statusRec(domain.ActivityIdle, false), statusPR(domain.PRFacts{}), false, domain.StatusPROpen},
		{"working", statusRec(domain.ActivityActive, false), nil, false, domain.StatusWorking},
		{"idle", statusRec(domain.ActivityIdle, false), nil, false, domain.StatusIdle},

		// A live session whose hook-capable agent never signaled is no_signal
		// once the grace passes — never a confident idle.
		{"no-signal-after-grace", silentRec(noSignalGrace + time.Second), nil, false, domain.StatusNoSignal},
		// A hook-less harness can never signal: its silence stays idle forever
		// instead of degrading into a false "needs you".
		{"hookless-silent-stays-idle", silentRec(2 * noSignalGrace), nil, true, domain.StatusIdle},
		// A chat session's silence is not evidence of anything. AO owns the
		// provider connection, so a lost controller arrives as exited; an idle
		// chat session is simply waiting on the user and must not read as broken.
		{"chat-silent-stays-idle", chatRec(silentRec(4 * noSignalGrace)), nil, false, domain.StatusIdle},
		// Right after spawn the agent legitimately hasn't called back yet.
		{"silent-within-grace-is-idle", silentRec(10 * time.Second), nil, false, domain.StatusIdle},
		{"silent-at-grace-boundary-is-idle", silentRec(noSignalGrace), nil, false, domain.StatusIdle},
		// Termination and PR facts outrank the missing-signal downgrade.
		{
			"no-signal-terminated-wins",
			domain.SessionRecord{Activity: domain.Activity{State: domain.ActivityExited, LastActivityAt: statusNow.Add(-2 * noSignalGrace)}, IsTerminated: true},
			nil,
			false,
			domain.StatusTerminated,
		},
		{"no-signal-pr-wins", silentRec(2 * noSignalGrace), statusPR(domain.PRFacts{}), false, domain.StatusPROpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveStatus(tt.rec, tt.pr, statusNow, !tt.hookless); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveStatusActivityPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		activity domain.ActivityState
		prs      []domain.PRFacts
		want     domain.SessionStatus
	}{
		{"active-without-pr", domain.ActivityActive, nil, domain.StatusWorking},
		{"active-with-open-pr", domain.ActivityActive, statusPR(domain.PRFacts{}), domain.StatusWorking},
		{"active-with-draft-pr", domain.ActivityActive, statusPR(domain.PRFacts{Draft: true}), domain.StatusWorking},
		{"active-with-review-pending", domain.ActivityActive, statusPR(domain.PRFacts{Review: domain.ReviewRequired}), domain.StatusWorking},
		{"active-with-ci-failure", domain.ActivityActive, statusPR(domain.PRFacts{CI: domain.CIFailing}), domain.StatusWorking},
		{"active-with-changes-requested", domain.ActivityActive, statusPR(domain.PRFacts{Review: domain.ReviewChangesRequest}), domain.StatusWorking},
		{"active-with-approved-pr", domain.ActivityActive, statusPR(domain.PRFacts{Review: domain.ReviewApproved}), domain.StatusWorking},
		{"active-with-mergeable-pr", domain.ActivityActive, statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable}), domain.StatusWorking},
		{"active-with-merged-pr", domain.ActivityActive, statusPR(domain.PRFacts{Merged: true}), domain.StatusWorking},
		{"exited-without-pr", domain.ActivityExited, nil, domain.StatusExited},
		{"exited-with-open-pr", domain.ActivityExited, statusPR(domain.PRFacts{}), domain.StatusExited},
		{"exited-with-merged-pr", domain.ActivityExited, statusPR(domain.PRFacts{Merged: true}), domain.StatusExited},
		// waiting_input/blocked defer to the PR's pipeline status when there is
		// one to derive; they only fall back to needs_input when there isn't.
		{"waiting-with-pr", domain.ActivityWaitingInput, statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable}), domain.StatusMergeable},
		{"blocked-with-pr", domain.ActivityBlocked, statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable}), domain.StatusMergeable},
		{"waiting-without-pr", domain.ActivityWaitingInput, nil, domain.StatusNeedsInput},
		{"blocked-without-pr", domain.ActivityBlocked, nil, domain.StatusNeedsInput},
		{"waiting-with-open-pr", domain.ActivityWaitingInput, statusPR(domain.PRFacts{}), domain.StatusPROpen},
		{"waiting-with-ci-failing-pr", domain.ActivityWaitingInput, statusPR(domain.PRFacts{CI: domain.CIFailing}), domain.StatusCIFailed},
		{"blocked-with-open-pr", domain.ActivityBlocked, statusPR(domain.PRFacts{}), domain.StatusPROpen},
		{"blocked-with-ci-failing-pr", domain.ActivityBlocked, statusPR(domain.PRFacts{CI: domain.CIFailing}), domain.StatusCIFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveStatus(statusRec(tt.activity, false), tt.prs, statusNow, true); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveSCMStatusRemainsAvailableWhileAgentIsActive(t *testing.T) {
	tests := []struct {
		name string
		prs  []domain.PRFacts
		want domain.SessionStatus
	}{
		{"without-pr", nil, ""},
		{"open", statusPR(domain.PRFacts{}), domain.StatusPROpen},
		{"draft", statusPR(domain.PRFacts{Draft: true}), domain.StatusDraft},
		{"ci-failed", statusPR(domain.PRFacts{CI: domain.CIFailing}), domain.StatusCIFailed},
		{"changes-requested", statusPR(domain.PRFacts{Review: domain.ReviewChangesRequest}), domain.StatusChangesRequested},
		{"review-pending", statusPR(domain.PRFacts{Review: domain.ReviewRequired}), domain.StatusReviewPending},
		{"approved", statusPR(domain.PRFacts{Review: domain.ReviewApproved}), domain.StatusApproved},
		{"mergeable", statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable}), domain.StatusMergeable},
		{"merged", statusPR(domain.PRFacts{Merged: true}), domain.StatusMerged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveSCMStatus(tt.prs); got != tt.want {
				t.Fatalf("deriveSCMStatus() = %q, want %q", got, tt.want)
			}
			if tt.want != "" {
				if got := deriveStatus(statusRec(domain.ActivityActive, false), tt.prs, statusNow, true); got != domain.StatusWorking {
					t.Fatalf("deriveStatus(active) = %q, want working", got)
				}
			}
		})
	}
}

// A blocked stacked child cannot merge until its parent does, so its readiness
// signals are suppressed, but its problem signals (failing CI, draft,
// requested-changes/unresolved-comments) must still surface for the session.
func TestAggregateStackedChildSignals(t *testing.T) {
	parent := domain.PRFacts{URL: "parent", SourceBranch: "feat", Mergeability: domain.MergeMergeable}
	child := func(f domain.PRFacts) domain.PRFacts {
		f.URL = "child"
		f.SourceBranch = "feat/child"
		f.TargetBranch = "feat"
		return f
	}
	tests := []struct {
		name string
		prs  []domain.PRFacts
		want domain.SessionStatus
	}{
		{"blocked-child-ci-failing-surfaces", []domain.PRFacts{parent, child(domain.PRFacts{CI: domain.CIFailing})}, domain.StatusCIFailed},
		{"blocked-child-draft-surfaces", []domain.PRFacts{parent, child(domain.PRFacts{Draft: true})}, domain.StatusDraft},
		{"blocked-child-changes-requested-surfaces", []domain.PRFacts{parent, child(domain.PRFacts{Review: domain.ReviewChangesRequest})}, domain.StatusChangesRequested},
		{"blocked-child-unresolved-comments-surfaces", []domain.PRFacts{parent, child(domain.PRFacts{ReviewComments: true})}, domain.StatusChangesRequested},
		// A blocked child's readiness signals stay hidden: only the parent's
		// mergeable state drives the session.
		{"blocked-child-mergeable-suppressed", []domain.PRFacts{parent, child(domain.PRFacts{Mergeability: domain.MergeMergeable})}, domain.StatusMergeable},
		{"blocked-child-approved-suppressed", []domain.PRFacts{parent, child(domain.PRFacts{Review: domain.ReviewApproved})}, domain.StatusMergeable},
		// Degenerate set where every open PR is blocked and none is actionable:
		// fall back to the raw aggregate so the session never goes dark.
		{
			"all-blocked-no-actionable-falls-back",
			[]domain.PRFacts{
				{URL: "a", SourceBranch: "feat/a", TargetBranch: "feat/b", Mergeability: domain.MergeMergeable},
				{URL: "b", SourceBranch: "feat/b", TargetBranch: "feat/a", Mergeability: domain.MergeMergeable},
			},
			domain.StatusMergeable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveStatus(statusRec(domain.ActivityIdle, false), tt.prs, statusNow, true); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

// Without an injected capability predicate the service must never claim
// no_signal; with one, capability follows the predicate per harness.
func TestHarnessSignalsCapabilityGate(t *testing.T) {
	if (&Service{}).harnessSignals(domain.HarnessCodex) {
		t.Fatal("zero-value Service reports signal-capable; want incapable (never no_signal)")
	}
	s := NewWithDeps(Deps{SignalCapable: func(h domain.AgentHarness) bool { return h == domain.HarnessCodex }})
	if !s.harnessSignals(domain.HarnessCodex) {
		t.Fatal("harnessSignals(codex) = false with codex-capable predicate")
	}
	if s.harnessSignals(domain.HarnessAmp) {
		t.Fatal("harnessSignals(amp) = true with codex-only predicate")
	}
}
