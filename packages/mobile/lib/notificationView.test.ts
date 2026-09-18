import { describe, expect, it } from "vitest";
import { notificationSections, notificationTarget, notificationVisual, relativeTime } from "./notificationView";
import { darkTheme } from "./theme";

describe("notificationVisual", () => {
	it("gives every known type its own label", () => {
		const labels = ["needs_input", "ready_to_merge", "pr_merged", "pr_closed_unmerged"].map(
			(t) => notificationVisual(darkTheme, t).label,
		);
		expect(new Set(labels).size).toBe(4);
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
			{ key: "earlier", title: "Earlier", data: [read] },
		]);
	});

	it("omits empty sections", () => {
		const read = { id: "read", status: "read" };

		expect(notificationSections([read])).toEqual([
			{ key: "earlier", title: "Earlier", data: [read] },
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
