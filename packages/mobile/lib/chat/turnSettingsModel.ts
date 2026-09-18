import type { ApprovalMode, ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";
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
	choices: TurnSettingChoice[];
	target:
		| { kind: "settings"; key: keyof TurnSettings }
		| { kind: "option"; optionId: string };
};

const APPROVALS: Array<{ value: ApprovalMode; label: string; description: string }> = [
	{ value: "default", label: "Default", description: "The worktree is the safety boundary" },
	{ value: "accept-edits", label: "Ask outside worktree", description: "Edits here are allowed; anything else asks" },
	{ value: "auto", label: "Ask when unsure", description: "The agent decides when to check with you" },
	{ value: "bypass-permissions", label: "Never ask", description: "No approvals or sandbox prompts" },
];

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

export function turnSettingsRows(snapshot: ConversationSnapshot, models: ChatModel[], options: ChatConfigOption[]): TurnSettingRow[] {
	const selectedModel = models.find((model) => model.id === snapshot.settings.model) ?? models.find((model) => model.default);
	const providerModel = options.some((option) => option.category === "model" || option.id === "model" || option.id === "agent");
	const providerMode = options.some((option) => option.category === "mode" || option.id === "mode");
	const rows: TurnSettingRow[] = [];

	if ((!can(snapshot, "config_options") || !providerModel) && models.length) {
		rows.push({
			id: "model",
			label: "Model",
			value: selectedModel?.displayName ?? "Default",
			kind: "select",
			choices: models.map((model) => ({
				value: model.id,
				label: model.displayName,
				description: model.description || (model.default ? "Provider default" : undefined),
				selected: model.id === selectedModel?.id,
			})),
			target: { kind: "settings", key: "model" },
		});
		if (selectedModel?.efforts?.length) {
			const effort = snapshot.settings.reasoningEffort ?? selectedModel.defaultEffort;
			rows.push({
				id: "effort",
				label: "Effort",
				value: capitalize(effort || "Default"),
				kind: "select",
				choices: selectedModel.efforts.map((value) => ({ value, label: capitalize(value), selected: value === effort })),
				target: { kind: "settings", key: "reasoningEffort" },
			});
		}
	}

	if (!can(snapshot, "config_options") || !providerMode) {
		const approval = snapshot.settings.approvalMode ?? "default";
		rows.push({
			id: "approvals",
			label: "Approvals",
			value: APPROVALS.find((item) => item.value === approval)?.label ?? "Default",
			kind: "select",
			choices: APPROVALS.map((item) => ({ ...item, selected: item.value === approval })),
			target: { kind: "settings", key: "approvalMode" },
		});
	}

	rows.push(...options.map(providerRow));
	return rows.sort((a, b) => controlPriority(a) - controlPriority(b));
}

export function turnSettingsSummary(snapshot: ConversationSnapshot, models: ChatModel[], options: ChatConfigOption[]): string {
	const rows = turnSettingsRows(snapshot, models, options);
	const model = rows.find(isModelRow);
	const permissions = rows.find(isPermissionRow);
	const modelValue = model?.value || snapshot.settings.model || "Default model";
	const permissionValue = permissions?.value
		|| APPROVALS.find((item) => item.value === (snapshot.settings.approvalMode ?? "default"))?.label
		|| "Default";
	return `${modelValue} · ${permissionValue === "Default" ? "Default permissions" : permissionValue}`;
}

function isModelRow(row: TurnSettingRow): boolean {
	const key = row.target.kind === "option" ? row.target.optionId : row.id;
	return row.target.kind === "settings" && row.target.key === "model"
		|| row.label.toLowerCase() === "model"
		|| ["model", "agent"].includes(key.toLowerCase());
}

function isPermissionRow(row: TurnSettingRow): boolean {
	if (row.target.kind === "settings" && row.target.key === "approvalMode") return true;
	const key = row.target.kind === "option" ? row.target.optionId : row.id;
	const description = `${key} ${row.label}`.toLowerCase();
	return description.includes("permission") || description.includes("approval");
}

function providerRow(option: ChatConfigOption): TurnSettingRow {
	const selected = option.choices.find((choice) => choice.value === option.currentValue);
	return {
		id: `option:${option.id}`,
		label: option.name,
		description: option.description,
		value: option.type === "boolean" ? (option.currentBoolean ? "On" : "Off") : selected?.name ?? option.currentValue ?? "Default",
		kind: option.type,
		enabled: Boolean(option.currentBoolean),
		choices: option.choices.map((choice) => ({
			value: choice.value,
			label: choice.name,
			description: choice.description,
			selected: choice.value === option.currentValue,
		})),
		target: { kind: "option", optionId: option.id },
	};
}

function controlPriority(row: TurnSettingRow): number {
	if (row.target.kind === "option") {
		const kind = providerTurnControlKind({
			id: row.target.optionId,
			name: row.label,
			type: row.kind,
			choices: [],
		});
		return { fast: 0, model: 1, effort: 2, permissions: 3, other: 10 }[kind];
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
