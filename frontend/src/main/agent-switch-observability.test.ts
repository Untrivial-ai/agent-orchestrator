import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	AgentSwitchVisibilityController,
	createAgentSwitchVisibilitySender,
} from "./agent-switch-observability";
import type { AgentSwitchVisibilitySignalBody } from "../shared/agent-switch-observability";

const metadata = { release: "1.2.3", environment: "stable", channel: "stable", os: "darwin" } as const;

describe("AgentSwitchVisibilityController", () => {
	beforeEach(() => vi.useFakeTimers());

	it.each([["active", 15_000], ["history", 60_000]] as const)("reports %s transport failure only at the exact grace", async (operation, grace) => {
		const send = vi.fn();
		const controller = new AgentSwitchVisibilityController({ send });
		controller.setPolicy(true, "generation-1");
		controller.registerWindow(1); controller.signal(1, signal("focus", true)); controller.signal(1, signal("online", true));
		controller.signal(1, health("transport", operation, false));
		await vi.advanceTimersByTimeAsync(grace - 1); expect(send).not.toHaveBeenCalled();
		await vi.advanceTimersByTimeAsync(1); expect(send).toHaveBeenCalledTimes(1);
		expect(send.mock.calls[0][0]).toMatchObject({ failurePoint: "visibility_transport", operation });
	});

	it("lets transport own an outage and suppresses the query incident", async () => {
		const send = vi.fn(); const controller = harness(send);
		controller.signal(1, health("query", "active", false));
		controller.signal(1, health("transport", "active", false));
		await vi.advanceTimersByTimeAsync(15_000);
		expect(send).toHaveBeenCalledTimes(1);
		expect(send.mock.calls[0][0].failurePoint).toBe("visibility_transport");
	});

	it("reports a still-current missing presentation after two seconds without local values", async () => {
		const send = vi.fn(); const controller = harness(send);
		controller.signal(1, signalBody({ kind: "expected_presentation", token: "local-token-sentinel", switchId: "local-switch-sentinel", updatedAt: "2026-08-28T00:00:00Z", localRouteKey: "local-route-sentinel", presentationKind: "recovery_required", durableState: "starting_target" }));
		await vi.advanceTimersByTimeAsync(2_000);
		expect(send.mock.calls[0][0]).toMatchObject({ failurePoint: "visibility_presentation", presentationKind: "recovery_required", durableState: "starting_target" });
		expect(JSON.stringify(send.mock.calls[0][0])).not.toMatch(/local-(token|switch|route)-sentinel/);
	});

	it("dedupes two mounts and accepts the first presentation acknowledgement", async () => {
		const send = vi.fn(); const controller = harness(send);
		for (const token of ["token-a", "token-b"]) controller.signal(1, signalBody({ kind: "expected_presentation", token, switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" }));
		controller.signal(1, signalBody({ kind: "cancel", token: "token-a" }));
		await vi.advanceTimersByTimeAsync(1_999); expect(send).not.toHaveBeenCalled();
		controller.signal(1, signalBody({ kind: "presented", token: "token-b" }));
		await vi.advanceTimersByTimeAsync(1); expect(send).not.toHaveBeenCalled();
	});

	it("keeps the focused owner's presentation timer when the old owner cleans up", async () => {
		const send = vi.fn(); const controller = harness(send);
		controller.signal(1, signalBody({ kind: "expected_presentation", token: "old-token", switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" }));
		controller.registerWindow(2);
		controller.signal(2, signal("online", true));
		controller.signal(2, signal("focus", true));
		controller.signal(2, signalBody({ kind: "expected_presentation", token: "new-token", switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" }));

		controller.signal(1, signalBody({ kind: "cancel", token: "old-token" }));
		await vi.advanceTimersByTimeAsync(2_000);

		expect(send).toHaveBeenCalledTimes(1);
		expect(send.mock.calls[0][0]).toMatchObject({ failurePoint: "visibility_presentation", presentationKind: "terminal_failure" });
	});

	it("does not let a delayed non-owner presentation acknowledgement clear the focused owner's expectation", async () => {
		const send = vi.fn(); const controller = harness(send);
		controller.signal(1, signalBody({ kind: "expected_presentation", token: "old-token", switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" }));
		controller.registerWindow(2);
		controller.signal(2, signal("online", true));
		controller.signal(2, signal("focus", true));
		controller.signal(2, signalBody({ kind: "expected_presentation", token: "new-token", switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" }));

		controller.signal(1, signalBody({ kind: "presented", token: "old-token" }));
		await vi.advanceTimersByTimeAsync(2_000);

		expect(send).toHaveBeenCalledTimes(1);
		expect(send.mock.calls[0][0]).toMatchObject({ failurePoint: "visibility_presentation", presentationKind: "terminal_failure" });
	});

	it("cancels on focus transfer, offline, destruction, stale generation, and disable", async () => {
		const send = vi.fn(); const controller = harness(send);
		controller.registerWindow(2);
		controller.signal(1, health("transport", "active", false));
		controller.signal(2, signal("online", true)); controller.signal(2, signal("focus", true));
		await vi.advanceTimersByTimeAsync(15_000); expect(send).not.toHaveBeenCalled();
		controller.signal(2, signal("online", false)); controller.destroyWindow(2);
		controller.signal(1, { consentGeneration: "stale", signal: { kind: "focus", value: true } });
		controller.setPolicy(false, "generation-2");
		await vi.runAllTimersAsync(); expect(send).not.toHaveBeenCalled();
	});

	it("permits recurrence only after five continuous healthy minutes", async () => {
		const send = vi.fn(); const controller = harness(send);
		controller.signal(1, health("transport", "active", false)); await vi.advanceTimersByTimeAsync(15_000);
		controller.signal(1, health("transport", "active", true)); await vi.advanceTimersByTimeAsync(299_999);
		controller.signal(1, health("transport", "active", false)); await vi.advanceTimersByTimeAsync(15_000);
		expect(send).toHaveBeenCalledTimes(1);
		controller.signal(1, health("transport", "active", true)); await vi.advanceTimersByTimeAsync(300_000);
		controller.signal(1, health("transport", "active", false)); await vi.advanceTimersByTimeAsync(15_000);
		expect(send).toHaveBeenCalledTimes(2);
	});

	it("aborts and awaits an in-flight delivery before disable completes", async () => {
		let settled = false;
		const send = vi.fn((_incident, _generation, abort: AbortSignal) => new Promise<void>((resolve) => abort.addEventListener("abort", () => { settled = true; resolve(); }, { once: true })));
		const controller = harness(send);
		controller.signal(1, health("transport", "active", false));
		await vi.advanceTimersByTimeAsync(15_000);
		await controller.disableAndDrain();
		expect(send).toHaveBeenCalledOnce();
		expect(settled).toBe(true);
	});

	it("keeps the dedicated visibility kill switch closed", async () => {
		const send = vi.fn();
		const controller = new AgentSwitchVisibilityController({ send, killSwitched: true });
		controller.setPolicy(true, "generation-1"); controller.registerWindow(1);
		controller.signal(1, signal("focus", true)); controller.signal(1, signal("online", true)); controller.signal(1, health("transport", "active", false));
		await vi.advanceTimersByTimeAsync(15_000);
		expect(send).not.toHaveBeenCalled();
	});
});

describe("main visibility sender", () => {
	it("uses a bounded no-cache manual-redirect request", async () => {
		const fetch = vi.fn().mockResolvedValue({ ok: true, status: 202, body: { cancel: vi.fn() } });
		const sender = createAgentSwitchVisibilitySender({ dsn: "https://public@example.com/42", production: true, fetch, metadata });
		await sender({ failurePoint: "visibility_query", operation: "active", elapsedTimeBucket: "under_30s" }, "generation-local", new AbortController().signal);
		expect(fetch).toHaveBeenCalledWith("https://example.com/api/42/envelope/", expect.objectContaining({ cache: "no-store", redirect: "manual", referrerPolicy: "no-referrer", credentials: "omit" }));
		expect(new TextDecoder().decode(fetch.mock.calls[0][1].body)).not.toContain("generation-local");
	});

	it("never serializes the FSM's local switch, revision, route, token, generation, or sender", async () => {
		vi.useFakeTimers();
		const fetch = vi.fn().mockResolvedValue({ ok: true, status: 202, body: { cancel: vi.fn() } });
		const sender = createAgentSwitchVisibilitySender({ dsn: "https://public@example.com/42", production: true, fetch, metadata, eventId: () => "0123456789abcdef0123456789abcdef", now: () => new Date("2026-08-28T10:15:30.000Z") });
		const controller = new AgentSwitchVisibilityController({ send: sender });
		controller.setPolicy(true, "generation-sentinel"); controller.registerWindow(91);
		controller.signal(91, { consentGeneration: "generation-sentinel", signal: { kind: "focus", value: true } });
		controller.signal(91, { consentGeneration: "generation-sentinel", signal: { kind: "online", value: true } });
		controller.signal(91, { consentGeneration: "generation-sentinel", signal: { kind: "expected_presentation", token: "token-sentinel", switchId: "switch-sentinel", updatedAt: "revision-sentinel", localRouteKey: "route-sentinel", presentationKind: "recovery_required", durableState: "starting_target" } });
		await vi.advanceTimersByTimeAsync(2_000);
		const bytes = new TextDecoder().decode(fetch.mock.calls[0][1].body);
		for (const sentinel of ["generation-sentinel", "token-sentinel", "switch-sentinel", "revision-sentinel", "route-sentinel", "91"]) expect(bytes).not.toContain(sentinel);
	});
});

function harness(send: ConstructorParameters<typeof AgentSwitchVisibilityController>[0]["send"]) {
	const controller = new AgentSwitchVisibilityController({ send });
	controller.setPolicy(true, "generation-1"); controller.registerWindow(1);
	controller.signal(1, signal("focus", true)); controller.signal(1, signal("online", true));
	return controller;
}
function signal(kind: "focus" | "online", value: boolean) { return signalBody({ kind, value }); }
function health(kind: "transport" | "query", operation: "active" | "history", healthy: boolean) { return signalBody({ kind, operation, healthy, active: true }); }
function signalBody(signal: AgentSwitchVisibilitySignalBody) { return { consentGeneration: "generation-1", signal }; }
