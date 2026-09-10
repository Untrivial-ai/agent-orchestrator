import { describe, expect, it, vi } from "vitest";
import type { ServerConfig } from "./config";
import { adoptManualConnection } from "./manualConnect";

// A literal rather than DEFAULT_CONFIG: config.ts reaches native storage at
// import time, and this is pure logic that needs none of it.
const cfg: ServerConfig = {
	host: "192.168.1.42",
	httpPort: "3011",
	muxPort: "14801",
	secure: false,
	password: "pw",
};

function deps(over: Partial<Parameters<typeof adoptManualConnection>[1]> = {}) {
	return {
		identity: vi.fn(async () => "h_manual"),
		saveHost: vi.fn(async () => {}),
		setActiveHost: vi.fn(async () => {}),
		...over,
	};
}

// The bug: ManualConnectSheet wrote only the legacy ServerConfig. Once a host
// list exists, migration skips and resolution reconnects the active host — so a
// manual connection was silently replaced by the previous machine on reload.
describe("adopting a manual connection", () => {
	it("stores the machine so it survives a reload", async () => {
		const d = deps();

		await adoptManualConnection(cfg, d);

		expect(d.saveHost).toHaveBeenCalledWith(
			expect.objectContaining({
				id: "h_manual",
				token: "pw",
				endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			}),
		);
	});

	it("makes it the active machine, not merely another entry", async () => {
		const d = deps();
		await adoptManualConnection(cfg, d);
		expect(d.setActiveHost).toHaveBeenCalledWith("h_manual");
	});

	// A TLS address entered by hand is the tailnet path; plain is the LAN.
	it("records a secure address as a tailscale endpoint", async () => {
		const d = deps();
		await adoptManualConnection({ ...cfg, secure: true, httpPort: "443" }, d);
		expect(d.saveHost).toHaveBeenCalledWith(
			expect.objectContaining({
				endpoints: [{ kind: "tailscale", host: "192.168.1.42", port: 443, secure: true }],
			}),
		);
	});

	// An older daemon has no identity probe. The machine is still worth storing
	// — it just stays unverified until it reports an id, exactly as a migrated
	// pairing does.
	it("stores the machine even when it reports no identity", async () => {
		const d = deps({ identity: vi.fn(async () => "") });

		const id = await adoptManualConnection(cfg, d);

		expect(d.saveHost).toHaveBeenCalledWith(expect.objectContaining({ id: "" }));
		expect(id).toBe("");
	});
});
