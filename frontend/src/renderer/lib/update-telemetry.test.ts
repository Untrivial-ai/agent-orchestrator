import { describe, expect, it, vi } from "vitest";
import type { UpdateOutcome } from "../../shared/update-telemetry";
const { onTelemetry, captureRendererEvent } = vi.hoisted(() => ({
	onTelemetry: vi.fn(), captureRendererEvent: vi.fn(),
}));
vi.mock("./bridge", () => ({ aoBridge: { updates: { onTelemetry } } }));
vi.mock("./telemetry", () => ({ captureRendererEvent }));
import { startUpdateTelemetry } from "./update-telemetry";

describe("update transfer telemetry bridge", () => {
	it("forwards transfer facts through the existing capture path", () => {
		const off = vi.fn();
		onTelemetry.mockReturnValue(off);
		const stop = startUpdateTelemetry();
		const callback = onTelemetry.mock.calls[0][0] as (outcome: UpdateOutcome) => void;
		callback({
			event: "ao.renderer.update_downloaded", phase: "download", trigger: "manual",
			to_version: "2.0.0", differential_eligible: true, transfer_mode: "differential",
			fallback: true, transferred_bytes: 100, target_bytes: 500,
		});
		expect(captureRendererEvent).toHaveBeenCalledWith("ao.renderer.update_downloaded", {
			phase: "download", trigger: "manual", to_version: "2.0.0", error_category: undefined,
			differential_eligible: true, transfer_mode: "differential", fallback: true,
			transferred_bytes: 100, target_bytes: 500,
		});
		stop();
		expect(off).toHaveBeenCalledOnce();
	});
});
