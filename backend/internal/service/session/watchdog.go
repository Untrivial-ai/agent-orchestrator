package session

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// workerErrorClass is the watchdog's read-time classification of one recorded
// provider error. Classification reads the raw message excerpt, so a
// classifier fix re-labels old rows without a backfill; anything unrecognized
// is unknown and still raises via the stalled catch-all (a classification
// miss must never mean silence).
type workerErrorClass string

const (
	workerErrorUnknown   workerErrorClass = "unknown"
	workerErrorQuota     workerErrorClass = "quota"
	workerErrorAuth      workerErrorClass = "auth"
	workerErrorEnv       workerErrorClass = "env"
	workerErrorVCS       workerErrorClass = "vcs"
	workerErrorTransient workerErrorClass = "transient"
)

// quotaPhrases match billing/quota/rate-limit errors: 429s, spend limits,
// exhausted credits. Matched before auth: a quota error that mentions tokens
// is still a quota error.
var quotaPhrases = []string{
	"429", "rate limit", "rate_limit", "rate-limit", "ratelimit",
	"too many requests", "quota", "spend limit", "spend_limit",
	"usage limit", "plan limit", "credit", "insufficient_quota",
	"billing", "payment", "pay-as-you-go",
}

// authPhrases match credential errors: 401/403, expired keys, re-auth
// demands. Matched before env so an SSH publickey denial reads as auth, not
// as a filesystem permission problem.
var authPhrases = []string{
	"401", "403", "unauthorized", "unauthorised", "forbidden",
	"authentication", "auth required", "reauth", "re-auth",
	"sign in", "signed in", "login", "log in", "token expired",
	"expired token", "invalid api key", "invalid_api_key", "api key",
	"apikey", "credentials", "publickey",
}

// envPhrases match environment errors: filesystem permissions, missing
// binaries, exhausted disks. These never heal without a person.
var envPhrases = []string{
	"eacces", "eperm", "enoent", "permission denied", "access is denied",
	"no such file", "command not found", "executable", "not installed",
	"eaddrinuse", "address already in use", "no space left", "enospc",
	"read-only file system", "readonly",
}

// vcsPhrases match version-control/workspace errors: worktree conflicts,
// merge conflicts, unpushed divergence. Matched after env so a denied
// worktree path still reads as env first.
var vcsPhrases = []string{
	"worktree", "work tree", "checked out", "merge conflict",
	"uncommitted changes", "rebase", "not a git repository",
	"failed to push", "non-fast-forward", "diverged", "index.lock",
	"git ",
}

// transientPhrases match retryable infrastructure errors that are not quota:
// 5xx, timeouts, connection resets, and TLS/certificate handshake failures.
// Enough of these in a row (with zero progress between) raise blocked_infra; a
// couple followed by silence read as stalled through the generic active-quiet
// rule instead.
var transientPhrases = []string{
	"500", "502", "503", "504", "timeout", "timed out", "connection",
	"econn", "socket", "temporarily", "try again", "overloaded",
	"capacity", "internal error", "server error", "bad gateway",
	"service unavailable", "network", "dns", "eai_again",
	"certificate", "cert", "tls", "ssl",
}

// classifyWorkerError maps one provider-error excerpt to its class. Precedence
// is quota, auth, env, vcs, transient, unknown: the first class with any
// phrase hit wins, and nothing falls through silently.
func classifyWorkerError(message string) workerErrorClass {
	lower := strings.ToLower(message)
	for _, phrase := range quotaPhrases {
		if strings.Contains(lower, phrase) {
			return workerErrorQuota
		}
	}
	for _, phrase := range authPhrases {
		if strings.Contains(lower, phrase) {
			return workerErrorAuth
		}
	}
	for _, phrase := range envPhrases {
		if strings.Contains(lower, phrase) {
			return workerErrorEnv
		}
	}
	for _, phrase := range vcsPhrases {
		if strings.Contains(lower, phrase) {
			return workerErrorVCS
		}
	}
	for _, phrase := range transientPhrases {
		if strings.Contains(lower, phrase) {
			return workerErrorTransient
		}
	}
	return workerErrorUnknown
}

