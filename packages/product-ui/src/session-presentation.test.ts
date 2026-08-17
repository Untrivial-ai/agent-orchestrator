import { describe, expect, it } from "vitest";
import {
	attentionZone,
	getAgentActivityView,
	getAttentionZoneView,
	getSessionStatusView,
	getSessionTimelinePillView,
	isAgentActivityWorking,
	isSessionIdle,
} from "./session-presentation";

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
