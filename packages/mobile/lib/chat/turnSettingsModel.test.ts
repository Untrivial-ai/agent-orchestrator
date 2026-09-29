import { describe, expect, it } from "vitest";
import type { ChatConfigOption, ChatModel, ConversationSnapshot } from "./types";
import { fastControlEnabled, fastControlValue, orderedProviderControls, providerTurnControlKind, turnSettingsRows, turnSettingsSummary } from "./turnSettingsModel";

const snapshot = (over: Partial<ConversationSnapshot> = {}): ConversationSnapshot => ({
	conversationId: "c",
	sessionId: "s",
	harness: "codex",
	mode: "chat",
	controller: { state: "ready" },
	latestSequence: 0,
	oldestSequence: 0,
	hasMoreBefore: false,
	turns: [],
	items: [],
	settings: {},
	capabilities: ["config_options"],
	...over,
});

describe("turnSettingsRows", () => {
	it("identifies the native controls Android promotes above generic provider options", () => {
		const controls: ChatConfigOption[] = [
			{ id: "fast", name: "Fast mode", type: "boolean", currentBoolean: true, choices: [] },
			{ id: "model", name: "Model", category: "model", type: "select", currentValue: "gpt", choices: [{ value: "gpt", name: "GPT" }] },
			{ id: "effort", name: "Effort", category: "thought_level", type: "select", currentValue: "high", choices: [{ value: "high", name: "High" }] },
			{ id: "permission_mode", name: "Permissions", category: "mode", type: "select", currentValue: "ask", choices: [{ value: "ask", name: "Ask" }] },
		];

		expect(controls.map(providerTurnControlKind)).toEqual(["fast", "model", "effort", "permissions"]);
		expect(orderedProviderControls([...controls].reverse()).map((option) => option.id)).toEqual([
			"fast",
			"model",
			"effort",
			"permission_mode",
		]);
	});

	it("treats a provider's binary Fast mode select as one direct on/off control", () => {
		const fastMode: ChatConfigOption = {
			id: "fast_mode",
			name: "Fast mode",
			type: "select",
			currentValue: "off",
			choices: [
				{ value: "off", name: "Off" },
				{ value: "on", name: "On" },
			],
		};

		expect(providerTurnControlKind(fastMode)).toBe("fast");
		expect(fastControlEnabled(fastMode)).toBe(false);
		expect(fastControlValue(fastMode, true)).toBe("on");
		expect(fastControlValue(fastMode, false)).toBe("off");
	});

	it("orders provider controls as fast mode, model, effort, permissions, then remaining controls", () => {
		const options: ChatConfigOption[] = [
			{ id: "sandbox", name: "Sandbox", type: "select", currentValue: "safe", choices: [{ value: "safe", name: "Safe" }] },
			{ id: "fast", name: "Fast mode", type: "boolean", currentBoolean: true, choices: [] },
			{ id: "effort", name: "Effort", category: "thought_level", type: "select", currentValue: "high", choices: [{ value: "high", name: "High" }] },
			{ id: "model", name: "Model", category: "model", type: "select", currentValue: "gpt", choices: [{ value: "gpt", name: "GPT" }] },
			{ id: "mode", name: "Mode", category: "mode", type: "select", currentValue: "agent", choices: [{ value: "agent", name: "Agent" }, { value: "bypass", name: "Bypass permissions" }] },
		];

		expect(turnSettingsRows(snapshot(), [], options).map((row) => row.label)).toEqual([
			"Fast mode",
			"Model",
			"Effort",
			"Mode",
			"Sandbox",
		]);
	});

	it("keeps its own approvals row when the provider's mode catalog is execution modes", () => {
		const options: ChatConfigOption[] = [
			{ id: "mode", name: "Mode", category: "mode", type: "select", currentValue: "build", choices: [{ value: "build", name: "build" }, { value: "plan", name: "plan" }] },
		];
		const rows = turnSettingsRows(snapshot({ settings: { approvalMode: "bypass-permissions" } }), [], options);
		expect(rows.map((row) => row.label)).toContain("Approvals");
		expect(rows.find((row) => row.label === "Approvals")?.value).toBe("Never ask");
	});

	it("builds model, effort, and approvals drill-down rows when provider controls are unavailable", () => {
		const models: ChatModel[] = [{
			id: "opus",
			displayName: "Opus",
			default: true,
			efforts: ["medium", "high"],
			defaultEffort: "medium",
		}];
		const rows = turnSettingsRows(snapshot({ capabilities: [], settings: { model: "opus", reasoningEffort: "high" } }), models, []);

		expect(rows.map((row) => [row.id, row.value, row.kind])).toEqual([
			["model", "Opus", "select"],
			["effort", "High", "select"],
			["approvals", "Default", "select"],
		]);
		expect(rows[1]?.choices.map((choice) => choice.label)).toEqual(["Medium", "High"]);
	});

	it("summarizes the selected model and permission level without repeating the harness", () => {
		const models: ChatModel[] = [{ id: "opus", displayName: "Opus", default: true }];
		expect(turnSettingsSummary(
			snapshot({ harness: "claude-code", capabilities: [], settings: { model: "opus", approvalMode: "auto" } }),
			models,
			[],
		)).toBe("Opus · Ask when unsure");
	});

	it("uses a provider permission control in the turn-settings summary", () => {
		const options: ChatConfigOption[] = [
			{ id: "model", name: "Model", category: "model", type: "select", currentValue: "sonnet", choices: [{ value: "sonnet", name: "Sonnet" }] },
			{ id: "permission_mode", name: "Permissions", category: "mode", type: "select", currentValue: "plan", choices: [{ value: "plan", name: "Plan only" }] },
		];
		expect(turnSettingsSummary(snapshot(), [], options)).toBe("Sonnet · Plan only");
	});
});

