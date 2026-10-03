import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionManagementAccumulator } from "./session-management-telemetry";

describe("SessionManagementAccumulator", () => {
	let now = 0;
	let capture: ReturnType<typeof vi.fn<(event: string, properties: Record<string, unknown>) => Promise<void>>>;
	let accumulator: SessionManagementAccumulator;

	beforeEach(() => {
		localStorage.clear();
		now = 1_000;
		capture = vi.fn<(event: string, properties: Record<string, unknown>) => Promise<void>>().mockResolvedValue(undefined);
		accumulator = new SessionManagementAccumulator(localStorage, () => now, capture);
	});

	it("aggregates active time and all six transitions", async () => {
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		now += 1_000;
		accumulator.checkpoint();
		accumulator.setSurface({ kind: "worker", sessionId: "w1" });
		now += 2_000;
		accumulator.checkpoint();
		accumulator.setSurface({ kind: "worker", sessionId: "w2" });
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o2" });
		await accumulator.flush();

		const properties = capture.mock.calls[0][1];
		expect(properties).toMatchObject({
			orchestrator_active_seconds: 1,
			worker_active_seconds: 2,
			transition_orchestrator_to_worker_count: 1,
			transition_orchestrator_same_count: 1,
			transition_orchestrator_switch_count: 1,
			transition_worker_to_orchestrator_count: 1,
			transition_worker_same_count: 1,
			transition_worker_switch_count: 1,
		});
	});

	it("counts manual opens, interaction categories, terminal bursts, and paths", async () => {
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		accumulator.markManualWorkerOpen("w1");
		accumulator.setSurface({ kind: "worker", sessionId: "w1" });
		accumulator.recordInteraction("w1", "chat", "worker");
		accumulator.recordInteraction("w1", "terminal", "worker");
		now += 10_000;
		accumulator.recordInteraction("w1", "terminal", "worker");
		accumulator.recordInteraction("w1", "lifecycle", "worker");
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		accumulator.setSurface({ kind: "worker", sessionId: "w1" });
		accumulator.setSurface({ kind: "worker", sessionId: "w2" });
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		await accumulator.flush();

		expect(capture.mock.calls[0][1]).toMatchObject({
			manual_worker_open_count: 1,
			direct_worker_chat_steer_count: 1,
			direct_worker_terminal_input_burst_count: 1,
			direct_worker_lifecycle_action_count: 1,
			pattern_orchestrator_worker_orchestrator_count: 1,
			pattern_orchestrator_multiple_workers_count: 1,
		});
	});

	it("restores unflushed aggregate state without exporting raw ids", async () => {
		accumulator.setSurface({ kind: "worker", sessionId: "private-worker" });
		accumulator.recordInteraction("private-worker", "chat", "worker");
		const restored = new SessionManagementAccumulator(localStorage, () => now, capture);
		await restored.flush();

		const exported = JSON.stringify(capture.mock.calls[0][1]);
		expect(exported).not.toContain("private-worker");
		expect(capture.mock.calls[0][1].direct_worker_chat_steer_count).toBe(1);
	});

	it("expires a persisted manual-open intent", async () => {
		accumulator.markManualWorkerOpen("w1");
		now += 10_001;
		const restored = new SessionManagementAccumulator(localStorage, () => now, capture);
		restored.setSurface({ kind: "worker", sessionId: "w1" });
		await restored.flush();
		expect(capture.mock.calls[0][1].manual_worker_open_count).toBe(0);
	});

	it("carries an unresolved path across an aggregate flush", async () => {
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		accumulator.setSurface({ kind: "worker", sessionId: "w1" });
		await accumulator.flush();
		accumulator.setSurface({ kind: "orchestrator", sessionId: "o1" });
		await accumulator.flush();
		expect(capture.mock.calls[1][1].pattern_orchestrator_worker_orchestrator_count).toBe(1);
	});
});
