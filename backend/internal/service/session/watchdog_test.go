package session

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// wdRecord builds a minimal session record for derivation tests. activityAt is
// the last progress; created defaults to the same instant when zero.
func wdRecord(state domain.ActivityState, activityAt time.Time, terminated bool) domain.SessionRecord {
	created := activityAt
	if created.IsZero() {
		created = activityAt
	}
	return domain.SessionRecord{
		ID:           "wd-session",
		Kind:         domain.KindWorker,
		IsTerminated: terminated,
		Activity:     domain.Activity{State: state, LastActivityAt: activityAt},
		CreatedAt:    created,
		UpdatedAt:    created,
	}
}

func wdErr(at time.Time, message string) domain.WorkerErrorEvent {
	return domain.WorkerErrorEvent{
		SessionID:  "wd-session",
		Source:     domain.WorkerErrorSourceChatTurn,
		TurnID:     "turn-1",
		Message:    message,
		OccurredAt: at,
	}
}

// wdSwitch builds a minimal active agent-switch saga for derivation tests.
func wdSwitch(state domain.AgentSwitchState, code domain.AgentSwitchErrorCode, updatedAt time.Time) *domain.AgentSwitch {
	return &domain.AgentSwitch{
		ID:            "switch-1",
		SessionID:     "wd-session",
		FromHarness:   domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex,
		State:         state,
		ErrorCode:     code,
		RequestedAt:   updatedAt,
		UpdatedAt:     updatedAt,
	}
}

func TestDeriveWatchdogHappyPathNoAttention(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	tests := []struct {
		name  string
		state domain.ActivityState
	}{
		{"active with recent progress", domain.ActivityActive},
		{"idle with recent progress", domain.ActivityIdle},
		{"waiting input under threshold", domain.ActivityWaitingInput},
		{"blocked under threshold", domain.ActivityBlocked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := deriveWatchdog(wdRecord(tc.state, now.Add(-time.Minute), false), cfg, nil, nil, now)
			if v.needsAttention {
				t.Fatalf("needsAttention = true (%s), want false", v.detail)
			}
		})
	}
}

func TestDeriveWatchdogTerminatedNeverNeedsAttention(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	rec := wdRecord(domain.ActivityActive, now.Add(-2*time.Hour), true)
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-time.Hour), "429 Too Many Requests")}
	v := deriveWatchdog(rec, cfg, errs, nil, now)
	if v.needsAttention {
		t.Fatalf("terminated session needsAttention = true, want false")
	}
}

func TestDeriveWatchdogStalledActiveNoProgress(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-11*time.Minute), false), cfg, nil, nil, now)
	if !v.needsAttention {
		t.Fatal("stalled active session did not need attention")
	}
	if v.reason != domain.AttentionReasonStalled {
		t.Fatalf("reason = %s, want stalled", v.reason)
	}
	if !strings.Contains(v.detail, "11m") {
		t.Fatalf("detail %q does not mention quiet duration", v.detail)
	}
}

func TestDeriveWatchdogStalledThresholdRespectsProjectConfig(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.WatchdogConfig{StalledAfterMinutes: 30}.Resolve()
	// 11 minutes quiet is a stall under the default (10m) but not under 30m.
	if v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-11*time.Minute), false), cfg, nil, nil, now); v.needsAttention {
		t.Fatalf("needsAttention under 30m threshold, want false")
	}
	if v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-31*time.Minute), false), cfg, nil, nil, now); !v.needsAttention {
		t.Fatal("did not stall after 31m under 30m threshold")
	}
}

func TestDeriveWatchdogQuestionPending(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	v := deriveWatchdog(wdRecord(domain.ActivityWaitingInput, now.Add(-6*time.Minute), false), cfg, nil, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonQuestionPending {
		t.Fatalf("verdict = %+v, want question_pending", v)
	}
}

func TestDeriveWatchdogDecisionPendingCarriesMarker(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	v := deriveWatchdog(wdRecord(domain.ActivityBlocked, now.Add(-6*time.Minute), false), cfg, nil, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonDecisionPending {
		t.Fatalf("verdict = %+v, want decision_pending", v)
	}
	if !strings.Contains(v.detail, domain.DecisionNeededMarker) {
		t.Fatalf("detail %q lacks DECISION NEEDED marker", v.detail)
	}
}

func TestDeriveWatchdogConsecutiveInfraErrorsRaiseBlockedInfra(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// Three retryable errors with no progress between them, even though the
	// last one was just recorded (nothing quiet yet).
	errs := []domain.WorkerErrorEvent{
		wdErr(now.Add(-3*time.Minute), "500 internal error"),
		wdErr(now.Add(-2*time.Minute), "upstream timeout"),
		wdErr(now.Add(-time.Minute), "503 service unavailable"),
	}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-4*time.Minute), false), cfg, errs, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonBlockedInfra {
		t.Fatalf("verdict = %+v, want blocked_infra", v)
	}
	if v.lastErrorAt == nil || !v.lastErrorAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("lastErrorAt = %v, want latest error time", v.lastErrorAt)
	}
}

