import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(),
	setItemAsync: vi.fn(),
	deleteItemAsync: vi.fn(),
}));

import type { Endpoint } from "./endpoints";
import { pairFromCode } from "./pairFlow";
import { encodePairingCode } from "./pairingCode";

const lan: Endpoint = { kind: "lan", host: "192.168.1.42", port: 3011, secure: false };
const tunnel: Endpoint = { kind: "tunnel", host: "abc.trycloudflare.com", port: 443, secure: true };

const code = encodePairingCode({
	v: 2,
	hostId: "h_paired",
	name: "prasad-mbp",
	platform: "darwin",
	endpoints: [lan, tunnel],
	token: "pw",
});

const deps = (over: Record<string, unknown> = {}) => ({
	race: vi.fn(async () => ({ ok: true as const, endpoint: lan, hostId: "h_paired" })),
	verify: vi.fn(async () => {}),
	saveHost: vi.fn(async () => {}),
	setActiveHost: vi.fn(async () => {}),
	...over,
});

describe("pairFromCode", () => {
	it("stores the machine with every endpoint the code carried", async () => {
		const d = deps();
		const got = await pairFromCode(`aomobile://pair#${code}`, d);

		expect(got.ok).toBe(true);
		expect(d.saveHost).toHaveBeenCalledWith(
			expect.objectContaining({
				id: "h_paired",
				name: "prasad-mbp",
				endpoints: [lan, tunnel],
				token: "pw",
			}),
		);
	});

	it("returns a config for the endpoint that won the race", async () => {
		const got = await pairFromCode(`aomobile://pair#${code}`, deps());

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.config.host).toBe("192.168.1.42");
	});

	it("makes the newly scanned machine active", async () => {
		const d = deps();
		await pairFromCode(`aomobile://pair#${code}`, d);

		expect(d.setActiveHost).toHaveBeenCalledWith("h_paired");
	});

	// Verifying before storing is the existing behaviour and worth keeping: a
	// pairing that is saved without being proven leaves the app looking
	// connected to something it has never reached.
	it("verifies the winning endpoint before storing anything", async () => {
		const order: string[] = [];
		const d = deps({
			verify: vi.fn(async () => void order.push("verify")),
			saveHost: vi.fn(async () => void order.push("save")),
		});

		await pairFromCode(`aomobile://pair#${code}`, d);

		expect(order).toEqual(["verify", "save"]);
	});

	it("stores nothing when verification fails", async () => {
		const d = deps({
			verify: vi.fn(async () => {
				throw new Error("401");
			}),
		});
		const got = await pairFromCode(`aomobile://pair#${code}`, d);

		expect(got.ok).toBe(false);
		if (!got.ok) expect(got.reason).toBe("verify-failed");
		expect(d.saveHost).not.toHaveBeenCalled();
	});

	it("reports an unusable code rather than throwing", async () => {
		const got = await pairFromCode("definitely not a pairing code", deps());

		expect(got.ok).toBe(false);
		if (!got.ok) expect(got.reason).toBe("not-ao-qr");
	});

	it("reports when none of the code's endpoints answer", async () => {
		const d = deps({ race: vi.fn(async () => ({ ok: false as const, reason: "none-reachable" as const })) });
		const got = await pairFromCode(`aomobile://pair#${code}`, d);

		expect(got.ok).toBe(false);
		if (!got.ok) expect(got.reason).toBe("none-reachable");
	});

	// v1 is no longer accepted. Those codes predate the identity probe the race
	// uses, so a daemon old enough to emit one answers 404 to every probe and
	// the race could never complete — the app now says to update the desktop
	// rather than failing obscurely. See isLegacyPairingCode.
	it("refuses a legacy v1 code", async () => {
		const d = deps();
		const got = await pairFromCode(
			JSON.stringify({ v: 1, host: "192.168.1.42", port: 3011, password: "old" }),
			d,
		);

		expect(got.ok).toBe(false);
		expect(d.saveHost).not.toHaveBeenCalled();
	});
});

// The reason must not depend on which caller reached pairFromCode: the scan
// screen checks the code itself before calling, but any other entry point (a
// deep link, a paste) would otherwise get "not an AO code" for a code AO made.
describe("pairFromCode reasons", () => {
	it("reports an outdated desktop rather than an unrecognised code", async () => {
		const got = await pairFromCode(JSON.stringify({ v: 1, host: "192.168.1.42", port: 3011 }), deps());
		expect(got.ok).toBe(false);
		if (!got.ok) expect(got.reason).toBe("outdated-desktop");
	});

	it("still reports genuinely unrecognised input as not an AO code", async () => {
		const got = await pairFromCode("hello world", deps());
		expect(got.ok).toBe(false);
		if (!got.ok) expect(got.reason).toBe("not-ao-qr");
	});
});
