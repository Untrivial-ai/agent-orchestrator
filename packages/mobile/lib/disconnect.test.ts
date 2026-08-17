import { beforeEach, describe, expect, it, vi } from "vitest";

// The modules under the seam all reach into native storage, so they're mocked
// out: what's worth asserting here is the order and completeness of the steps,
// not their implementations (which have their own coverage).
const calls: string[] = [];
// Lets a test make the push step fail the way a SecureStore write can.
let unpairThrows: Error | null = null;

vi.mock("./push", () => ({
	// forgetServer unpairs rather than merely unregistering: the phone is leaving
	// this daemon, so its row must go, not just its push token. Unregistering
	// would leave the old desktop listing a phone that has moved on.
	unpairFromServer: vi.fn(async () => {
		calls.push("unpairFromServer");
		if (unpairThrows) throw unpairThrows;
	}),
}));
vi.mock("./config", () => ({
	clearConfig: vi.fn(async () => {
		calls.push("clearConfig");
	}),
}));
vi.mock("./onboardingStore", () => ({
	clearOnboardingSkipped: vi.fn(async () => {
		calls.push("clearOnboardingSkipped");
	}),
}));

const { forgetServer } = await import("./disconnect");

describe("forgetServer", () => {
	beforeEach(() => {
		calls.length = 0;
		unpairThrows = null;
	});

	// Clearing only the config would leave the daemon still pushing to this
	// device, still listing it as paired, and leave the password in the keystore.
	it("unpairs, clears the config, and re-arms onboarding", async () => {
		await forgetServer();
		expect(calls).toEqual(["unpairFromServer", "clearConfig", "clearOnboardingSkipped"]);
	});

	// The unpair call needs credentials that clearConfig would otherwise destroy,
	// so the ordering is load-bearing, not incidental.
	it("unpairs before the credentials are thrown away", async () => {
		await forgetServer();
		expect(calls.indexOf("unpairFromServer")).toBeLessThan(calls.indexOf("clearConfig"));
	});

	// unpairFromServer catches its own network failures, but its SecureStore
	// writes are unguarded. A throw there used to abort the disconnect with the
	// host and password still on disk — the phone looked disconnected and was not.
	it("still clears credentials when the push step throws", async () => {
		unpairThrows = new Error("SecureStore unavailable");
		await expect(forgetServer()).rejects.toThrow("SecureStore unavailable");
		expect(calls).toContain("clearConfig");
		expect(calls).toContain("clearOnboardingSkipped");
	});
});
