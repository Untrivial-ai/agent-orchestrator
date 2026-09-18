import { describe, expect, it } from "vitest";
import { can, type ConversationSnapshot } from "./conversation";

/**
 * Gating a control on an advertised capability, rather than on a refusal.
 *
 * Without this a client learns that a harness cannot steer by drawing the
 * control, taking the press, and withdrawing it — which reads as a bug rather
 * than as a difference between harnesses. The distinction that matters is
 * "not known yet" versus "cannot": both hide the control, but only the second is
 * permanent, and conflating them would either flash a control on every load or
 * hide one that works.
 */
const base: ConversationSnapshot = {
	conversationId: "conv-1",
	sessionId: "s-1",
	harness: "opencode",
	mode: "chat",
	controller: { state: "ready" },
	latestSequence: 0,
	oldestSequence: 1,
	hasMoreBefore: false,
	settings: {},
	turns: [],
	items: [],
};

describe("can", () => {
	it("reports an advertised capability", () => {
		expect(can({ ...base, capabilities: ["steer", "compaction"] }, "steer")).toBe(true);
	});

	it("refuses one the provider did not advertise", () => {
		expect(can({ ...base, capabilities: ["compaction"] }, "steer")).toBe(false);
	});

	// An unstarted controller advertises nothing. That is not permission.
	it("refuses everything when no capabilities have been reported", () => {
		expect(can(base, "steer")).toBe(false);
	});

	// A daemon that knows about capabilities this client does not must not break it,
	// which is why the wire shape is an open list rather than a fixed set of flags.
	it("ignores capabilities it does not recognize", () => {
		const snapshot = { ...base, capabilities: ["steer", "something_invented_later"] };
		expect(can(snapshot, "steer")).toBe(true);
		expect(can(snapshot, "compaction")).toBe(false);
	});
});
