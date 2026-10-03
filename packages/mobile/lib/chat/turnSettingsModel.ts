import type { ApprovalMode, ChatConfigChoice, ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";
import { can } from "./types";

export type TurnSettingChoice = {
	value: string;
	label: string;
	description?: string;
	selected: boolean;
};

export type TurnSettingRow = {
	id: string;
	label: string;
	description?: string;
	value: string;
	kind: "select" | "boolean";
	enabled?: boolean;
	/**
	 * How the provider categorised this control. Derived once, from the option
	 * itself — the summary used to re-guess it from the label and missed a Mode
	 * option called "Mode", so it showed AO's approvalMode while the sheet was
	 * editing the provider's.
	 */
	providerKind?: ProviderTurnControlKind;
	choices: TurnSettingChoice[];
	target:
		| { kind: "settings"; key: keyof TurnSettings }
		| { kind: "option"; optionId: string };
};

/**
 * The value of a setting nobody has named: the provider did not report it and
 * the user has not picked one. Shown instead of guessing (#5834).
 */
export const NOT_REPORTED = "Not reported";

const USE_AGENT_PERMISSIONS = "Use agent permissions";

/** Labels for a provider's `default` choice when nothing says what it resolves to (desktop's wording, #5849). */
const FOLLOW_AGENT = {
	model: "Use agent model",
	effort: "Use agent effort",
	other: "Use agent setting",
} as const;

const APPROVALS: Array<{ value: ApprovalMode; label: string; description: string }> = [
	{ value: "default", label: USE_AGENT_PERMISSIONS, description: "The worktree is the safety boundary" },
	{ value: "accept-edits", label: "Ask outside worktree", description: "Edits here are allowed; anything else asks" },
	{ value: "auto", label: "Ask when unsure", description: "The agent decides when to check with you" },
	{ value: "bypass-permissions", label: "Never ask", description: "No approvals or sandbox prompts" },
];

/**
 * AO's own approval modes, by name. Codex's default is full access: AO runs it
 * with approval "never" and sandbox "danger-full-access" (codexappserver
 * `approvalSettings`), so it is not the agent's own permissions there. Same
 * split as desktop's CODEX_APPROVAL_COPY / APPROVAL_COPY.
 */
export function approvalLabel(mode: ApprovalMode, harness: string): string {
	if (mode === "default" && harness === "codex") return "Full access";
	return APPROVALS.find((item) => item.value === mode)?.label ?? mode;
}

/** A native catalog's effort level by name; "default" names no level. */
export function effortChoiceLabel(effort: string): string {
	return isDefaultValue(effort) ? FOLLOW_AGENT.effort : capitalize(effort);
}

/** "Use agent model" and the like, including "Use agent model (Opus 5.5)": a choice that follows the agent. */
export function followsAgentLabel(label: string): boolean {
	return Object.values(FOLLOW_AGENT).some((follow) => label === follow || label.startsWith(`${follow} (`));
}

export type ProviderTurnControlKind = "fast" | "model" | "effort" | "permissions" | "other";

/**
 * Providers name their settings differently, so render the important four by
 * their semantic category first and fall back to a conservative name match.
 */
export function providerTurnControlKind(option: ChatConfigOption): ProviderTurnControlKind {
	const id = option.id.toLowerCase();
	const name = option.name.toLowerCase();
	if (id === "fast" || id.includes("fast") || name.includes("fast")) return "fast";
	if (option.category === "model" || id === "model" || id === "agent") return "model";
	if (option.category === "thought_level" || id === "effort" || id.includes("thought") || id.includes("reason")) return "effort";
	if (option.category === "mode" || id === "mode" || id.includes("permission") || id.includes("approval")) return "permissions";
	return "other";
}

/**
 * Providers offer a choice whose value is "default" and whose label names
 * nothing: Claude sends "Default (recommended)" for the model and "Default" for
 * effort (#5834). The wire value is kept, so picking it still follows the
 * provider when its default moves.
 *
 * Model and effort follow desktop's `resolveImplicitChoice` (TurnSettingsBar,
 * #5849): when the choice's description names another choice ("Opus 5.5"), it
 * takes that name and the duplicate goes, unless that model is the explicit
 * pick, when it reads "Use agent model (Opus 5.5)". Otherwise "Use agent
 * model" / "Use agent effort" rather than a level we would have to guess.
 *
 * Permission modes: a placeholder the daemon maps to AO's default permission
 * mode reads "Use agent permissions", as on desktop; one it maps to another
 * mode keeps its name, since the label would describe the wrong behaviour. A
 * placeholder the daemon does not map reads "Use agent permissions" too; desktop
 * drops such a choice when its value is "default", but here it stays, so every
 * mode the agent offers can still be picked. "Default approvals" is what
 * daemons before #5849 call OpenCode's default tier, and a phone can be paired
 * with one.
 */
export function resolveDefaultChoices(options: ChatConfigOption[]): ChatConfigOption[] {
	return options.map(resolveDefaultChoice);
}

function resolveDefaultChoice(option: ChatConfigOption): ChatConfigOption {
	if (option.type !== "select") return option;
	const kind = providerTurnControlKind(option);
	// Fast mode is a toggle, and fastControlValue reads a "default" choice as Off.
	if (kind === "fast") return option;
	if (kind === "permissions") {
		const followsAgent = (choice: ChatConfigChoice) =>
			(choice.permissionMode === undefined || choice.permissionMode === "default")
			&& (isDefaultPlaceholderLabel(choice.name) || /^default approvals$/i.test(choice.name.trim()));
		if (!option.choices.some(followsAgent)) return option;
		return {
			...option,
			choices: option.choices.map((choice) => followsAgent(choice) ? { ...choice, name: USE_AGENT_PERMISSIONS } : choice),
		};
	}
	const implicit = option.choices.find((choice) => choice.value === "default");
	if (!implicit) return option;
	const named = implicit.description?.trim().toLowerCase();
	const concrete = named
		? option.choices.find((choice) => choice !== implicit && choice.name.trim().toLowerCase() === named)
		: undefined;
	if (concrete && option.currentValue !== concrete.value) {
		// The row that stays reads exactly like the one it replaces.
		const name = isDefaultPlaceholderLabel(concrete.name) ? concrete.value : concrete.name;
		return {
			...option,
			choices: option.choices
				.filter((choice) => choice !== concrete)
				.map((choice) => choice === implicit ? { ...choice, name, description: concrete.description } : choice),
		};
	}
	if (!isDefaultPlaceholderLabel(implicit.name)) return option;
	// An `agent` option is classed with models here, but it picks an agent.
	const isModel = option.category === "model" || option.id.toLowerCase() === "model";
	const follow = isModel ? FOLLOW_AGENT.model : kind === "effort" ? FOLLOW_AGENT.effort : FOLLOW_AGENT.other;
	return {
		...option,
		choices: option.choices.map((choice) =>
			choice === implicit ? { ...choice, name: concrete ? `${follow} (${concrete.name})` : follow, description: undefined } : choice),
	};
}

/** "Default", "Default (recommended)": a label that names no model, level or mode. */
function isDefaultPlaceholderLabel(label: string): boolean {
	return /^default(?:\s*\([^)]*\))?$/i.test(label.trim());
}

