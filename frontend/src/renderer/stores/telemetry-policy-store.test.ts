import { beforeEach, describe, expect, it, vi } from "vitest";
const bridge = vi.hoisted(() => ({ getPolicy: vi.fn(), setEventsEnabled: vi.fn(), onPolicy: vi.fn() }));
vi.mock("../lib/bridge", () => ({ aoBridge: { telemetry: { ...bridge, capture: vi.fn(), getBootstrap: vi.fn() } } }));
import { useTelemetryPolicyStore } from "./telemetry-policy-store";

const offView = { eventsEnabled: false, consentGeneration: "generation-off", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true, state: "applied" as const, environmentVeto: false, durabilitySupported: true };

describe("telemetry policy store", () => {
	beforeEach(() => {
		bridge.getPolicy.mockReset().mockResolvedValue(offView); bridge.setEventsEnabled.mockReset(); bridge.onPolicy.mockReset().mockReturnValue(() => undefined);
		useTelemetryPolicyStore.setState({ view: null, loaded: false, saving: false, saveError: false });
	});
	it("loads and subscribes to the main-owned mutable policy", async () => {
		let listener: ((view: typeof offView) => void) | undefined;
		bridge.onPolicy.mockImplementation((next) => { listener = next; return () => undefined; });
		await useTelemetryPolicyStore.getState().load();
		listener?.({ ...offView, eventsEnabled: true, consentGeneration: "generation-on" });
		expect(useTelemetryPolicyStore.getState().view?.consentGeneration).toBe("generation-on");
	});
	it("does not claim enablement when main reports cleanup pending", async () => {
		bridge.setEventsEnabled.mockResolvedValue({ ...offView, state: "cleanup_pending", acknowledged: false });
		await useTelemetryPolicyStore.getState().setEnabled(true);
		expect(useTelemetryPolicyStore.getState()).toMatchObject({ saving: false, saveError: false, view: { eventsEnabled: false, state: "cleanup_pending" } });
	});
});
