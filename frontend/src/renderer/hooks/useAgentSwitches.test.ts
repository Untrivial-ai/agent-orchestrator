import { describe, expect, it } from "vitest";
import type { AgentSwitch } from "./useAgentSwitches";
import {
	agentSwitchesRefetchInterval,
	findActiveAgentSwitch,
	findRecoveryRequiredAgentSwitch,
} from "./useAgentSwitches";

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

describe("agentSwitchesRefetchInterval", () => {
	it.each([
		["polls an ordinary active switch", {}, 1_000],
		["polls target-start recovery until ownership is resolved", { errorCode: "target_start_unconfirmed" }, 1_000],
		["polls while source shutdown recovery may be running", { errorCode: "source_stop_unconfirmed", state: "stopping_source" }, 1_000],
		["polls while source restoration may be running", { errorCode: "source_restore_unconfirmed", state: "source_stopped" }, 1_000],
		["does not poll terminal history", { state: "completed" }, false],
		["keeps polling a protected future state", { state: "future_phase" }, 1_000],
	] as const)("%s", (_name, overrides, expected) => {
		expect(agentSwitchesRefetchInterval([switchRecord(overrides)])).toBe(expected);
	});
});

describe("durable agent switch selection", () => {
	it("keeps an ordinary active row separate from static recovery", () => {
		const recovery = switchRecord({ errorCode: "target_start_unconfirmed", id: "recovery" });
		const active = switchRecord({ id: "active", state: "delivering_context" });

		expect(findActiveAgentSwitch([recovery, active])).toBe(active);
		expect(findRecoveryRequiredAgentSwitch([recovery, active])).toBe(recovery);
	});

	it("selects a failed source restoration as static recovery", () => {
		const recovery = switchRecord({
			errorCode: "source_restore_unconfirmed",
			state: "source_stopped",
		});

		expect(findActiveAgentSwitch([recovery])).toBeUndefined();
		expect(findRecoveryRequiredAgentSwitch([recovery])).toBe(recovery);
	});

	it("selects an unconfirmed source stop as static recovery", () => {
		const recovery = switchRecord({
			errorCode: "source_stop_unconfirmed",
			state: "stopping_source",
		});

		expect(findActiveAgentSwitch([recovery])).toBeUndefined();
		expect(findRecoveryRequiredAgentSwitch([recovery])).toBe(recovery);
	});
});
