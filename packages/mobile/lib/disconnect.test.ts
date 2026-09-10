import { beforeEach, describe, expect, it, vi } from "vitest";

// The modules under the seam all reach into native storage, so they're mocked
// out: what's worth asserting here is the order and completeness of the steps,
// not their implementations (which have their own coverage).
const calls: string[] = [];
// Lets a test make the push step fail the way a SecureStore write can.
let unpairThrows: Error | null = null;
let removeHostThrows: Error | null = null;
let activeHostThrows: Error | null = null;

const active = {
	id: "h_active",
	name: "active",
	platform: "darwin",
	endpoints: [{ kind: "lan" as const, host: "192.168.1.42", port: 3011, secure: false }],
	lastConnected: 1,
};

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
vi.mock("./hosts", () => ({
	activeHostMetadata: vi.fn(async () => {
		if (activeHostThrows) throw activeHostThrows;
		return active;
	}),
	removeHost: vi.fn(async (id: string) => {
		calls.push(`removeHost:${id}`);
		if (removeHostThrows) throw removeHostThrows;
	}),
}));
vi.mock("./chat/eventCursor", () => ({
	clearEventCursorsForHost: vi.fn(async (host: typeof active) => {
		calls.push(`clearEventCursor:${host.id}`);
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
		removeHostThrows = null;
		activeHostThrows = null;
	});

	// Clearing only the config would leave the daemon still pushing to this
	// device, still listing it as paired, and leave the password in the keystore.
	it("unpairs, clears the config, and re-arms onboarding", async () => {
		await forgetServer();
		expect(calls).toEqual([
			"unpairFromServer",
			"removeHost:h_active",
			"clearEventCursor:h_active",
			"clearConfig",
			"clearOnboardingSkipped",
		]);
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

	it("still performs safe local cleanup when active-host lookup throws", async () => {
		activeHostThrows = new Error("SecureStore unavailable");

		await expect(forgetServer()).rejects.toThrow("SecureStore unavailable");
		expect(calls).toContain("unpairFromServer");
		expect(calls).toContain("clearConfig");
		expect(calls).toContain("clearOnboardingSkipped");
	});
});

// The bug this covers: forgetting cleared only the legacy config, so the host
// record and its token in ao.hostToken.<id> survived. resolveActiveConfig then
// raced that machine's endpoints on the next launch and silently reconnected to
// the server the user had just forgotten.
describe("forgetServer and the host list", () => {
	beforeEach(() => {
		calls.length = 0;
		unpairThrows = null;
		removeHostThrows = null;
		activeHostThrows = null;
	});

	it("removes the paired machine, not just the resolved address", async () => {
		await forgetServer();
		expect(calls).toContain("removeHost:h_active");
	});

	// Same reasoning as the existing finally: whatever fails upstream, the
	// pairing must not survive a disconnect.
	it("removes the machine even when unpairing fails", async () => {
		unpairThrows = new Error("daemon unreachable");

		// The error still surfaces — the caller decides what to show — but the
		// pairing is gone regardless, which is the whole point of the finally.
		await expect(forgetServer()).rejects.toThrow("daemon unreachable");

		expect(calls).toContain("removeHost:h_active");
		expect(calls).toContain("clearConfig");
	});

	it("finishes local cleanup when removing the host token fails", async () => {
		removeHostThrows = new Error("SecureStore unavailable");

		await expect(forgetServer()).rejects.toThrow("SecureStore unavailable");
		expect(calls).toContain("clearEventCursor:h_active");
		expect(calls).toContain("clearConfig");
		expect(calls).toContain("clearOnboardingSkipped");
	});
});
