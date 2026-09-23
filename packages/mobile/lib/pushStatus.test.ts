import { describe, expect, it } from "vitest";
import {
	classifyServerFailure,
	describePushToggle,
	describeRegisterFailure,
	hasServer,
	type PushStatus,
} from "./pushStatus";

const status = (over: Partial<PushStatus> = {}): PushStatus => ({
	supported: true,
	granted: true,
	canAskAgain: true,
	registered: false,
	...over,
});

// Push is one switch in Settings, so the state machine has to answer: where
// does the switch sit, and can the user move it?
describe("describePushToggle", () => {
	const server = { host: "192.168.1.5" };

	it("is off and disabled while the status is still loading", () => {
		const t = describePushToggle(null, server);
		expect(t.value).toBe(false);
		expect(t.disabled).toBe(true);
	});

	it("is disabled on a simulator, where no token can be minted", () => {
		const t = describePushToggle(status({ supported: false }), server);
		expect(t.disabled).toBe(true);
		expect(t.footer).toMatch(/physical device/i);
	});

	// Regression: an unpaired app still holds a config object with an empty host,
	// so `!!config` is truthy. Turning the switch on there could only burn the
	// one-shot OS permission prompt and then fail against an empty host.
	it("is disabled with no server paired, since there is nothing to register with", () => {
		const t = describePushToggle(status({ registered: true }), { host: "" });
		expect(t.value).toBe(false);
		expect(t.disabled).toBe(true);
		expect(t.footer).toMatch(/connect to your ao server/i);
	});

	it("reads on only when permission is granted and the device is registered", () => {
		expect(describePushToggle(status({ registered: true }), server).value).toBe(true);
		expect(describePushToggle(status({ registered: false }), server).value).toBe(false);
		expect(describePushToggle(status({ granted: false, registered: true }), server).value).toBe(false);
	});

	it("stays interactive when granted but not yet registered", () => {
		const t = describePushToggle(status({ registered: false }), server);
		expect(t.disabled).toBe(false);
		expect(t.blocked).toBe(false);
		expect(t.footer).toMatch(/isn't registered/i);
	});

	it("offers a normal turn-on when permission has not been asked for yet", () => {
		const t = describePushToggle(status({ granted: false }), server);
		expect(t.disabled).toBe(false);
		expect(t.blocked).toBe(false);
	});

	// Blocked must stay tappable: the tap is the only chance to explain that the
	// switch can now only be moved from system settings.
	it("marks a permanent denial as blocked but leaves the switch tappable", () => {
		const t = describePushToggle(status({ granted: false, canAskAgain: false }), server);
		expect(t.blocked).toBe(true);
		expect(t.disabled).toBe(false);
		expect(t.footer).toMatch(/system settings/i);
	});

	// A permanent denial outranks a leftover registration: the switch must never
	// read "on" when the OS will not deliver anything.
	it("reports blocked even if a stale registration is still held", () => {
		const t = describePushToggle(status({ granted: false, canAskAgain: false, registered: true }), server);
		expect(t.value).toBe(false);
		expect(t.blocked).toBe(true);
	});
});

describe("hasServer", () => {
	it("treats a missing, empty, or whitespace host as no server", () => {
		expect(hasServer(null)).toBe(false);
		expect(hasServer(undefined)).toBe(false);
		expect(hasServer({})).toBe(false);
		expect(hasServer({ host: "" })).toBe(false);
		expect(hasServer({ host: "   " })).toBe(false);
	});

	it("treats a real host as a server", () => {
		expect(hasServer({ host: "192.168.1.5" })).toBe(true);
	});
});

describe("classifyServerFailure", () => {
	// Only a request that never got an answer is genuinely "unreachable".
	it("reports unreachable only when there was no response at all", () => {
		expect(classifyServerFailure(undefined)).toBe("server-unreachable");
	});

	it("separates auth rejection, rate limiting, and other error statuses", () => {
		expect(classifyServerFailure(401)).toBe("server-auth");
		expect(classifyServerFailure(403)).toBe("server-auth");
		expect(classifyServerFailure(429)).toBe("server-rate-limited");
		expect(classifyServerFailure(500)).toBe("server-error");
		expect(classifyServerFailure(404)).toBe("server-error");
	});
});

describe("describeRegisterFailure", () => {
	// Regression: a user whose server was simply unreachable was told their build
	// "has no push entitlement", which was false and alarming.
	it("blames the server, not the build, when the daemon is unreachable", () => {
		const { title, message } = describeRegisterFailure("server-unreachable", "ios");
		expect(title).toMatch(/couldn't reach your ao server/i);
		expect(`${title} ${message}`).not.toMatch(/entitlement/i);
	});

	// Names the fix, not the cause: "no push entitlement" is a fact about the
	// build that the person holding the phone can do nothing with, and the app
	// is now a public listing they can reinstall from.
	it("sends an iOS user to the App Store when the token itself failed", () => {
		const { message } = describeRegisterFailure("token-failed", "ios");
		expect(message).toMatch(/app store/i);
		expect(message).not.toMatch(/entitlement|testflight/i);
	});

	it("does not send an Android user to the App Store", () => {
		const { message } = describeRegisterFailure("token-failed", "android");
		expect(message).not.toMatch(/entitlement|app store/i);
	});

	it("points at system settings when permission was denied", () => {
		const { message } = describeRegisterFailure("denied", "ios");
		expect(message).toMatch(/system settings/i);
	});

	// Regression: every non-2xx answer used to funnel into "server-unreachable",
	// telling someone with a wrong password to go check their network.
	it("does not claim the server was unreachable when it answered and rejected us", () => {
		for (const reason of ["server-auth", "server-rate-limited", "server-error"] as const) {
			const { title, message } = describeRegisterFailure(reason, "ios");
			expect(`${title} ${message}`).not.toMatch(/couldn't reach/i);
		}
	});

	it("points at the password when the server rejected the credentials", () => {
		const { message } = describeRegisterFailure("server-auth", "ios");
		expect(message).toMatch(/password/i);
	});

	it("names the HTTP status when the server errored, and omits it when unknown", () => {
		expect(describeRegisterFailure("server-error", "ios", 500).message).toMatch(/HTTP 500/);
		expect(describeRegisterFailure("server-error", "ios").message).not.toMatch(/HTTP/);
	});

	it("tells an unpaired user to connect rather than blaming the build or network", () => {
		const { title, message } = describeRegisterFailure("not-configured", "ios");
		expect(title).toMatch(/connect to your ao server/i);
		expect(`${title} ${message}`).not.toMatch(/entitlement|couldn't reach/i);
	});

	it("covers the remaining reasons with a usable message", () => {
		for (const reason of ["no-project-id", "unsupported"] as const) {
			const { title, message } = describeRegisterFailure(reason, "ios");
			expect(title.length).toBeGreaterThan(0);
			expect(message.length).toBeGreaterThan(0);
		}
	});
});