// isInfraClass reports whether an error class counts toward the consecutive
// provider-error rule: quota and transient failures are the ones a nudge can
// heal. Auth/env/vcs failures have their own sticky rules and must never be
// mislabeled as infrastructure.
func isInfraClass(class workerErrorClass) bool {
	return class == workerErrorQuota || class == workerErrorTransient
}

// watchdogVerdict is the derived attention outcome for one session read.
type watchdogVerdict struct {
	needsAttention bool
	reason         domain.AttentionReason
	detail         string
	lastErrorAt    *time.Time
}

// deriveWatchdog computes the needs-attention verdict from durable facts: the
// session's activity state/recency, its project-resolved thresholds, its
// recorded worker errors (newest first; sorted defensively), and its
// already-loaded active agent switch when one exists. Evaluation order is pause
// states, then a wedged switch, then error rules, then the active-quiet
// catch-all; a terminated session never needs attention. Any new progress
// (activity newer than every error) clears error-based verdicts with no manual
// reset, and a cleared switch row clears its verdict on the next read.
func deriveWatchdog(rec domain.SessionRecord, cfg domain.ResolvedWatchdogConfig, errs []domain.WorkerErrorEvent, activeSwitch *domain.AgentSwitch, now time.Time) watchdogVerdict {
	if rec.IsTerminated {
		return watchdogVerdict{}
	}
	progressAt := rec.Activity.LastActivityAt
	if progressAt.IsZero() {
		progressAt = rec.CreatedAt
	}
	if progressAt.IsZero() {
		return watchdogVerdict{}
	}
	quiet := now.Sub(progressAt)

	state := rec.Activity.State
	if state == domain.ActivityBlocked && quiet >= cfg.QuestionPendingAfter {
		return watchdogVerdict{
			needsAttention: true,
			reason:         domain.AttentionReasonDecisionPending,
			detail: fmt.Sprintf("%s: permission dialog open for %s; automation will not answer it",
				domain.DecisionNeededMarker, trimDuration(quiet)),
		}
	}
	if state == domain.ActivityWaitingInput && quiet >= cfg.QuestionPendingAfter {
		return watchdogVerdict{
			needsAttention: true,
			reason:         domain.AttentionReasonQuestionPending,
			detail:         fmt.Sprintf("Waiting on input for %s (question pending)", trimDuration(quiet)),
		}
	}

	// A wedged agent switch outranks the error rules: the saga already holds the
	// session's interaction lock, so no retry or nudge can move it and only a
	// person can resolve ownership. Healthy mid-switch sagas do not match
	// RequiresRecovery and fall through to the error and quiet rules like any
	// other session; the check re-runs every read, so the verdict clears the
	// moment the row clears.
	if activeSwitch != nil && activeSwitch.RequiresRecovery() {
		verdict := watchdogVerdict{
			needsAttention: true,
			reason:         domain.AttentionReasonSwitchRecoveryPending,
			detail:         switchRecoveryDetail(*activeSwitch, now),
		}
		if at := switchRecoveryAt(*activeSwitch); !at.IsZero() {
			verdict.lastErrorAt = &at
		}
		return verdict
	}

	sorted := append([]domain.WorkerErrorEvent(nil), errs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].OccurredAt.Equal(sorted[j].OccurredAt) {
			return sorted[i].Message < sorted[j].Message
		}
		return sorted[i].OccurredAt.After(sorted[j].OccurredAt)
	})
	var since []domain.WorkerErrorEvent
	for _, err := range sorted {
		if err.OccurredAt.After(progressAt) {
			since = append(since, err)
		}
	}
	if len(since) > 0 {
		latest := since[0]
		at := latest.OccurredAt
		infra := 0
		for _, err := range since {
			if isInfraClass(classifyWorkerError(err.Message)) {
				infra++
			}
		}
		if infra >= cfg.ConsecutiveInfraErrors {
			return watchdogVerdict{
				needsAttention: true,
				reason:         domain.AttentionReasonBlockedInfra,
				detail: fmt.Sprintf("%d consecutive provider errors with no progress; latest %s ago: %s",
					infra, trimDuration(now.Sub(at)), excerptWorkerError(latest.Message)),
				lastErrorAt: &at,
			}
		}
		errQuiet := now.Sub(maxTime(progressAt, at))
		if errQuiet >= cfg.StalledAfter {
			class := classifyWorkerError(latest.Message)
			reason := domain.AttentionReasonStalled
			prefix := fmt.Sprintf("No progress for %s", trimDuration(now.Sub(progressAt)))
			switch class {
			case workerErrorQuota:
				reason = domain.AttentionReasonProviderQuota
				prefix = fmt.Sprintf("Provider quota error %s ago with no progress since", trimDuration(now.Sub(at)))
			case workerErrorAuth:
				reason = domain.AttentionReasonProviderAuth
				prefix = fmt.Sprintf("Provider auth error %s ago with no progress since", trimDuration(now.Sub(at)))
			case workerErrorEnv:
				reason = domain.AttentionReasonEnvironmentError
				prefix = fmt.Sprintf("Environment error %s ago with no progress since", trimDuration(now.Sub(at)))
			case workerErrorVCS:
				reason = domain.AttentionReasonVCSConflict
				prefix = fmt.Sprintf("Workspace error %s ago with no progress since", trimDuration(now.Sub(at)))
			}
			return watchdogVerdict{
				needsAttention: true,
				reason:         reason,
				detail:         fmt.Sprintf("%s: %s", prefix, excerptWorkerError(latest.Message)),
				lastErrorAt:    &at,
			}
		}
	}

	if state == domain.ActivityActive && quiet >= cfg.StalledAfter {
		return watchdogVerdict{
			needsAttention: true,
			reason:         domain.AttentionReasonStalled,
			detail:         fmt.Sprintf("Active with no progress for %s", trimDuration(quiet)),
		}
	}
	return watchdogVerdict{}
}

