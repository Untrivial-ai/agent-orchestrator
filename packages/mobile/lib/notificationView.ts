// Presentation rules for the notification history list. Pure — no React Native
// or Expo imports — so the wording and the routing decision are unit-testable,
// the same split as pushStatus.ts / push.ts.
import type { Theme } from "./theme";

export type NotificationVisual = {
	icon: "message-circle" | "git-pull-request" | "check-circle" | "x-circle" | "bell";
	color: string;
	label: string;
};

export type NotificationSection<T> = {
	key: "attention" | "earlier";
	title: "Needs attention" | "Earlier";
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
	if (earlier.length > 0) sections.push({ key: "earlier", title: "Earlier", data: earlier });
	return sections;
}

/** Icon, colour and short label for one notification type. */
export function notificationVisual(t: Theme, type: string): NotificationVisual {
	switch (type) {
		case "needs_input":
			return { icon: "message-circle", color: t.amber, label: "Needs input" };
		case "ready_to_merge":
			return { icon: "git-pull-request", color: t.green, label: "Ready to merge" };
		case "pr_merged":
			return { icon: "check-circle", color: t.blue, label: "Merged" };
		case "pr_closed_unmerged":
			return { icon: "x-circle", color: t.red, label: "Closed" };
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
export function notificationTarget(n: { type: string; sessionId?: string }): string {
	return n.type === "needs_input" && n.sessionId ? `/session/${n.sessionId}` : "/prs";
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
