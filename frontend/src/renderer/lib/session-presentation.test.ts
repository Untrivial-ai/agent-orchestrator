import { describe, expect, it } from "vitest";
import {
	attentionZone,
	getAgentActivityView,
	getAttentionZoneView,
	getSessionStatusDotView,
	getSessionStatusView,
	getSessionTimelinePillView,
	isAgentActivityWorking,
	isSessionIdle,
} from "./session-presentation";
import type { WorkspaceSession } from "../types/workspace";

function sessionWith(overrides: Partial<WorkspaceSession>): WorkspaceSession {
	return {
		id: "sess-1",
		workspaceId: "ws-1",
		workspaceName: "my-app",
		title: "fix-bug",
		provider: "claude-code",
		branch: "feat/x",
		status: "working",
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [],
		...overrides,
	};
}

const openPr: WorkspaceSession["prs"][number] = {
	number: 7,
	url: "https://github.com/acme/app/pull/7",
	state: "open",
	ci: "unknown",
	review: "none",
	mergeability: "unknown",
	reviewComments: false,
	updatedAt: "2026-01-01T00:00:00Z",
};

describe("session presentation", () => {
	it.each([
		["active", "Working", true, "bg-status-working animate-status-pulse"],
		["idle", "Idle", false, "bg-status-idle"],
		["waiting_input", "Input Needed", false, "bg-status-needs-you"],
		["blocked", "Awaiting Decision", false, "bg-status-needs-you"],
		["exited", "Exited", false, "bg-status-exited"],
		["unknown", "Unknown", false, "bg-status-unknown"],
	] as const)("maps %s agent activity to %s", (state, label, breathe, indicatorClassName) => {
		expect(getAgentActivityView({ state, lastActivityAt: "" })).toMatchObject({
			label,
			breathe,
			indicatorClassName,
		});
	});

	it("uses raw agent activity, not session status, for working indicators", () => {
		expect(isAgentActivityWorking({ state: "active", lastActivityAt: "" })).toBe(true);
		expect(isAgentActivityWorking({ state: "idle", lastActivityAt: "" })).toBe(false);
		expect(isAgentActivityWorking(undefined)).toBe(false);
	});

	it.each([
		["working", "Working"],
		["idle", "Idle"],
		["needs_input", "Input needed"],
		["no_signal", "No signal"],
		["ci_failed", "CI failed"],
		["changes_requested", "Changes requested"],
		["review_pending", "Review pending"],
		["draft", "Draft PR"],
		["pr_open", "PR open"],
		["approved", "Approved"],
		["mergeable", "Ready"],
		["merged", "Merged"],
		["exited", "Exited"],
		["terminated", "Terminated"],
		["unknown", "Unknown status"],
	] as const)("maps %s session status to %s", (status, label) => {
		expect(getSessionStatusView(status).label).toBe(label);
	});

	it("uses distinct session-card tones for idle, no signal, and PR waiting states", () => {
		expect(getSessionStatusView("idle").className).toBe("text-status-idle");
		expect(getSessionStatusView("no_signal").className).toBe("text-status-unknown");
		expect(getSessionStatusView("draft").className).toBe("text-status-in-review");
		expect(getSessionStatusView("pr_open").className).toBe("text-status-in-review");
		expect(getSessionStatusView("review_pending").className).toBe("text-status-in-review");
		expect(getSessionStatusView("exited").className).toBe("text-status-exited");
	});

	it.each([
		["approved", "merge", "Ready to merge"],
		["mergeable", "merge", "Ready to merge"],
		["needs_input", "action", "Needs you"],
		["exited", "action", "Needs you"],
		["no_signal", "action", "Needs you"],
		["ci_failed", "action", "Needs you"],
		["changes_requested", "action", "Needs you"],
		["unknown", "action", "Needs you"],
		["review_pending", "pending", "In review"],
		["pr_open", "pending", "In review"],
		["draft", "pending", "In review"],
		["working", "working", "Working"],
		["idle", "working", "Working"],
		["merged", "merge", "Ready to merge"],
		["terminated", "done", "Terminated"],
	] as const)("maps %s to the %s attention zone", (status, zone, label) => {
		expect(attentionZone(sessionWith({ status }))).toBe(zone);
		expect(getAttentionZoneView(status)).toMatchObject({ zone, label });
	});

	it.each([
		["idle", "bg-status-idle"],
		["working", "bg-status-working"],
		["needs_input", "bg-status-needs-you"],
		["exited", "bg-status-needs-you"],
		["no_signal", "bg-status-needs-you"],
		["ci_failed", "bg-status-needs-you"],
		["changes_requested", "bg-status-needs-you"],
		["unknown", "bg-status-needs-you"],
		["draft", "bg-status-in-review"],
		["pr_open", "bg-status-in-review"],
		["review_pending", "bg-status-in-review"],
		["approved", "bg-status-ready"],
		["mergeable", "bg-status-ready"],
		["merged", "bg-status-merged"],
		["terminated", "bg-status-terminated"],
	] as const)("paints the %s session dot with its board-section tone", (status, dotClassName) => {
		expect(getSessionStatusDotView(sessionWith({ status }))).toMatchObject({ className: dotClassName });
	});

	it("prefers SCM state over runtime status for the dot tone", () => {
		// A running agent drives status to `working`, which would erase every PR
		// tone in the sidebar. SCM state wins so the row keeps saying "merged".
		const merged = sessionWith({
			status: "working",
			scmStatus: "merged",
			activity: { state: "active", lastActivityAt: "" },
		});

		expect(getSessionStatusDotView(merged)).toEqual({ className: "bg-status-merged", breathe: true });
	});

	it("keeps board-section color while raw working activity starts the motion", () => {
		const scmStatus = "mergeable" as const;

		expect(
			getSessionStatusDotView(sessionWith({ status: "idle", scmStatus, activity: { state: "idle", lastActivityAt: "" } })),
		).toEqual({ className: "bg-status-ready", breathe: false });
		expect(
			getSessionStatusDotView(
				sessionWith({ status: "working", scmStatus, activity: { state: "active", lastActivityAt: "" } }),
			),
		).toEqual({ className: "bg-status-ready", breathe: true });
	});

	it("uses a blinking blue dot when an idle-section session starts working", () => {
		expect(
			getSessionStatusDotView(
				sessionWith({ status: "idle", activity: { state: "active", lastActivityAt: "" } }),
			),
		).toEqual({ className: "bg-status-working", breathe: true });
	});

	it("paints a closed-without-merge session red", () => {
		expect(
			getSessionStatusDotView(
				sessionWith({ status: "idle", displayStatus: "Closed without merge" }),
			),
		).toEqual({ className: "bg-status-exited", breathe: false });
	});

	it("keeps activity indicator color independent from PR and CI presentation", () => {
		const active = getAgentActivityView({ state: "active", lastActivityAt: "" });
		const idle = getAgentActivityView({ state: "idle", lastActivityAt: "" });

		expect(active.indicatorClassName).toBe("bg-status-working animate-status-pulse");
		expect(idle.indicatorClassName).toBe("bg-status-idle");
	});

	it("uses a muted accent treatment for In Review instead of idle gray", () => {
		expect(getAttentionZoneView("review_pending")).toMatchObject({
			dot: "var(--color-status-in-review)",
			titleClassName: "text-status-in-review",
			dotClassName: "bg-status-in-review",
		});
	});

	it("classifies only backend-derived idle sessions for the work lane", () => {
		expect(isSessionIdle(sessionWith({ status: "idle" }))).toBe(true);
		expect(
			isSessionIdle(
				sessionWith({
					status: "idle",
					activity: { state: "active", lastActivityAt: "" },
					prs: [openPr],
				}),
			),
		).toBe(true);
		expect(
			isSessionIdle(
				sessionWith({
					status: "working",
					activity: { state: "idle", lastActivityAt: "" },
					prs: [openPr],
				}),
			),
		).toBe(false);
		expect(
			isSessionIdle(
				sessionWith({
					status: "working",
					activity: { state: "active", lastActivityAt: "" },
				}),
			),
		).toBe(false);
		expect(isSessionIdle(sessionWith({ status: "working" }))).toBe(false);
	});

	it.each([
		["no_signal", "No Signal", "var(--color-status-unknown)"],
		["ci_failed", "CI Failed", "var(--color-status-exited)"],
		["changes_requested", "Changes Requested", "var(--color-status-needs-you)"],
	] as const)("centralizes the %s timeline pill", (status, label, tone) => {
		expect(getSessionTimelinePillView(status)).toMatchObject({ label, tone, breathe: false });
	});
});

 it("shows an unstarted imported conversation without an error-coloured status dot", () => {
 expect(getSessionStatusDotView({...sessionWith({status: "exited"}), importedHistory: true})).toEqual({className: "bg-muted-foreground", breathe: false});
 });