// switchRecoveryAt is the instant a wedged switch last changed: its durable
// UpdatedAt, falling back to RequestedAt for a row that never advanced past the
// request. Zero means the row carries no usable instant.
func switchRecoveryAt(sw domain.AgentSwitch) time.Time {
	if !sw.UpdatedAt.IsZero() {
		return sw.UpdatedAt
	}
	return sw.RequestedAt
}

// switchRecoveryDetail is the human sentence behind a switch_recovery_pending
// verdict: the harness pair, the stable error code, how long the saga has been
// wedged, and the Restore affordance that resolves it.
func switchRecoveryDetail(sw domain.AgentSwitch, now time.Time) string {
	detail := fmt.Sprintf("Agent switch %s -> %s needs recovery (%s", sw.FromHarness, sw.TargetHarness, sw.ErrorCode)
	if at := switchRecoveryAt(sw); !at.IsZero() {
		detail += fmt.Sprintf(", %s", trimDuration(now.Sub(at)))
	}
	return detail + "); open the agent-switch dialog and Restore"
}

// excerptWorkerError bounds one error message for human display: single line,
// capped well below the stored excerpt cap.
func excerptWorkerError(message string) string {
	const maxExcerptRunes = 160
	flat := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, strings.TrimSpace(message))
	flat = strings.Join(strings.Fields(flat), " ")
	runes := []rune(flat)
	if len(runes) <= maxExcerptRunes {
		return flat
	}
	return string(runes[:maxExcerptRunes]) + "..."
}

// trimDuration renders a duration the way a dashboard would: 5m, 3h12m, 8h.
// Sub-second noise is rounded away; a non-positive duration reads as 0s.
func trimDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0 && s > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
