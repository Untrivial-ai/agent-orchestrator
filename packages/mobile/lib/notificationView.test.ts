import { describe, expect, it } from "vitest";
import { notificationAction, notificationSections, notificationTarget, notificationVisual, relativeTime } from "./notificationView";
import { darkTheme } from "./theme";

describe("notificationVisual", () => {
	it("gives every known type its own label", () => {
		const labels = ["needs_input", "ready_to_merge", "pr_merged", "pr_closed_unmerged", "review_completed", "review_changes_requested"].map(
			(t) => notificationVisual(darkTheme, t).label,
		);
		expect(new Set(labels).size).toBe(6);
	});

	// The renderer draws these with GitHub's own vocabulary, and merged has its own
	// hue rather than an action colour: it is settled, not still open — and it is
	// no longer the grey it shared with a session that had merely switched off.
	it("marks a merged PR the way the palette and the renderer both say", () => {
		const merged = notificationVisual(darkTheme, "pr_merged");
		expect(merged.icon).toBe("git-merge");
		expect(merged.color).toBe(darkTheme.purple);
		expect(merged.color).not.toBe(darkTheme.textSecondary);
		expect(merged.color).not.toBe(darkTheme.accent);
	});

	it("uses pull-request glyphs for pull-request outcomes", () => {
		expect(notificationVisual(darkTheme, "ready_to_merge").icon).toBe("git-pull-request-arrow");
		expect(notificationVisual(darkTheme, "pr_closed_unmerged").icon).toBe("git-pull-request-closed");
	});

	it("gives every known type a distinct semantic icon", () => {
		const icons = ["needs_input", "ready_to_merge", "pr_merged", "pr_closed_unmerged"].map(
			(type) => notificationVisual(darkTheme, type).icon,
		);
		expect(new Set(icons).size).toBe(4);
	});

	it("falls back to a usable label for an unknown type", () => {
		expect(notificationVisual(darkTheme, "something_new").label).toBe("something_new");
		expect(notificationVisual(darkTheme, "").label).toBe("Notification");
	});
});

describe("notificationTarget", () => {
	// Must agree with PushManager's tap routing: the same notification opened
	// from history and from the tray has to land in the same place.
	it("opens the session for a needs_input notification", () => {
		expect(notificationTarget({ type: "needs_input", sessionId: "abc" })).toBe("/session/abc");
	});

	it("falls back to the PRs tab when there is no session to open", () => {
		expect(notificationTarget({ type: "needs_input", sessionId: "" })).toBe("/prs");
		expect(notificationTarget({ type: "needs_input" })).toBe("/prs");
	});

	it("sends PR notifications to the PRs tab", () => {
		expect(notificationTarget({ type: "ready_to_merge", sessionId: "abc" })).toBe("/prs");
		expect(notificationTarget({ type: "pr_merged", sessionId: "abc" })).toBe("/prs");
	});

	it("opens the matching review result when the payload identifies its pull request", () => {
		expect(notificationTarget({ type: "review_completed", sessionId: "session 1", prUrl: "https://github.com/acme/repo/pull/42" }))
			.toBe("/review/session%201?prUrl=https%3A%2F%2Fgithub.com%2Facme%2Frepo%2Fpull%2F42");
		expect(notificationTarget({ type: "review_changes_requested", sessionId: "s1", prUrl: "https://github.com/acme/repo/pull/43" }))
			.toBe("/review/s1?prUrl=https%3A%2F%2Fgithub.com%2Facme%2Frepo%2Fpull%2F43");
	});

	it("falls back to the PR list when a review result payload is incomplete", () => {
		expect(notificationTarget({ type: "review_completed", sessionId: "s1" })).toBe("/prs");
		expect(notificationTarget({ type: "review_changes_requested", prUrl: "https://github.com/acme/repo/pull/43" })).toBe("/prs");
	});

	// A tray payload carries no guarantee of a type field, and PushManager passes
	// "" when it is missing. An unknown or absent type must still land somewhere
	// rather than routing to "/session/undefined".
	it("sends an unknown or missing type to the PRs tab", () => {
		expect(notificationTarget({ type: "" })).toBe("/prs");
		expect(notificationTarget({ type: "", sessionId: "abc" })).toBe("/prs");
		expect(notificationTarget({ type: "something_new", sessionId: "abc" })).toBe("/prs");
	});
});