/**
 * A provider option's current value, by the name of its choice. A value no
 * choice explains is shown as sent; "default" or no value at all reads "Not
 * reported" rather than "Default" (#5834).
 */
export function providerChoiceLabel(option: ChatConfigOption): string {
	if (option.type === "boolean") return option.currentBoolean ? "On" : "Off";
	const selected = option.choices.find((choice) => choice.value === option.currentValue);
	if (selected) return selected.name;
	return sentValue(option.currentValue) ?? NOT_REPORTED;
}

/**
 * The native model row's value: `model` by name (the catalog entry picked, else
 * the one the catalog marks as the provider's default). Without one, the
 * setting as sent, except "default", which reads "Not reported". Display only:
 * the setting itself is sent unchanged.
 */
export function nativeModelLabel(model: ChatModel | undefined, setting: string | undefined): string {
	return model?.displayName ?? sentValue(setting) ?? NOT_REPORTED;
}

/** A value as sent, or undefined when there is none or it is "default", which names nothing. */
function sentValue(value: string | undefined): string | undefined {
	return value && !isDefaultValue(value) ? value : undefined;
}

/**
 * Where an effort slider sits for `selected`: the index of that level, or -1
 * when the effort is none of the slider's levels (not reported, or a level this
 * model does not list). -1 must not be clamped to the first level: the slider
 * saves the level it sits on, so a clamp saved a level nobody picked.
 */
