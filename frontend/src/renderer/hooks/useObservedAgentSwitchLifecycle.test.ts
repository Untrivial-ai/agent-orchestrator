import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { deriveAgentSwitchPresentation } from "../lib/agent-switch-presentation";
import type { AgentSwitch } from "./useAgentSwitches";
import { useObservedAgentSwitchLifecycle } from "./useObservedAgentSwitchLifecycle";

function switchRecord(overrides: Partial<AgentSwitch> = {}): AgentSwitch {
	return {
		agentHandoffStatus: "not_attempted",
		fromHarness: "claude-code",
		id: "switch-1",
		state: "starting_target",
		targetHarness: "codex",
		...overrides,
	};
}

function successPresentation(agentSwitch: AgentSwitch) {
	return deriveAgentSwitchPresentation({
		agentSwitch,
		currentHarness: agentSwitch.targetHarness,
		isTerminated: false,
		terminalHandleId: "target-controller",
	});
}

describe("useObservedAgentSwitchLifecycle", () => {
	afterEach(() => vi.useRealTimers());

	it("retires a settled lifecycle so its terminal history cannot be selected again", () => {
		vi.useFakeTimers();
		const activeSwitch = switchRecord();
		const completedSwitch = switchRecord({ state: "completed" });
		const { result, rerender } = renderHook(
			({ agentSwitches, candidates }) =>
				useObservedAgentSwitchLifecycle({
					sessionId: "session-1",
					agentSwitches,
					nonterminalCandidates: candidates,
				}),
			{
				initialProps: {
					agentSwitches: [activeSwitch],
					candidates: [activeSwitch],
				},
			},
		);

		rerender({ agentSwitches: [completedSwitch], candidates: [] });
		expect(result.current.observedTerminalSwitch).toBe(completedSwitch);

		act(() => result.current.settle(completedSwitch, successPresentation(completedSwitch)));
		expect(result.current.transientSuccessSwitchId).toBe(completedSwitch.id);
		rerender({ agentSwitches: [completedSwitch], candidates: [] });
		expect(result.current.observedTerminalSwitch).toBeUndefined();
		expect(result.current.isObserved(completedSwitch.id)).toBe(false);
		expect(result.current.isRetired(completedSwitch.id)).toBe(true);

		act(() => vi.advanceTimersByTime(3_000));
		rerender({ agentSwitches: [completedSwitch], candidates: [] });

		expect(result.current.observedTerminalSwitch).toBeUndefined();
		expect(result.current.isObserved(completedSwitch.id)).toBe(false);
		expect(result.current.isRetired(completedSwitch.id)).toBe(true);
	});

	it("does not replay a success notice after navigating away and back", () => {
		vi.useFakeTimers();
		const activeSwitch = switchRecord();
		const completedSwitch = switchRecord({ state: "completed" });
		const { result, rerender } = renderHook(
			({ sessionId, agentSwitches, candidates }) =>
				useObservedAgentSwitchLifecycle({
					sessionId,
					agentSwitches,
					nonterminalCandidates: candidates,
				}),
			{
				initialProps: {
					sessionId: "session-a",
					agentSwitches: [activeSwitch],
					candidates: [activeSwitch],
				},
			},
		);

		rerender({
			sessionId: "session-a",
			agentSwitches: [completedSwitch],
			candidates: [],
		});
		act(() => result.current.settle(completedSwitch, successPresentation(completedSwitch)));
		expect(result.current.transientSuccessSwitchId).toBe(completedSwitch.id);

		rerender({ sessionId: "session-b", agentSwitches: [], candidates: [] });
		expect(result.current.transientSuccessSwitchId).toBeUndefined();
		rerender({
			sessionId: "session-a",
			agentSwitches: [completedSwitch],
			candidates: [],
		});

		expect(result.current.transientSuccessSwitchId).toBeUndefined();
		expect(result.current.observedTerminalSwitch).toBeUndefined();
	});

	it("starts fresh when the mounted session changes", () => {
		const activeSwitch = switchRecord();
		const { result, rerender } = renderHook(
			({ sessionId, candidates }) =>
				useObservedAgentSwitchLifecycle({
					sessionId,
					agentSwitches: [],
					nonterminalCandidates: candidates,
				}),
			{
				initialProps: { sessionId: "session-1", candidates: [activeSwitch] },
			},
		);
		expect(result.current.isObserved(activeSwitch.id)).toBe(true);

		act(() => result.current.retire(activeSwitch.id));
		rerender({ sessionId: "session-2", candidates: [] });

		expect(result.current.isObserved(activeSwitch.id)).toBe(false);
		expect(result.current.isRetired(activeSwitch.id)).toBe(false);
	});

	it("does not present a terminal failure first discovered as history", () => {
		const historicalFailure = switchRecord({ id: "historical", state: "failed", errorCode: "switch_failed" });
		const { result } = renderHook(() => useObservedAgentSwitchLifecycle({
			sessionId: "session-1",
			agentSwitches: [historicalFailure],
			nonterminalCandidates: [],
		}));
		expect(result.current.observedTerminalSwitch).toBeUndefined();
		expect(result.current.isObserved(historicalFailure.id)).toBe(false);
	});
});
