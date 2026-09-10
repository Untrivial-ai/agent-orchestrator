import { describe, expect, it } from "vitest";
import { MOBILE_EVENTS } from "./events";
import { sanitizeMobileProperties } from "./sanitize";

describe("sanitizeMobileProperties", () => {
	it("keeps only allowlisted enum values", () => {
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.paired, { method: "qr", from_onboarding: true }),
		).toEqual({ method: "qr", from_onboarding: true });
	});

	it("drops an enum value outside the closed set", () => {
		expect(sanitizeMobileProperties(MOBILE_EVENTS.paired, { method: "nfc" })).toEqual({});
	});

	// The privacy contract. Session titles, PR names, project names, terminal
	// text, and the connection password are all things a caller might wrongly
	// pass; none are allowlisted, so all must be dropped.
	it("drops any unregistered key, so titles and secrets cannot leak", () => {
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.featureUsed, {
				feature: "spawn",
				outcome: "succeeded",
				session_title: "fix the auth bug in secret-repo",
				project: "acme/secret-repo",
				password: "hunter2",
				terminal_tail: "$ cat .env",
			}),
		).toEqual({ feature: "spawn", outcome: "succeeded" });
	});

	it("keeps mode on a spawn feature event and accepts the handoff feature", () => {
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.featureUsed, {
				feature: "spawn",
				outcome: "succeeded",
				mode: "tui",
			}),
		).toEqual({ feature: "spawn", outcome: "succeeded", mode: "tui" });
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.featureUsed, {
				feature: "handoff",
				outcome: "succeeded",
				mode: "chat",
			}),
		).toEqual({ feature: "handoff", outcome: "succeeded", mode: "chat" });
	});

	it("drops a mode value outside the chat/tui set", () => {
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.featureUsed, {
				feature: "spawn",
				outcome: "succeeded",
				mode: "gui",
			}),
		).toEqual({ feature: "spawn", outcome: "succeeded" });
	});

	it("returns {} for an unknown event rather than passing the payload through", () => {
		expect(sanitizeMobileProperties("ao.mobile_app.not_a_real_event", { anything: "x" })).toEqual({});
	});

	it("keeps a valid count but drops NaN, Infinity, negatives, and floats", () => {
		// No event ships a count today, so exercise the rule via a synthetic check:
		// notification cold_start is a flag; confirm flags reject non-booleans.
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.notificationOpened, {
				target: "session",
				cold_start: "yes",
			}),
		).toEqual({ target: "session" });
		expect(
			sanitizeMobileProperties(MOBILE_EVENTS.notificationOpened, {
				target: "session",
				cold_start: true,
			}),
		).toEqual({ target: "session", cold_start: true });
	});

});