export function effortSliderIndex(levels: ReadonlyArray<{ value: string }>, selected: string): number {
	return levels.findIndex((level) => level.value === selected);
}

/** The level a slider sitting at `index` should save: none while it is unplaced (-1) or on `selected` already. */
export function effortSliderWrite(levels: ReadonlyArray<{ value: string }>, selected: string, index: number): string | undefined {
	const next = index < 0 ? undefined : levels[index]?.value;
	return next && next !== selected ? next : undefined;
}

/** Providers encode Fast mode as either a boolean or an On/Off select. */
export function fastControlEnabled(option: ChatConfigOption): boolean {
	if (option.type === "boolean") return Boolean(option.currentBoolean);
	const current = option.choices.find((choice) => choice.value === option.currentValue);
	return fastChoiceMatches(current, true) || fastChoiceMatches({ value: option.currentValue ?? "", name: option.currentValue ?? "" }, true);
}

/** Returns the provider-owned value for an On/Off Fast mode select. */
export function fastControlValue(option: ChatConfigOption, enabled: boolean): string | undefined {
	if (option.type !== "select") return undefined;
	const direct = option.choices.find((choice) => fastChoiceMatches(choice, enabled));
	if (direct) return direct.value;
	if (option.choices.length === 2) {
		const current = option.choices.find((choice) => choice.value === option.currentValue);
		if (current) return option.choices.find((choice) => choice.value !== current.value)?.value;
	}
	return undefined;
}

function fastChoiceMatches(choice: { value: string; name: string } | undefined, enabled: boolean): boolean {
	if (!choice) return false;
	const text = `${choice.value} ${choice.name}`.toLowerCase();
	const words = text.split(/[^a-z]+/).filter(Boolean);
	return enabled
		? words.some((word) => ["on", "true", "fast", "enabled", "enable"].includes(word))
		: words.some((word) => ["off", "false", "normal", "standard", "disabled", "disable", "default"].includes(word));
}

/** The mobile form starts with the controls people actively tune each turn. */
export function orderedProviderControls(options: ChatConfigOption[]): ChatConfigOption[] {
	const priority: Record<ProviderTurnControlKind, number> = {
		fast: 0,
		model: 1,
		effort: 2,
		permissions: 3,
		other: 4,
	};
	return options.sort((a, b) => priority[providerTurnControlKind(a)] - priority[providerTurnControlKind(b)]);
}

/**
 * A provider `mode` option replaces the Approvals row only when it offers
 * approval choices. OpenCode advertises build/plan there — execution modes —
 * and mistaking them for approvals leaves the session with no way to bypass.
 */
function isPermissionModeOption(option: ChatConfigOption): boolean {
	if (option.category !== "mode" && option.id !== "mode") return false;
	return option.choices.some((choice) =>
		![choice.value, choice.name].some((text) => /^(?:plan|agent|build)(?:[\s_-]mode)?$/i.test(text.trim())));
}

export function turnSettingsRows(snapshot: ConversationSnapshot, models: ChatModel[], options: ChatConfigOption[]): TurnSettingRow[] {
	const selectedModel = models.find((model) => model.id === snapshot.settings.model) ?? models.find((model) => model.default);
	const providerModel = options.some((option) => option.category === "model" || option.id === "model" || option.id === "agent");
	const providerMode = options.some(isPermissionModeOption);
	const rows: TurnSettingRow[] = [];

	if ((!can(snapshot, "config_options") || !providerModel) && models.length) {
		rows.push({
			id: "model",
			label: "Model",
			value: nativeModelLabel(selectedModel, snapshot.settings.model),
			kind: "select",
			// No "Provider default" hint: with nothing picked, the provider's model
			// is the selected row already (#5834).
			choices: models.map((model) => ({
				value: model.id,
				label: model.displayName,
				description: model.description || undefined,
				selected: model.id === selectedModel?.id,
			})),
			target: { kind: "settings", key: "model" },
		});
		if (selectedModel?.efforts?.length) {
			const effort = snapshot.settings.reasoningEffort ?? selectedModel.defaultEffort;
			rows.push({
				id: "effort",
				label: "Effort",
				value: effort ? effortChoiceLabel(effort) : NOT_REPORTED,
				kind: "select",
				choices: selectedModel.efforts.map((value) => ({ value, label: effortChoiceLabel(value), selected: value === effort })),
				target: { kind: "settings", key: "reasoningEffort" },
			});
		}
	}

	if (!can(snapshot, "config_options") || !providerMode) {
		const approval = snapshot.settings.approvalMode ?? "default";
		rows.push({
			id: "approvals",
			label: "Approvals",
			value: approvalLabel(approval, snapshot.harness),
			kind: "select",
			choices: APPROVALS.map((item) => ({
				value: item.value,
				label: approvalLabel(item.value, snapshot.harness),
				description: item.description,
				selected: item.value === approval,
			})),
			target: { kind: "settings", key: "approvalMode" },
		});
	}

	rows.push(...options.map(providerRow));
	return rows.sort((a, b) => controlPriority(a) - controlPriority(b));
}