describe("turnSettingsSummary", () => {
	// The four controls a Codex session actually reports, verbatim from the
	// daemon: the Mode option is called "Mode", with nothing in its id or name
	// resembling "permission".
	const providerControls: ChatConfigOption[] = [
		{ id: "mode", name: "Mode", category: "mode", type: "select", currentValue: "bypassPermissions", choices: [{ value: "bypassPermissions", name: "Never ask" }] },
		{ id: "model", name: "Model", category: "model", type: "select", currentValue: "opus", choices: [{ value: "opus", name: "Opus" }] },
		{ id: "effort", name: "Effort", category: "thought_level", type: "select", currentValue: "xhigh", choices: [{ value: "xhigh", name: "Extra high" }] },
		{ id: "fast", name: "Fast mode", category: "model_config", type: "select", currentValue: "off", choices: [{ value: "off", name: "Off" }] },
	];

	// The bug this pins: the summary matched a permission row by looking for
	// "permission" or "approval" in the label, so a provider Mode option never
	// matched and the line fell back to AO's approvalMode — a different value
	// from the one the sheet was editing.
	it("reads the provider's own Mode rather than AO's approvalMode", () => {
		const summary = turnSettingsSummary(
			snapshot({ settings: { model: "opus", reasoningEffort: "xhigh", approvalMode: "default" } }),
			[],
			providerControls,
		);
		expect(summary).toContain("Never ask");
		expect(summary).not.toContain("Default permissions");
	});

	it("names the effort, which the line never used to mention", () => {
		const summary = turnSettingsSummary(snapshot(), [], providerControls);
		expect(summary).toBe("Opus · Extra high · Never ask");
	});

	it("drops an effort of Default rather than spending width on it", () => {
		const models: ChatModel[] = [
			{ id: "opus", displayName: "Opus", default: true, efforts: ["default", "high"], defaultEffort: "default" },
		];
		const summary = turnSettingsSummary(
			snapshot({ capabilities: [], settings: { model: "opus", reasoningEffort: "default" } }),
			models,
			[],
		);
		expect(summary).toBe("Opus · Default permissions");
	});

	it("still reports a model when the catalogue has not loaded", () => {
		const summary = turnSettingsSummary(snapshot({ capabilities: [], settings: { model: "opus" } }), [], []);
		expect(summary).toBe("opus · Default permissions");
	});
});
