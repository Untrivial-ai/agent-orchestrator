// Presentation rules for the notification history list. Pure — no React Native
// or Expo imports — so the wording and the routing decision are unit-testable,
// the same split as pushStatus.ts / push.ts.
import type { Theme } from "./theme";

export type NotificationVisual = {
	/** lucide icon names, drawn from the renderer's own path data. */
	icon: "message-square-dot" | "git-pull-request-arrow" | "git-merge" | "git-pull-request-closed" | "bell";
	color: string;
	label: string;
};

export type NotificationSection<T> = {
	key: "attention" | "earlier";
	/**
	 * Absent for the read group: the split still orders unread first, but the
	 * rows below it are plainly older and did not need a word to say so.
	 */
	title?: "Needs attention";
	data: T[];
};

/**
 * Keeps actionable unread history ahead of settled items without disturbing
 * the daemon's newest-first order inside either group.
 */
export function notificationSections<T extends { status: string }>(items: readonly T[]): NotificationSection<T>[] {
	const attention = items.filter((item) => item.status === "unread");
	const earlier = items.filter((item) => item.status !== "unread");
	const sections: NotificationSection<T>[] = [];
	if (attention.length > 0) sections.push({ key: "attention", title: "Needs attention", data: attention });
	if (earlier.length > 0) sections.push({ key: "earlier", data: earlier });
	return sections;
}

/** Icon, colour and short label for one notification type. */
export function notificationVisual(t: Theme, type: string): NotificationVisual {
	switch (type) {
		case "needs_input":
			return { icon: "message-square-dot", color: t.amber, label: "Needs input" };
		case "ready_to_merge":
			return { icon: "git-pull-request-arrow", color: t.green, label: "Ready to merge" };
		case "pr_merged":
			// Purple, as this theme's own palette says: "purple = merged (terminal,
			// not actionable)". It was rendering blue with a generic tick, which
			// read as an action still open and matched neither desktop nor the
			// palette's own rule.
			return { icon: "git-merge", color: t.purple, label: "Merged" };
		case "pr_closed_unmerged":
			return { icon: "git-pull-request-closed", color: t.red, label: "Closed" };
		case "review_completed":
			return { icon: "git-pull-request-arrow", color: t.green, label: "Review completed" };
		case "review_changes_requested":
			return { icon: "message-square-dot", color: t.amber, label: "Review changes requested" };
		default:
			return { icon: "bell", color: t.textTertiary, label: type || "Notification" };
	}
}

/**
 * Where tapping a notification should land. Mirrors the routing PushManager
 * already applies to a notification tap, so opening an item from history and
 * opening it from the tray agree — the rule lives here rather than being written
 * twice.
 */
export function notificationTarget(n: { type: string; sessionId?: string; prUrl?: string }): string {
	const sessionId = n.sessionId?.trim();
	if ((n.type === "review_completed" || n.type === "review_changes_requested") && sessionId && n.prUrl) {
		return `/review/${encodeURIComponent(sessionId)}?prUrl=${encodeURIComponent(n.prUrl)}`;
	}
	return n.type === "needs_input" && sessionId ? `/session/${encodeURIComponent(sessionId)}` : "/prs";
}

/** Compact "3m" / "4h" / "2d" stamp. Returns "" for an unparseable timestamp. */
export function relativeTime(iso: string, now: number = Date.now()): string {
	const then = Date.parse(iso);
	if (Number.isNaN(then)) return "";
	const secs = Math.max(0, Math.round((now - then) / 1000));
	if (secs < 60) return "now";
	const mins = Math.floor(secs / 60);
	if (mins < 60) return `${mins}m`;
	const hours = Math.floor(mins / 60);
	if (hours < 24) return `${hours}h`;
	const days = Math.floor(hours / 24);
	if (days < 7) return `${days}d`;
	return `${Math.floor(days / 7)}w`;
}

/**
 * What tapping a notification should do, given the state of the session behind
 * it. Ported from the renderer's NotificationItem, which decides this with:
 *
 *   offerRestore    = terminated && type === "needs_input"
 *   canOpenSession  = sessionId && sessionsReady && (!terminated || !offerRestore)
 *
 * Review outcomes are the exception: their payload identifies the exact PR,
 * so they can open review detail without waiting for the session board.
 * The distinction the remaining notifications draw is worth keeping: a terminated session behind
 * "needs input" has a paused agent and nothing to show, so restore is the only
 * sensible action. A terminated session behind a PR outcome describes work that
 * already finished — there is nothing to resume, so it stays readable rather
 * than being gated behind a restore nobody wants.
 */
export type NotificationAction =
	| { kind: "open"; sessionId: string }
	| { kind: "review"; sessionId: string; prUrl: string }
	| { kind: "restore"; sessionId: string }
	| { kind: "prs" }
	| { kind: "none" };

export function notificationAction(
	n: { type: string; sessionId?: string; prUrl?: string },
	state: { terminated: boolean; sessionsReady: boolean },
): NotificationAction {
	const sessionId = n.sessionId?.trim();
	if (n.type === "review_completed" || n.type === "review_changes_requested") {
		return sessionId && n.prUrl
			? { kind: "review", sessionId, prUrl: n.prUrl }
			: { kind: "prs" };
	}
	// No session to open: a PR outcome still has somewhere useful to go.
	if (!sessionId) return n.type === "needs_input" ? { kind: "none" } : { kind: "prs" };
	// The board has not loaded yet, so whether it is terminated is unknown.
	// Guessing "open" would land on a screen that cannot resolve the session.
	if (!state.sessionsReady) return { kind: "none" };
	const offerRestore = state.terminated && n.type === "needs_input";
	if (offerRestore) return { kind: "restore", sessionId };
	return { kind: "open", sessionId };
}