func TestDeriveWatchdogInfraErrorsBelowThresholdStayQuiet(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// Two retryable errors, under the threshold of three, and the worker is
	// well within the stalled window thanks to recent-ish progress.
	errs := []domain.WorkerErrorEvent{
		wdErr(now.Add(-2*time.Minute), "503 service unavailable"),
		wdErr(now.Add(-time.Minute), "timeout"),
	}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-3*time.Minute), false), cfg, errs, nil, now)
	if v.needsAttention {
		t.Fatalf("needsAttention = true (%s), want false below infra threshold", v.detail)
	}
}

func TestDeriveWatchdogProviderQuotaAfterQuiet(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-11*time.Minute), "429 rate limit exceeded for API key")}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-12*time.Minute), false), cfg, errs, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonProviderQuota {
		t.Fatalf("verdict = %+v, want provider_quota", v)
	}
	if v.lastErrorAt == nil || !v.lastErrorAt.Equal(now.Add(-11*time.Minute)) {
		t.Fatalf("lastErrorAt = %v, want error time", v.lastErrorAt)
	}
}

func TestDeriveWatchdogProviderAuthAfterQuiet(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-12*time.Minute), "403 forbidden: invalid_api_key")}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-13*time.Minute), false), cfg, errs, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonProviderAuth {
		t.Fatalf("verdict = %+v, want provider_auth", v)
	}
}

func TestDeriveWatchdogEnvironmentErrorAfterQuiet(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-12*time.Minute), "EACCES: permission denied opening /ws/.ao/running.json")}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-13*time.Minute), false), cfg, errs, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonEnvironmentError {
		t.Fatalf("verdict = %+v, want environment_error", v)
	}
}

func TestDeriveWatchdogVCSConflictAfterQuiet(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-12*time.Minute), "fatal: merge conflict in feat/x")}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-13*time.Minute), false), cfg, errs, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonVCSConflict {
		t.Fatalf("verdict = %+v, want vcs_conflict", v)
	}
}

func TestDeriveWatchdogUnknownErrorQuietRaisesStalledWithExcerpt(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// A classification miss must never mean silence: an unrecognized error
	// followed by quiet still raises stalled, with the error excerpt in the
	// detail.
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-12*time.Minute), "weird-upstream-42: the cheese has moved")}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-13*time.Minute), false), cfg, errs, nil, now)
	if !v.needsAttention {
		t.Fatal("unknown error + quiet did not raise stalled")
	}
	if v.reason != domain.AttentionReasonStalled {
		t.Fatalf("reason = %s, want stalled", v.reason)
	}
	if !strings.Contains(v.detail, "weird-upstream-42") {
		t.Fatalf("detail %q lacks error excerpt", v.detail)
	}
}

func TestDeriveWatchdogTransientErrorRecoversOnNextProgress(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// Key acceptance: a transient 429 at T-11m is followed by progress at
	// T-2m, so on the next read the error predates the latest progress, the
	// flag auto-clears, and no manual reset is involved.
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-11*time.Minute), "429 rate limit exceeded")}
	rec := wdRecord(domain.ActivityActive, now.Add(-2*time.Minute), false)
	v := deriveWatchdog(rec, cfg, errs, nil, now)
	if v.needsAttention {
		t.Fatalf("recovered session still needsAttention (%s); error must be ignored once progress follows", v.detail)
	}
	if v.lastErrorAt != nil {
		t.Fatalf("lastErrorAt = %v, want nil after recovery", v.lastErrorAt)
	}
}

func TestDeriveWatchdogProgressAfterErrorsClearsBlockedInfra(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	errs := []domain.WorkerErrorEvent{
		wdErr(now.Add(-9*time.Minute), "500 internal error"),
		wdErr(now.Add(-8*time.Minute), "timeout"),
		wdErr(now.Add(-7*time.Minute), "503 service unavailable"),
	}
	// Progress at T-3m came after all three errors: the blocked_infra run
	// ended, and nothing else is wrong.
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-3*time.Minute), false), cfg, errs, nil, now)
	if v.needsAttention {
		t.Fatalf("needsAttention after progress cleared the errors: %s", v.detail)
	}
}

