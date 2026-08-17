import { describe, expect, it } from "vitest";
import type { AgentSwitchSummary } from "../types/workspace";
import {
	deriveAgentSwitchPresentation,
	type AgentSwitchPresentation,
	type AgentSwitchPresentationInput,
} from "./agent-switch-presentation";

function switchSummary(
	overrides: Partial<AgentSwitchSummary> = {},
): AgentSwitchSummary {
	return {
		agentHandoffStatus: "not_attempted",
		fromHarness: "claude-code",
		id: "switch-1",
		state: "preparing_handoff",
		targetHarness: "codex",
		...overrides,
	};
}

function input(
	agentSwitch: AgentSwitchSummary,
	overrides: Partial<Omit<AgentSwitchPresentationInput, "agentSwitch">> = {},
): AgentSwitchPresentationInput {
	return {
		agentSwitch,
		activityState: "active",
		currentHarness: "claude-code",
		isTerminated: false,
		terminalHandleId: "source-terminal",
		...overrides,
	};
}

function expectRelationship(
	presentation: AgentSwitchPresentation,
	expected: Pick<
		AgentSwitchPresentation,
		"stage" | "outcome" | "tone" | "animate" | "lockAgentTerminal" | "allowSourceInput"
	>,
) {
	expect(presentation).toMatchObject(expected);
	expect(presentation.values).toEqual({ source: "Claude Code", target: "Codex" });
}

describe("deriveAgentSwitchPresentation", () => {
	it.each([
		["preparing_handoff", "preparing", "switchAgent.state.preparingHandoff"],
		["stopping_source", "stopping_source", "switchAgent.state.stoppingSource"],
		["source_stopped", "starting_target", "switchAgent.state.sourceStopped"],
		["starting_target", "starting_target", "switchAgent.state.startingTarget"],
		["target_ready", "confirming_takeover", "switchAgent.state.targetReady"],
		["delivering_context", "confirming_takeover", "switchAgent.state.deliveringContext"],
	] as const)("maps %s into the durable %s stage", (state, stage, descriptionKey) => {
		const presentation = deriveAgentSwitchPresentation(input(switchSummary({ state })));

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: true,
			lockAgentTerminal: true,
			outcome: "in_progress",
			stage,
			tone: "working",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.compact.switching",
			descriptionKey,
			titleKey: "switchAgent.progressTitle",
		});
	});

	it.each([
		["blocked", true, "switchAgent.sourceInput.description"],
		["waiting_input", true, "switchAgent.sourceInput.description"],
		["active", false, "switchAgent.state.preparingHandoff"],
	] as const)(
		"maps a requested source handoff with %s activity to preparation",
		(activityState, allowSourceInput, descriptionKey) => {
			const presentation = deriveAgentSwitchPresentation(
				input(switchSummary({ agentHandoffStatus: "requested" }), { activityState }),
			);

			expectRelationship(presentation, {
				allowSourceInput,
				animate: true,
				lockAgentTerminal: !allowSourceInput,
				outcome: "in_progress",
				stage: "preparing",
				tone: allowSourceInput ? "warning" : "working",
			});
			expect(presentation.descriptionKey).toBe(descriptionKey);
		},
	);

	it("treats target-start uncertainty as static recovery before the ordinary starting stage", () => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ errorCode: "target_start_unconfirmed", state: "starting_target" })),
		);

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: false,
			lockAgentTerminal: true,
			outcome: "recovery",
			stage: "needs_attention",
			tone: "warning",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.recovery.compact",
			descriptionKey: "switchAgent.recovery.description",
			titleKey: "switchAgent.recovery.title",
		});
	});

	it("treats a failed source restoration as a provider-specific recovery action", () => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ errorCode: "source_restore_unconfirmed", state: "source_stopped" })),
		);

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: false,
			lockAgentTerminal: true,
			outcome: "recovery",
			stage: "needs_attention",
			tone: "warning",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.sourceRecovery.compact",
			descriptionKey: "switchAgent.sourceRecovery.description",
			titleKey: "switchAgent.sourceRecovery.title",
		});
	});

	it("treats an unconfirmed source stop as actionable recovery", () => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ errorCode: "source_stop_unconfirmed", state: "stopping_source" })),
		);

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: false,
			lockAgentTerminal: true,
			outcome: "recovery",
			stage: "needs_attention",
			tone: "warning",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.recovery.compact",
			descriptionKey: "switchAgent.sourceStopRecovery.description",
			titleKey: "switchAgent.sourceStopRecovery.title",
		});
	});

	it("keeps a completed row in confirmation until the target harness and terminal settle", () => {
		const presentation = deriveAgentSwitchPresentation(input(switchSummary({ state: "completed" })));

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: true,
			lockAgentTerminal: true,
			outcome: "in_progress",
			stage: "confirming_takeover",
			tone: "working",
		});
		expect(presentation.descriptionKey).toBe("switchAgent.state.completed");
	});

	it.each([
		[
			"the target terminal is missing",
			{ currentHarness: "codex", terminalHandleId: undefined, isTerminated: false },
		],
		[
			"the projected session is terminated",
			{ currentHarness: "codex", terminalHandleId: "target-terminal", isTerminated: true },
		],
		[
			"the source harness is still projected",
			{ currentHarness: "claude-code", terminalHandleId: "target-terminal", isTerminated: false },
		],
	] as const)("does not settle completion when %s", (_name, settlement) => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ state: "completed" }), settlement),
		);

		expect(presentation).toMatchObject({
			lockAgentTerminal: true,
			outcome: "in_progress",
			stage: "confirming_takeover",
		});
	});

	it("settles completion only after the live target harness and terminal are projected", () => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ state: "completed" }), {
				currentHarness: "codex",
				isTerminated: false,
				terminalHandleId: "target-terminal",
			}),
		);

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: false,
			lockAgentTerminal: false,
			outcome: "success",
			stage: null,
			tone: "success",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.success.compact",
			descriptionKey: "switchAgent.state.completed",
			titleKey: "switchAgent.success.compact",
		});
	});

	it.each([
		["target_binary_missing", "switchAgent.error.targetBinaryMissing"],
		["future_failure", "switchAgent.state.failed"],
	] as const)("maps failed switches through the stable %s error detail", (errorCode, descriptionKey) => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ errorCode, state: "failed" })),
		);

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: false,
			lockAgentTerminal: false,
			outcome: "failure",
			stage: "needs_attention",
			tone: "danger",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.failure.compact",
			descriptionKey,
			titleKey: "switchAgent.state.failed",
		});
	});

	it("protects terminal ownership while checking a future durable state", () => {
		const presentation = deriveAgentSwitchPresentation(
			input(switchSummary({ state: "future_phase" })),
		);

		expectRelationship(presentation, {
			allowSourceInput: false,
			animate: true,
			lockAgentTerminal: true,
			outcome: "in_progress",
			stage: "preparing",
			tone: "working",
		});
		expect(presentation).toMatchObject({
			compactLabelKey: "switchAgent.refreshOnly.compact",
			descriptionKey: "switchAgent.checkingStatus",
			titleKey: "switchAgent.checkingStatus",
		});
	});
});