export function turnSettingsSummary(snapshot: ConversationSnapshot, models: ChatModel[], options: ChatConfigOption[]): string {
	const rows = turnSettingsRows(snapshot, models, options);
	const effort = rows.find(isEffortRow);
	const permissions = rows.find(isPermissionRow);
	// Effort is the setting people change most after the model, and it was the
	// one this line never mentioned. A value that names nothing is dropped:
	// saying it spends the row's width on nothing (#5834).
	return [
		modelLabel(rows, snapshot),
		named(effort ? effort.value : capitalize(snapshot.settings.reasoningEffort ?? "")),
		named(permissions?.value ?? approvalLabel(snapshot.settings.approvalMode ?? "default", snapshot.harness)),
	].filter(Boolean).join(" · ");
}

/**
 * The model the next turn runs on, as the turn-settings control names it.
 * Empty when nothing names one (#5834).
 */
export function turnSettingsModelLabel(snapshot: ConversationSnapshot, models: ChatModel[], options: ChatConfigOption[]): string {
	return modelLabel(turnSettingsRows(snapshot, models, options), snapshot);
}

function modelLabel(rows: TurnSettingRow[], snapshot: ConversationSnapshot): string {
	const model = rows.find(isModelRow);
	return named(model ? model.value : snapshot.settings.model ?? "");
}

/** The value, or "" when it names nothing: unreported, a bare "default", or "Use agent …". */
function named(value: string): string {
	return value === NOT_REPORTED || isDefaultValue(value) || followsAgentLabel(value) ? "" : value;
}

function isDefaultValue(value: string): boolean {
	return value.trim().toLowerCase() === "default";
}

function isModelRow(row: TurnSettingRow): boolean {
	if (row.target.kind === "settings") return row.target.key === "model";
	return row.providerKind === "model";
}

function isEffortRow(row: TurnSettingRow): boolean {
	if (row.target.kind === "settings") return row.target.key === "reasoningEffort";
	return row.providerKind === "effort";
}

function isPermissionRow(row: TurnSettingRow): boolean {
	if (row.target.kind === "settings") return row.target.key === "approvalMode";
	return row.providerKind === "permissions";
}

function providerRow(option: ChatConfigOption): TurnSettingRow {
	return {
		id: `option:${option.id}`,
		label: option.name,
		description: option.description,
		value: providerChoiceLabel(option),
		kind: option.type,
		enabled: Boolean(option.currentBoolean),
		choices: option.choices.map((choice) => ({
			value: choice.value,
			label: choice.name,
			description: choice.description,
			selected: choice.value === option.currentValue,
		})),
		target: { kind: "option", optionId: option.id },
		providerKind: providerTurnControlKind(option),
	};
}

function controlPriority(row: TurnSettingRow): number {
	if (row.target.kind === "option") {
		return { fast: 0, model: 1, effort: 2, permissions: 3, other: 10 }[row.providerKind ?? "other"];
	}
	const key = row.id;
	if (key === "model") return 1;
	if (key === "effort") return 2;
	if (key === "approvals") return 4;
	return 10;
}

export function applyTurnSettingChoice(settings: TurnSettings, row: TurnSettingRow, value: string): TurnSettings {
	if (row.target.kind !== "settings") return settings;
	if (row.target.key === "model") return { ...settings, model: value, reasoningEffort: undefined };
	return { ...settings, [row.target.key]: value } as TurnSettings;
}

function capitalize(value: string): string {
	return value ? value[0].toUpperCase() + value.slice(1) : value;
}