func TestDeriveWatchdogErrorsBeforeProgressIgnored(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// Errors from a previous outage that predate the session's latest progress
	// are history, not a live stall.
	errs := []domain.WorkerErrorEvent{wdErr(now.Add(-20*time.Minute), "402 payment required")}
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-5*time.Minute), false), cfg, errs, nil, now)
	if v.needsAttention {
		t.Fatalf("pre-progress error raised attention: %s", v.detail)
	}
}

func TestDeriveWatchdogQuestionThresholdRespectsProjectConfig(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.WatchdogConfig{QuestionPendingAfterMinutes: 1}.Resolve()
	// Default would wait 5m; the project override pings at 1m.
	v := deriveWatchdog(wdRecord(domain.ActivityWaitingInput, now.Add(-2*time.Minute), false), cfg, nil, nil, now)
	if !v.needsAttention || v.reason != domain.AttentionReasonQuestionPending {
		t.Fatalf("verdict = %+v, want question_pending under 1m override", v)
	}
}

func TestExcerptWorkerErrorBoundsMessage(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := excerptWorkerError(long)
	if len([]rune(got)) > 163 {
		t.Fatalf("excerpt too long: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("excerpt %q lacks truncation marker", got)
	}
	// Newlines and tabs are flattened to spaces so the sentence stays single-line.
	got = excerptWorkerError("line one\n\tline two")
	if strings.ContainsAny(got, "\n\t") || !strings.Contains(got, "line one line two") {
		t.Fatalf("excerpt %q not flattened", got)
	}
}

func TestClassifyWorkerErrorPrecedence(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want workerErrorClass
	}{
		{"quota 429", "429 Too Many Requests", workerErrorQuota},
		{"quota spend limit", "spend limit reached for pay-as-you-go", workerErrorQuota},
		{"auth 403 wins over env permission denied", "403 forbidden (permission denied): invalid api key", workerErrorAuth},
		{"env eacces", "EACCES: permission denied", workerErrorEnv},
		{"vcs after env", "fatal: worktree: permission denied reading .git", workerErrorEnv},
		{"vcs merge conflict", "error: merge conflict in package.json", workerErrorVCS},
		{"transient 502", "502 bad gateway", workerErrorTransient},
		{"transient certificate", "x509: certificate signed by unknown authority", workerErrorTransient},
		{"transient ssl handshake", "SSL handshake failed", workerErrorTransient},
		{"transient tls reset", "TLS connection reset by peer", workerErrorTransient},
		{"unknown catch-all", "the cheese has moved", workerErrorUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWorkerError(tc.msg); got != tc.want {
				t.Fatalf("classify(%q) = %s, want %s", tc.msg, got, tc.want)
			}
		})
	}
}

func TestDeriveWatchdogSwitchRecoveryPendingFiresOnRetainedMarkers(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// Each retained recovery marker, in every state it is valid in. Progress is
	// recent (T-1m) so nothing else would raise: the verdict fires immediately
	// off the switch fact alone.
	tests := []struct {
		name  string
		state domain.AgentSwitchState
		code  domain.AgentSwitchErrorCode
	}{
		{"source stop unconfirmed", domain.AgentSwitchStoppingSource, domain.AgentSwitchErrorSourceStopUnconfirmed},
		{"target start unconfirmed", domain.AgentSwitchStartingTarget, domain.AgentSwitchErrorTargetStartUnconfirmed},
		{"source restore unconfirmed after source stopped", domain.AgentSwitchSourceStopped, domain.AgentSwitchErrorSourceRestoreUnconfirmed},
		{"source restore unconfirmed while starting target", domain.AgentSwitchStartingTarget, domain.AgentSwitchErrorSourceRestoreUnconfirmed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sw := wdSwitch(tc.state, tc.code, now.Add(-2*time.Minute))
			v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-time.Minute), false), cfg, nil, sw, now)
			if !v.needsAttention || v.reason != domain.AttentionReasonSwitchRecoveryPending {
				t.Fatalf("verdict = %+v, want switch_recovery_pending", v)
			}
			if v.reason != "switch_recovery_pending" {
				t.Fatalf("reason = %q, want stable wire value", v.reason)
			}
			// The switch instant, not an error instant, anchors the timestamp.
			if v.lastErrorAt == nil || !v.lastErrorAt.Equal(now.Add(-2*time.Minute)) {
				t.Fatalf("lastErrorAt = %v, want switch UpdatedAt", v.lastErrorAt)
			}
			if !strings.Contains(v.detail, string(tc.code)) || !strings.Contains(v.detail, "Restore") {
				t.Fatalf("detail %q lacks error code or Restore affordance", v.detail)
			}
		})
	}
}

