import { describe, expect, it } from "vitest";
import {
	attentionZone,
	getAgentActivityView,
	getAttentionZoneView,
	getDisplayStatusLabel,
	getSessionStatusView,
	getSessionTimelinePillView,
	getKanbanColumnView,
	toKanbanColumn,
	isAgentActivityWorking,
	isSessionIdle,
} from "./session-presentation";
import { DISPLAY_STATUSES } from "./session-models";

describe("session presentation", () => {
	it.each([
		["active", "Working", true, "bg-status-working animate-status-pulse"],
		["idle", "Idle", false, "bg-status-idle"],
		["waiting_input", "Input Needed", false, "bg-status-needs-you"],
		["blocked", "Awaiting Decision", false, "bg-status-needs-you"],
		["exited", "Exited", false, "bg-status-exited"],
		["unknown", "Unknown", false, "bg-status-unknown"],
	] as const)("maps %s activity without app state", (state, label, breathe, indicatorClassName) => {
		expect(getAgentActivityView({ state, lastActivityAt: "" })).toMatchObject({
			label,
			breathe,
			indicatorClassName,
		});
	});

	it("accepts injected labels", () => {
		expect(getSessionStatusView("working", (key) => `translated:${key}`).label).toBe(
			"translated:status.working",
		);
		expect(getAttentionZoneView("approved", (key) => `translated:${key}`).label).toBe(
			"translated:zone.merge",
		);
	});

	it.each([
		["approved", "merge"],
		["needs_input", "action"],
		["review_pending", "pending"],
		["working", "working"],
		["terminated", "done"],
	] as const)("maps %s to the %s attention zone", (status, zone) => {
		expect(attentionZone(status)).toBe(zone);
	});

	it.each([
		["working", "text-status-working", "bg-status-working"],
		["idle", "text-status-idle", "bg-status-idle"],
		["needs_input", "text-status-needs-you", "bg-status-needs-you"],
		["exited", "text-status-exited", "bg-status-exited"],
		["no_signal", "text-status-unknown", "bg-status-unknown"],
		["ci_failed", "text-status-exited", "bg-status-exited"],
		["changes_requested", "text-status-needs-you", "bg-status-needs-you"],
		["review_pending", "text-status-in-review", "bg-status-in-review"],
		["draft", "text-status-in-review", "bg-status-in-review"],
		["pr_open", "text-status-in-review", "bg-status-in-review"],
		["approved", "text-status-ready", "bg-status-ready"],
		["mergeable", "text-status-ready", "bg-status-ready"],
		["merged", "text-status-merged", "bg-status-merged"],
		["unknown", "text-status-unknown", "bg-status-unknown"],
	] as const)("pairs the %s text tone with a matching dot tone", (status, className, dotClassName) => {
		expect(getSessionStatusView(status)).toMatchObject({ className, dotClassName });
	});

	it("falls back to the unknown tone for an unrecognized status", () => {
		expect(getSessionStatusView("nonsense" as never)).toMatchObject({
			className: "text-status-unknown",
			dotClassName: "bg-status-unknown",
		});
	});

	it.each([
		["building", "Building", "bg-status-working"],
		["validating", "Validating", "bg-status-validating"],
		["needs_review", "In review", "bg-status-in-review"],
		["ready", "Ready", "bg-status-ready"],
		["archive", "Archive", "bg-status-terminated"],
	] as const)("gives the %s column its own label and palette", (column, label, dotClassName) => {
		expect(getKanbanColumnView(column)).toMatchObject({ column, label, dotClassName });
	});

	it("accepts injected labels for Kanban columns", () => {
		expect(getKanbanColumnView("needs_review", (key) => `translated:${key}`).label).toBe(
			"translated:column.needs_review",
		);
	});

	it("has exactly one translation key for every daemon display status", () => {
		for (const status of DISPLAY_STATUSES) {
			expect(getDisplayStatusLabel(status)).toBe(status);
			expect(getDisplayStatusLabel(status, (key) => `translated:${key}`)).toMatch(/^translated:displayStatus\./);
		}
	});

	it("shows an unrecognized display status as raw text instead of a translation key", () => {
		expect(getDisplayStatusLabel("Rebasing onto main", (key) => `translated:${key}`)).toBe(
			"Rebasing onto main",
		);
	});

	it("prefers the daemon's column over anything derived from status", () => {
		// A validating session can read "mergeable" on the card; the column wins.
		expect(toKanbanColumn("validating", "mergeable")).toBe("validating");
		expect(toKanbanColumn("building", "changes_requested")).toBe("building");
	});

	// A daemon that predates kanbanColumn still sends status, so the fallback
	// must land each session in the lane the board gave it before the column
	// existed rather than collapsing every live session into the first lane.
	// The statuses landing in needs_review are the whole review-feedback loop
	// as seen from a person's turn -- awaiting review, feedback to answer, a
	// failing check to decide about -- not only PRs awaiting a first review.
	it.each([
		["mergeable", "ready"],
		["approved", "ready"],
		["merged", "ready"],
		["changes_requested", "needs_review"],
		["needs_input", "needs_review"],
		["ci_failed", "needs_review"],
		["review_pending", "validating"],
		["draft", "validating"],
		["pr_open", "validating"],
		["working", "building"],
		["idle", "building"],
		["terminated", "archive"],
	] as const)("places a %s session from an older daemon in %s", (status, column) => {
		expect(toKanbanColumn(undefined, status)).toBe(column);
		expect(toKanbanColumn("", status)).toBe(column);
		// An unrecognized column (newer daemon, unknown lane) takes the same path.
		expect(toKanbanColumn("bogus", status)).toBe(column);
	});

	it("keeps lifecycle predicates independent of presentation labels", () => {
		expect(isAgentActivityWorking({ state: "active", lastActivityAt: "" })).toBe(true);
		expect(isAgentActivityWorking(undefined)).toBe(false);
		expect(isSessionIdle({ status: "idle" })).toBe(true);
		expect(isSessionIdle({ status: "working" })).toBe(false);
	});

	it("centralizes timeline status treatment", () => {
		expect(getSessionTimelinePillView("ci_failed")).toEqual({
			label: "CI Failed",
			tone: "var(--color-status-exited)",
			breathe: false,
		});
	});
});
