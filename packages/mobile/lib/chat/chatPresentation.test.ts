import { describe, expect, it } from "vitest";

import { actionControlWidth, conversationMarkerPresentation, requestPresentation } from "./chatPresentation";

describe("chat presentation", () => {
	it("gives conversation-map states concise user-facing labels and semantic tones", () => {
		expect(conversationMarkerPresentation("running")).toEqual({ icon: "activity", label: "Working", tone: "working" });
		expect(conversationMarkerPresentation("completed")).toEqual({ icon: "check-circle", label: "Done", tone: "success" });
		expect(conversationMarkerPresentation("failed")).toEqual({ icon: "alert-circle", label: "Failed", tone: "danger" });
		expect(conversationMarkerPresentation("interrupted")).toEqual({ icon: "minus-circle", label: "Stopped", tone: "muted" });
		expect(conversationMarkerPresentation("queued")).toEqual({ icon: "clock", label: "Queued", tone: "muted" });
	});

	it("does not expose daemon state names for conversation entries without a turn", () => {
		expect(conversationMarkerPresentation(undefined)).toEqual({ icon: "message-circle", label: "Update", tone: "accent" });
	});

	it("distinguishes pending requests from historical resolved requests", () => {
		expect(requestPresentation("approval", true)).toEqual({ icon: "shield", title: "Approval needed", badge: "Needs approval", tone: "attention" });
		expect(requestPresentation("input", true)).toEqual({ icon: "message-circle", title: "Agent needs input", badge: "Needs input", tone: "accent" });
		expect(requestPresentation("approval", false)).toEqual({ icon: "check-circle", title: "Approval resolved", badge: "Resolved", tone: "muted" });
		expect(requestPresentation("input", false)).toEqual({ icon: "check-circle", title: "Input resolved", badge: "Resolved", tone: "muted" });
	});

	it("gives longer native approval actions room without letting them dominate the row", () => {
		expect(actionControlWidth("Allow once")).toBeGreaterThan(actionControlWidth("Skip"));
		expect(actionControlWidth("Next", true)).toBeGreaterThanOrEqual(76);
		expect(actionControlWidth("A very long provider-owned permission decision")).toBeLessThanOrEqual(148);
	});
});
