import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import type { TelemetryPolicySnapshot } from "../shared/telemetry-policy";
import { DesktopTelemetryController } from "./desktop-telemetry-controller";
import { nodeTelemetryPolicyFileSystem, TelemetryPolicyAuthority } from "./telemetry-policy-file";

describe("DesktopTelemetryController", () => {
	it("cancels visibility before disable and advances it with every trusted generation", async () => {
		const authority = new AuthorityFake(true, "generation-on");
		const visibility = { setPolicy: vi.fn(), disableAndDrain: vi.fn().mockResolvedValue(undefined), closeAndDrain: vi.fn().mockResolvedValue(undefined) };
		const transport = { closeAndDrain: vi.fn(), capture: vi.fn(), clearCache: vi.fn() };
		const daemon = {
			prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: "generation-on", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }),
			applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => ({ status: "applied", consentGeneration: generation, eventsEnabled: enabled, gateDrained: !enabled, purgeConfirmed: !enabled })),
		};
		const controller = new DesktopTelemetryController({ authority, daemon, transportFactory: async () => transport, environmentAllowsEvents: true, productionEnabled: true, visibility });
		await controller.initialize();
		await controller.setEventsEnabled(false, "generation-on");
		expect(visibility.setPolicy).toHaveBeenCalledWith(false, "generation-on");
		expect(visibility.setPolicy).toHaveBeenLastCalledWith(false, "generation-1");
		expect(visibility.setPolicy.mock.invocationCallOrder[1]).toBeLessThan(transport.closeAndDrain.mock.invocationCallOrder[0]);
		await controller.close();
		expect(visibility.closeAndDrain).toHaveBeenCalled();
	});

	it("keeps opt-out cleanup pending when any desktop purge fails", async () => {
		const authority = new AuthorityFake(true, "generation-on");
		const transport = { closeAndDrain: vi.fn(), capture: vi.fn(), clearCache: vi.fn().mockRejectedValue(new Error("cache purge failed")) };
		const daemon = { prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: "generation-on", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }), applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => ({ status: "applied", consentGeneration: generation, eventsEnabled: enabled, gateDrained: !enabled, purgeConfirmed: !enabled })) };
		const controller = new DesktopTelemetryController({ authority, daemon, transportFactory: async () => transport, environmentAllowsEvents: true, productionEnabled: true, clearRendererQueues: vi.fn().mockRejectedValue(new Error("queue purge failed")) });
		await controller.initialize();
		const result = await controller.setEventsEnabled(false, "generation-on");
		expect(result).toMatchObject({ eventsEnabled: false, state: "cleanup_pending", acknowledged: false, reason: "cleanup_failed" });
	});

	it("fails closed in memory when the durable off replacement fails", async () => {
		const authority = new AuthorityFake(true, "generation-on");
		authority.failWrites = true;
		const daemon = { prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: "generation-on", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }), applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => ({ status: "applied", consentGeneration: generation, eventsEnabled: enabled, gateDrained: !enabled, purgeConfirmed: !enabled })) };
		const controller = new DesktopTelemetryController({ authority, daemon, transportFactory: async () => ({ closeAndDrain: async () => {}, capture: () => {}, clearCache: async () => {} }), environmentAllowsEvents: true, productionEnabled: true });
		await controller.initialize();
		await expect(controller.setEventsEnabled(false, "generation-on")).rejects.toThrow("write failed");
		expect(controller.snapshot()).toMatchObject({ eventsEnabled: false, acknowledged: false, state: "cleanup_failed" });
	});

	it("retries the durable off generation and preserved desktop purge after a post-rename fsync failure", async () => {
		const dataDir = await mkdtemp(path.join(os.tmpdir(), "ao-controller-policy-"));
		try {
			const enabledGeneration = "7f80c8a9-ec67-4a16-a067-a444ffcc5cca";
			await writeFile(path.join(dataDir, "telemetry_policy.json"), `${JSON.stringify({
				schema_version: 1,
				events_enabled: true,
				consent_generation: enabledGeneration,
				updated_at: "2026-08-28T10:15:30.000Z",
			})}\n`, { mode: 0o600 });
			let failDirectorySync = false;
			const base = nodeTelemetryPolicyFileSystem;
			const authority = new TelemetryPolicyAuthority({
				dataDir,
				packagedDefault: false,
				platform: "linux",
				fs: {
					...base,
					syncDirectory: async (target) => {
						if (failDirectorySync) {
							failDirectorySync = false;
							throw new Error("directory fsync failed");
						}
						await base.syncDirectory(target);
					},
				},
			});
			const transport = { closeAndDrain: vi.fn(), capture: vi.fn(), clearCache: vi.fn() };
			const clearRendererQueues = vi.fn();
			const daemon = {
				prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: enabledGeneration, eventsEnabled: false, gateDrained: true, purgeConfirmed: false }),
				applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => ({ status: "applied", consentGeneration: generation, eventsEnabled: enabled, gateDrained: !enabled, purgeConfirmed: !enabled })),
			};
			const controller = new DesktopTelemetryController({
				authority,
				daemon,
				transportFactory: async () => transport,
				environmentAllowsEvents: true,
				productionEnabled: true,
				clearRendererQueues,
			});
			await controller.initialize();
			failDirectorySync = true;

			await expect(controller.setEventsEnabled(false, enabledGeneration)).rejects.toThrow("directory fsync failed");
			const pending = authority.snapshot();
			expect(pending).toMatchObject({ eventsEnabled: false, acknowledged: false });
			expect(transport.clearCache).not.toHaveBeenCalled();
			expect(clearRendererQueues).not.toHaveBeenCalled();

			const retried = await controller.retryPendingCleanup();

			expect(retried).toMatchObject({
				eventsEnabled: false,
				consentGeneration: pending.consentGeneration,
				acknowledged: true,
				state: "applied",
			});
			expect(authority.snapshot()).toMatchObject({ consentGeneration: pending.consentGeneration, acknowledged: true });
			expect(daemon.applyPolicy).toHaveBeenLastCalledWith(pending.consentGeneration, false);
			expect(transport.clearCache).toHaveBeenCalledOnce();
			expect(clearRendererQueues).toHaveBeenCalledOnce();
		} finally {
			await rm(dataDir, { recursive: true, force: true });
		}
	});

	it("writes one durable off generation even when prepare is unavailable and remains pending without purge acknowledgement", async () => {
		const authority = new AuthorityFake(true, "generation-on");
		const transport = { closeAndDrain: vi.fn(), capture: vi.fn(), clearCache: vi.fn() };
		const daemon = { prepareDisable: vi.fn().mockRejectedValue(new Error("offline")), applyPolicy: vi.fn().mockResolvedValueOnce({ status: "applied", consentGeneration: "generation-on", eventsEnabled: true, gateDrained: false, purgeConfirmed: false }).mockRejectedValue(new Error("offline")) };
		const controller = new DesktopTelemetryController({ authority, daemon, transportFactory: async () => transport, environmentAllowsEvents: true, productionEnabled: true });
		await controller.initialize();
		const result = await controller.setEventsEnabled(false, "generation-on");
		expect(authority.writes).toEqual([false]);
		expect(transport.closeAndDrain.mock.invocationCallOrder[0]).toBeLessThan(authority.writeSpy.mock.invocationCallOrder[0]);
		expect(result).toMatchObject({ eventsEnabled: false, state: "cleanup_pending", acknowledged: false });
	});

	it("rejects stale renderer generations and broadcasts disable then enable to the same subscriber", async () => {
		const authority = new AuthorityFake(false, "generation-off");
		const daemon = {
			prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: "generation-off", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }),
			applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => ({ status: "applied", consentGeneration: generation, eventsEnabled: enabled, gateDrained: !enabled, purgeConfirmed: !enabled })),
		};
		const views: string[] = [];
		const controller = new DesktopTelemetryController({ authority, daemon, transportFactory: async () => ({ closeAndDrain: async () => {}, capture: () => {}, clearCache: async () => {} }), environmentAllowsEvents: true, productionEnabled: true, broadcast: (view) => views.push(view.consentGeneration) });
		await controller.initialize();
		await expect(controller.setEventsEnabled(true, "stale")).rejects.toThrow("stale");
		await controller.setEventsEnabled(true, "generation-off");
		expect(views.at(-1)).toBe("generation-1");
	});

	it("rolls a durable enablement back off when the daemon applies it but its response is lost", async () => {
		const authority = new AuthorityFake(false, "generation-off");
		const visibility = { setPolicy: vi.fn(), disableAndDrain: vi.fn().mockResolvedValue(undefined), closeAndDrain: vi.fn().mockResolvedValue(undefined) };
		let daemonEnabled = false;
		const daemon = {
			prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: "generation-1", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }),
			applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => {
				daemonEnabled = enabled;
				if (enabled) throw new Error("daemon response lost");
				return { status: "applied", consentGeneration: generation, eventsEnabled: false, gateDrained: true, purgeConfirmed: true } as const;
			}),
		};
		const controller = new DesktopTelemetryController({
			authority,
			daemon,
			transportFactory: async () => ({ closeAndDrain: async () => {}, capture: () => {}, clearCache: async () => {} }),
			environmentAllowsEvents: true,
			productionEnabled: true,
			visibility,
		});
		await controller.initialize();

		await expect(controller.setEventsEnabled(true, "generation-off")).rejects.toThrow("daemon response lost");

		expect(authority.writes).toEqual([true, false]);
		expect(daemon.applyPolicy.mock.calls).toEqual([
			["generation-off", false],
			["generation-1", true],
			["generation-2", false],
		]);
		expect(daemonEnabled).toBe(false);
		expect(controller.snapshot()).toMatchObject({ eventsEnabled: false, consentGeneration: "generation-2", acknowledged: true, state: "applied" });
		expect(visibility.setPolicy).toHaveBeenLastCalledWith(false, "generation-2");
	});

	it("rolls a daemon-acknowledged enablement back off when the main transport cannot start", async () => {
		const authority = new AuthorityFake(false, "generation-off");
		const transportFactory = vi.fn().mockRejectedValue(new Error("transport start failed"));
		let daemonEnabled = false;
		const daemon = {
			prepareDisable: vi.fn().mockResolvedValue({ status: "applied", consentGeneration: "generation-1", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }),
			applyPolicy: vi.fn().mockImplementation(async (generation: string, enabled: boolean) => {
				daemonEnabled = enabled;
				return { status: "applied", consentGeneration: generation, eventsEnabled: enabled, gateDrained: !enabled, purgeConfirmed: !enabled } as const;
			}),
		};
		const controller = new DesktopTelemetryController({ authority, daemon, transportFactory, environmentAllowsEvents: true, productionEnabled: true });
		await controller.initialize();

		await expect(controller.setEventsEnabled(true, "generation-off")).rejects.toThrow("transport start failed");

		expect(authority.writes).toEqual([true, false]);
		expect(daemon.applyPolicy.mock.calls).toEqual([
			["generation-off", false],
			["generation-1", true],
			["generation-2", false],
		]);
		expect(daemonEnabled).toBe(false);
		expect(controller.snapshot()).toMatchObject({ eventsEnabled: false, consentGeneration: "generation-2", acknowledged: true, state: "applied" });
	});
});

class AuthorityFake {
	writes: boolean[] = [];
	failWrites = false;
	writeSpy = vi.fn();
	private current: TelemetryPolicySnapshot;
	constructor(enabled: boolean, generation: string) { this.current = { eventsEnabled: enabled, consentGeneration: generation, updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true }; }
	snapshot() { return { ...this.current }; }
	async load() { return this.snapshot(); }
	async setEventsEnabled(enabled: boolean) { this.writes.push(enabled); this.writeSpy(); if (this.failWrites) throw new Error("write failed"); this.current = { eventsEnabled: enabled, consentGeneration: `generation-${this.writes.length}`, updatedAt: "2026-08-28T10:15:31.000Z", acknowledged: true }; return this.snapshot(); }
	async retryPendingReplacement() { if (this.failWrites) throw new Error("write failed"); this.current = { ...this.current, acknowledged: true }; return this.snapshot(); }
	readonly durabilitySupported = true;
}