func TestDeriveWatchdogSwitchRecoveryDetailNamesHarnessesAndAge(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	sw := wdSwitch(domain.AgentSwitchSourceStopped, domain.AgentSwitchErrorSourceRestoreUnconfirmed, now.Add(-7*time.Minute))
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-time.Minute), false), domain.DefaultWatchdog(), nil, sw, now)
	for _, want := range []string{"claude-code", "codex", "source_restore_unconfirmed", "7m", "Restore"} {
		if !strings.Contains(v.detail, want) {
			t.Fatalf("detail %q lacks %q", v.detail, want)
		}
	}
}

func TestDeriveWatchdogHealthyMidSwitchStaysSilent(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// Non-terminal sagas that are not wedged carry no retained marker, so the
	// switch rule must not fire. Recent progress keeps the fallback rules quiet.
	for _, state := range []domain.AgentSwitchState{
		domain.AgentSwitchPreparingHandoff,
		domain.AgentSwitchStoppingSource,
		domain.AgentSwitchSourceStopped,
		domain.AgentSwitchStartingTarget,
		domain.AgentSwitchTargetReady,
		domain.AgentSwitchDelivering,
	} {
		sw := wdSwitch(state, "", now.Add(-time.Minute))
		v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-time.Minute), false), cfg, nil, sw, now)
		if v.needsAttention {
			t.Fatalf("healthy mid-switch saga %s raised attention: %+v", state, v)
		}
	}
}

func TestDeriveWatchdogSwitchRecoveryTerminatedNeverFlags(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	sw := wdSwitch(domain.AgentSwitchStartingTarget, domain.AgentSwitchErrorTargetStartUnconfirmed, now.Add(-time.Minute))
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-time.Minute), true), domain.DefaultWatchdog(), nil, sw, now)
	if v.needsAttention {
		t.Fatalf("terminated session with recovery switch flagged: %+v", v)
	}
}

func TestDeriveWatchdogSwitchRecoveryPrecedenceAfterPauseStates(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	sw := wdSwitch(domain.AgentSwitchSourceStopped, domain.AgentSwitchErrorSourceRestoreUnconfirmed, now.Add(-time.Minute))
	// A parked question outranks the wedged switch: answering it is the more
	// actionable fact, and the task orders the switch check after pause states.
	v := deriveWatchdog(wdRecord(domain.ActivityWaitingInput, now.Add(-6*time.Minute), false), cfg, nil, sw, now)
	if v.reason != domain.AttentionReasonQuestionPending {
		t.Fatalf("reason = %s, want question_pending over switch recovery", v.reason)
	}
	// Same for an open decision dialog.
	v = deriveWatchdog(wdRecord(domain.ActivityBlocked, now.Add(-6*time.Minute), false), cfg, nil, sw, now)
	if v.reason != domain.AttentionReasonDecisionPending {
		t.Fatalf("reason = %s, want decision_pending over switch recovery", v.reason)
	}
}

func TestDeriveWatchdogSwitchRecoveryPrecedenceBeforeErrorRules(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	// A wedged switch outranks both the consecutive-infra rule and the stalled
	// catch-all, even when the session is long quiet with a full error run: the
	// saga holds the interaction lock, so ownership recovery is the only move.
	errs := []domain.WorkerErrorEvent{
		wdErr(now.Add(-9*time.Minute), "500 internal error"),
		wdErr(now.Add(-8*time.Minute), "upstream timeout"),
		wdErr(now.Add(-7*time.Minute), "503 service unavailable"),
	}
	sw := wdSwitch(domain.AgentSwitchStoppingSource, domain.AgentSwitchErrorSourceStopUnconfirmed, now.Add(-6*time.Minute))
	v := deriveWatchdog(wdRecord(domain.ActivityActive, now.Add(-20*time.Minute), false), cfg, errs, sw, now)
	if v.reason != domain.AttentionReasonSwitchRecoveryPending {
		t.Fatalf("reason = %s, want switch_recovery_pending over blocked_infra/stalled", v.reason)
	}
}

func TestDeriveWatchdogSwitchRecoveryClearsWhenRowClears(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cfg := domain.DefaultWatchdog()
	rec := wdRecord(domain.ActivityActive, now.Add(-time.Minute), false)
	sw := wdSwitch(domain.AgentSwitchSourceStopped, domain.AgentSwitchErrorSourceRestoreUnconfirmed, now.Add(-time.Minute))
	if v := deriveWatchdog(rec, cfg, nil, sw, now); !v.needsAttention {
		t.Fatal("wedged switch did not raise")
	}
	// The saga settled: the switch read now yields no row, and recent progress
	// leaves the session quiet with no manual reset.
	if v := deriveWatchdog(rec, cfg, nil, nil, now); v.needsAttention {
		t.Fatalf("cleared switch still flagged: %+v", v)
	}
}
