import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

import { DEFAULT_CONFIG, type ServerConfig } from "./config";
import type { Endpoint } from "./endpoints";
import type { Host } from "./hosts";
import { resolveActiveConfig } from "./resolveConfig";
import { IncompatibleHostVersionError } from "./race";

const lan: Endpoint = { kind: "lan", host: "192.168.1.42", port: 3011, secure: false };
const tunnel: Endpoint = { kind: "tunnel", host: "abc.trycloudflare.com", port: 443, secure: true };

const host = (over: Partial<Host> = {}): Host => ({
	id: "h_a", name: "mbp", platform: "darwin",
	endpoints: [lan, tunnel], token: "pw", lastConnected: 2, ...over,
});

const legacy: ServerConfig = { ...DEFAULT_CONFIG, host: "10.0.0.9", httpPort: "3011", password: "old" };

const deps = (over: Record<string, unknown> = {}) => ({
	migrate: vi.fn(async () => {}),
	activeHost: vi.fn(async () => host()),
	connect: vi.fn(async (id: string) => ({
		ok: true as const,
		config: { ...DEFAULT_CONFIG, host: "192.168.1.42", httpPort: "3011", password: "pw" },
		endpoint: lan,
		hostId: id,
	})),
	loadLegacyConfig: vi.fn(async () => legacy),
	persist: vi.fn(async () => {}),
	...over,
});

describe("resolveActiveConfig", () => {
	// The terminal and other long-lived surfaces read the persisted config
	// directly rather than the store's copy. Without writing the winner back,
	// a phone that raced onto Tailscale after losing Wi-Fi kept pointing them
	// at the dead LAN address: REST recovered, the terminal did not.
	it("persists the endpoint that won so other surfaces follow", async () => {
		const d = deps();
		await resolveActiveConfig(d);

		expect(d.persist).toHaveBeenCalledWith(
			expect.objectContaining({ host: "192.168.1.42" }),
		);
	});

	it("does not persist anything when the race fails", async () => {
		const d = deps({ connect: vi.fn(async () => ({ ok: false as const, reason: "none-reachable" as const })) });
		await resolveActiveConfig(d);

		expect(d.persist).not.toHaveBeenCalled();
	});

	it("keeps incompatible host failures distinct from offline without using a cached address", async () => {
		const d = deps({ connect: vi.fn(async () => ({ ok: false as const, reason: "incompatible" as const })) });
		await expect(resolveActiveConfig(d)).rejects.toBeInstanceOf(IncompatibleHostVersionError);
		expect(d.persist).not.toHaveBeenCalled();
		expect(d.loadLegacyConfig).not.toHaveBeenCalled();
	});

	it("rejects a host upgrade on reconnect even after a working v1 connection", async () => {
		const working = deps().connect;
		const connect = vi.fn()
			.mockImplementationOnce(working)
			.mockResolvedValueOnce({ ok: false as const, reason: "incompatible" as const });
		const d = deps({ connect });
		await expect(resolveActiveConfig(d)).resolves.toMatchObject({ host: "192.168.1.42" });
		await expect(resolveActiveConfig(d)).rejects.toBeInstanceOf(IncompatibleHostVersionError);
		expect(connect).toHaveBeenCalledTimes(2);
		expect(d.persist).toHaveBeenCalledTimes(1);
	});

	// Which machine that is belongs to hosts.activeHost — an explicit selection
	// where one has been made, most-recent otherwise. Resolution just asks.
	it("connects to whichever machine is active", async () => {
		const d = deps({ activeHost: vi.fn(async () => host({ id: "h_chosen" })) });
		const got = await resolveActiveConfig(d);

		expect(d.connect).toHaveBeenCalledWith("h_chosen");
		expect(got?.host).toBe("192.168.1.42");
	});

	// Migration runs first, so a user upgrading from the single-server config
	// has their machine in the list before we look for one.
	it("migrates an old pairing before looking for machines", async () => {
		const order: string[] = [];
		const d = deps({
			migrate: vi.fn(async () => void order.push("migrate")),
			activeHost: vi.fn(async () => {
				order.push("load");
				return host();
			}),
		});
		await resolveActiveConfig(d);

		expect(order).toEqual(["migrate", "load"]);
	});

	it("does not reuse an unverified saved address when the selected machine is offline", async () => {
		const d = deps({
			connect: vi.fn(async () => ({ ok: false as const, reason: "none-reachable" as const })),
			loadLegacyConfig: vi.fn(async () => ({ ...legacy, hostId: "h_a" })),
		});
		expect(await resolveActiveConfig(d)).toBeNull();
	});

	it("never reconnects to A when selected B is offline", async () => {
		const d = deps({
			activeHost: vi.fn(async () => host({ id: "h_b" })),
			connect: vi.fn(async () => ({ ok: false as const, reason: "none-reachable" as const })),
			loadLegacyConfig: vi.fn(async () => ({ ...legacy, hostId: "h_a" })),
		});

		expect(await resolveActiveConfig(d)).toBeNull();
	});

	it("falls back to the stored config when no machine is paired", async () => {
		const d = deps({ activeHost: vi.fn(async () => null) });
		const got = await resolveActiveConfig(d);

		expect(d.connect).not.toHaveBeenCalled();
		expect(got?.host).toBe("10.0.0.9");
	});

	it("does not use another machine's stored config if resolution throws", async () => {
		const d = deps({
			activeHost: vi.fn(async () => host({ id: "h_b" })),
			loadLegacyConfig: vi.fn(async () => ({ ...legacy, hostId: "h_a" })),
			connect: vi.fn(async () => {
				throw new Error("boom");
			}),
		});
		expect(await resolveActiveConfig(d)).toBeNull();
	});
});