describe("notificationSections", () => {
	it("puts unread notifications in the attention section before earlier history", () => {
		const read = { id: "read", status: "read" };
		const unread = { id: "unread", status: "unread" };

		expect(notificationSections([read, unread])).toEqual([
			{ key: "attention", title: "Needs attention", data: [unread] },
			{ key: "earlier", data: [read] },
		]);
	});

	it("omits empty sections", () => {
		const read = { id: "read", status: "read" };

		expect(notificationSections([read])).toEqual([
			{ key: "earlier", data: [read] },
		]);
		expect(notificationSections([])).toEqual([]);
	});
});

describe("relativeTime", () => {
	const now = Date.parse("2026-07-30T12:00:00Z");
	const ago = (ms: number) => new Date(now - ms).toISOString();
	const SEC = 1000;
	const MIN = 60 * SEC;
	const HOUR = 60 * MIN;
	const DAY = 24 * HOUR;

	it("collapses anything under a minute to now", () => {
		expect(relativeTime(ago(5 * SEC), now)).toBe("now");
	});

	it("steps through minutes, hours, days and weeks", () => {
		expect(relativeTime(ago(3 * MIN), now)).toBe("3m");
		expect(relativeTime(ago(4 * HOUR), now)).toBe("4h");
		expect(relativeTime(ago(2 * DAY), now)).toBe("2d");
		expect(relativeTime(ago(20 * DAY), now)).toBe("2w");
	});

	// Clock skew between phone and daemon can put a timestamp slightly ahead.
	it("does not render a negative age", () => {
		expect(relativeTime(ago(-30 * SEC), now)).toBe("now");
	});

	it("returns nothing for an unparseable timestamp", () => {
		expect(relativeTime("not-a-date", now)).toBe("");
	});
});

describe("notificationAction", () => {
	const ready = { terminated: false, sessionsReady: true };

	it("opens the session behind a live notification", () => {
		expect(notificationAction({ type: "needs_input", sessionId: "s1" }, ready)).toEqual({ kind: "open", sessionId: "s1" });
		expect(notificationAction({ type: "ready_to_merge", sessionId: "s1" }, ready)).toEqual({ kind: "open", sessionId: "s1" });
	});

	// The renderer's rule: a paused agent is the only thing a terminated
	// needs_input row can act on, so it offers restore rather than navigating to
	// a session with nothing to show.
	it("offers restore instead of opening a terminated session that wants input", () => {
		expect(notificationAction({ type: "needs_input", sessionId: "s1" }, { terminated: true, sessionsReady: true }))
			.toEqual({ kind: "restore", sessionId: "s1" });
	});

	// ...but a finished PR is still worth reading, so termination does not gate it.
	it("still opens a terminated session behind a PR outcome", () => {
		for (const type of ["ready_to_merge", "pr_merged", "pr_closed_unmerged"]) {
			expect(notificationAction({ type, sessionId: "s1" }, { terminated: true, sessionsReady: true }))
				.toEqual({ kind: "open", sessionId: "s1" });
		}
	});

	it("waits rather than guessing before the board has loaded", () => {
		expect(notificationAction({ type: "needs_input", sessionId: "s1" }, { terminated: false, sessionsReady: false }))
			.toEqual({ kind: "none" });
	});

	it("sends a session-less PR notification to the PR list", () => {
		expect(notificationAction({ type: "ready_to_merge" }, ready)).toEqual({ kind: "prs" });
		expect(notificationAction({ type: "needs_input" }, ready)).toEqual({ kind: "none" });
	});

	it("opens review results directly without waiting for the session board", () => {
		const notification = { type: "review_completed", sessionId: "s1", prUrl: "https://github.com/acme/repo/pull/42" };
		expect(notificationAction(notification, { terminated: false, sessionsReady: false })).toEqual({
			kind: "review",
			sessionId: "s1",
			prUrl: notification.prUrl,
		});
	});

	it("sends incomplete review result notifications to the PR list", () => {
		expect(notificationAction({ type: "review_completed", sessionId: "s1" }, ready)).toEqual({ kind: "prs" });
		expect(notificationAction({ type: "review_changes_requested", prUrl: "https://github.com/acme/repo/pull/42" }, ready))
			.toEqual({ kind: "prs" });
	});
});
