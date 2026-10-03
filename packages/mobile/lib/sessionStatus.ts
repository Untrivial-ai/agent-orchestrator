// Derived session status, split out of api.ts so pure view-model modules can
// use it — api.ts chains through config.ts into AsyncStorage and react-native,
// so nothing importing a value from it is unit-testable.
//
// Re-exported from api.ts, so existing call sites are unchanged.
import type { DashboardSession } from "./api";
import type { AttentionLevel } from "./theme";

const TERMINAL_STATUSES = new Set(["killed", "terminated", "done", "cleanup", "errored", "merged"]);

export function isTerminalStatus(status?: string | null): boolean {
	return !!status && TERMINAL_STATUSES.has(status);
}

// Fallback attention bucket when the server didn't compute attentionLevel.
export function attentionOf(s: DashboardSession): AttentionLevel {
	if (s.attentionLevel) return s.attentionLevel as AttentionLevel;
	const pr = s.pr ?? s.prs?.[0];
	if (s.status === "merged" || s.status === "done" || isTerminalStatus(s.status)) return "done";
	if (pr?.mergeability?.mergeable || s.status === "mergeable" || s.status === "approved") return "merge";
	if (s.status === "needs_input" || s.status === "stuck" || s.status === "errored") return "respond";
	if (
		pr?.ciStatus === "failing" ||
		pr?.reviewDecision === "changes_requested" ||
		s.status === "ci_failed" ||
		s.status === "changes_requested" ||
		s.status === "commented"
	)
		return "review";
	if (s.status === "pr_open" || s.status === "review_pending") return "pending";
	return "working";
}

// Matches desktop's `session.displayName ?? session.issueId ?? session.id`
// (useWorkspaceQuery.ts). `issueId` is the rung mobile was missing: the daemon
// stores the task name there — it is what desktop's New task dialog sends as its
// Title — so without it a named session still rendered as its own id, and the
// same session read differently in the two apps.
//
// issueTitle/userPrompt/summary are hardcoded null by mapSession today; they
// stay for forward compatibility, between the two rungs that actually fire.
//
// The candidates are filtered on trimmed content rather than truthiness. A
// displayName of " " is truthy, so a `||` chain stopped there and rendered a
// card with no title at all — and the id fallback, the one value that is always
// present, was never reached. Whitespace is not a name.
export function sessionTitle(s: DashboardSession): string {
	const candidates = [s.displayName, s.issueId, s.issueTitle, s.userPrompt, s.summary];
	for (const c of candidates) {
		const trimmed = c?.trim();
		if (trimmed) return trimmed;
	}
	// `id` is server-assigned and never blank, so this terminates. Trimmed anyway
	// so the return type's promise ("a non-empty string") holds unconditionally.
	return s.id.trim() || s.id;
}
