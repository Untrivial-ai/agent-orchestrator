/**
 * What the next turn will be sent with: model, reasoning effort, approval mode.
 *
 * All three are per-turn on the provider's side, so choosing one changes the next
 * message and never restarts the agent — the running turn keeps what it was
 * dispatched with. That is why this sits in the composer rather than in settings.
 *
 * The catalog comes from the provider, not from a list in AO. Models are added,
 * renamed, hidden per account and gated by entitlement AO cannot see, so a
 * hardcoded list would be wrong within a week. An agent whose provider cannot
 * enumerate models reports none and the model control hides itself.
 *
 * ACP agents advertise those same dimensions as live session options. They share
 * this chrome rather than each growing a row of pickers: model and thought level
 * club into the left-hand control, while a provider-owned mode (such as planning)
 * stays separate from AO's approval policy. The lists inside are still the
 * provider's; only the grouping of the triggers is AO's.
 */

import { Fragment, useMemo, useState, type FocusEvent, type ReactNode } from "react";
import { ChevronDown, Shuffle } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	OptionMenu,
	OptionMenuContent,
	OptionMenuItem,
	OptionMenuLabel,
	OptionMenuSub,
	OptionMenuSubContent,
	OptionMenuSubTrigger,
	OptionMenuTrigger,
} from "../ui/option-menu";
import { fallbackEffort, useApplyEffortDefault } from "../../lib/effort";
import { cn } from "../../lib/utils";
import { effortDisplayLabel, EffortMenuItems, EffortPicker, formatEffortLabel } from "../settings/EffortPicker";
import { agentModelDisplayLabel, isDefaultPlaceholderLabel, splitClaudeModels } from "../../lib/agent-model-choices";
import { Switch } from "../ui/switch";
import { ModelMenuChoices } from "./ModelMenuChoices";
import type {
	ApprovalMode,
	ChatConfigOption,
	ChatConfigOptionValue,
	ChatModel,
	ModelReroute,
	TurnSettings,
} from "../../types/conversation";

/** AO's generic approval modes, used by harnesses without a native vocabulary. */
const APPROVAL_COPY: Record<ApprovalMode, { label: string }> = {
	default: { label: "Use agent permissions" },
	"accept-edits": { label: "Accept edits" },
	auto: { label: "Auto-approve" },
	"bypass-permissions": { label: "Bypass permissions" },
};

const APPROVAL_ORDER: ApprovalMode[] = [
	"default",
	"accept-edits",
	"auto",
	"bypass-permissions",
];

// Codex has three distinct permission profiles. Its default is already full
// access in AO's isolated worktree posture, so expose it as that rather than a
// fourth, ambiguous "default" option.
const CODEX_APPROVAL_COPY: Record<ApprovalMode, { label: string }> = {
	default: { label: "Full access" },
	"accept-edits": { label: "Ask for approval" },
	auto: { label: "Approve for me" },
	"bypass-permissions": { label: "Bypass permissions" },
};

const CODEX_APPROVAL_ORDER: ApprovalMode[] = [
	"default",
	"accept-edits",
	"auto",
	"bypass-permissions",
];

const TRIGGER_CLASS =
	"h-7 gap-1 bg-transparent rounded-lg px-3 text-xs! leading-none text-muted-foreground hover:bg-interactive-active hover:text-foreground data-[state=open]:bg-interactive-active data-[state=open]:text-foreground";
const CHAT_MENU_CLASS = "chat-settings-menu text-xs!";

export function TurnSettingsBar({
	models,
	settings,
	harness,
	showApprovalMode = true,
	approvalModes,
	reroute,
	onChange,
	onRememberPermissions,
	rememberPermissionsPending,
	rememberPermissionsError,
	rememberedPermissionMode,
	configOptions,
	onChangeConfigOption,
	configPending,
	error,
	disabled,
	autoSelectEffortOnOpen = false,
	children,
}: {
	models: ChatModel[];
	settings: TurnSettings;
	/** The active provider selects its own supported permission vocabulary. */
	harness?: string;
	/** Cloud permissions are fixed for the session, not a per-turn setting. */
	showApprovalMode?: boolean;
	approvalModes?: ApprovalMode[];
	/**
	 * The provider answered with a different model than the one chosen. Separate from
	 * `settings` all the way down: settings are what the user asked for, this is what
	 * replied, and folding them together is how the control ends up advertising a
	 * model that is not the one producing the answers.
	 */
	reroute?: ModelReroute;
	onChange?: (next: TurnSettings) => void;
	onRememberPermissions?: (mode: ApprovalMode) => Promise<unknown> | void;
	rememberPermissionsPending?: boolean;
	rememberPermissionsError?: string;
	rememberedPermissionMode?: ApprovalMode;
	/** Controls advertised by an ACP agent for this exact live session. */
	configOptions?: ChatConfigOption[];
	onChangeConfigOption?: (
		optionId: string,
		value: ChatConfigOptionValue,
	) => Promise<unknown> | void;
	/** Prevent overlapping writes because provider responses replace the catalog. */
	configPending?: boolean;
	/**
	 * Pick an effort as soon as this opens when the model reports levels but no
	 * default. Only for a conversation with no turns yet, so reopening an existing
	 * one never changes the effort it was running with. Choosing another model
	 * always selects one regardless.
	 */
	autoSelectEffortOnOpen?: boolean;
	error?: string;
	disabled?: boolean;
	/** Inline controls on the right model row, before the mode/approval picker — queue vs steer. */
	children?: ReactNode;
}) {
	const { t } = useTranslation();
	const displayModels = useMemo(
		() => models.map((model) => ({
			...model,
			displayName: agentModelDisplayLabel(harness, model.displayName),
		})),
		[harness, models],
	);
	const claudeLabels = useMemo(() => (harness === "claude-code" ? claudeChoiceLabels(displayModels) : undefined), [harness, displayModels]);
	const displayConfigOptions = useMemo(
		() => (configOptions ?? []).map((option) => isModelOption(option) ? {
			...option,
			choices: option.choices.map((choice) => ({
				...choice,
				name: claudeLabels?.(choice.value) ?? agentModelDisplayLabel(harness, choice.name),
				description: choice.description
					? agentModelDisplayLabel(harness, choice.description)
					: choice.description,
			})),
		} : option),
		[claudeLabels, configOptions, harness],
	);
	const selected = displayModels.find((model) => model.id === settings.model);
	const fallback = settings.model ? undefined : displayModels.find((model) => model.default);
	// A catalog miss must not relabel an explicit choice or borrow another model's
	// effort settings. Custom or newly available models may not be listed yet.
	const chosenLabel =
		selected?.displayName ?? settings.model ?? fallback?.displayName ?? "Choose a model";
	const rerouted = reroute
		? displayModels.find((model) => model.id === reroute.toModel)?.displayName ?? reroute.toModel
		: undefined;
	const modelLabel = rerouted ?? chosenLabel;
	const availableEfforts = (selected ?? fallback)?.efforts ?? [];
	const efforts = availableEfforts.filter((effort) => effort.toLowerCase() !== "default");
	const selectedEffort =
		settings.reasoningEffort && settings.reasoningEffort !== "default"
			? settings.reasoningEffort
			: (selected ?? fallback)?.defaultEffort;
	const effortLabel = selectedEffort === "default" ? undefined : selectedEffort;
	const approvalCopy = harness === "codex" ? CODEX_APPROVAL_COPY : APPROVAL_COPY;
	const approvalOrder = harness === "codex" ? CODEX_APPROVAL_ORDER : APPROVAL_ORDER;
	const approvalLabel = approvalCopy[settings.approvalMode ?? "default"].label;
	const modelGroupLabel = effortLabel
		? `${modelLabel} ${formatEffortLabel(effortLabel, t)}`
		: modelLabel;
	const grouped = partitionConfigOptions(displayConfigOptions);
	const optionDisabled = Boolean(disabled || configPending || rememberPermissionsPending);
	const applyOption = (optionId: string, value: ChatConfigOptionValue) => {
		if (!onChangeConfigOption) return;
		void Promise.resolve(onChangeConfigOption(optionId, value)).catch(() => {});
	};
	const nativeModelMenu = Boolean(onChange && displayModels.length > 0 && grouped.model.length === 0);
	// A model with effort levels but no provider default would otherwise show no
	// selected effort. AO picks a level and sets it, so what the picker shows is
	// what the next turn uses.
	const acpEffortOption = grouped.effort.at(0);
	useApplyEffortDefault(
		`${harness}:${grouped.model.map((option) => option.currentValue).join(":")}`,
		acpEffortOption ? acpFallbackEffort(acpEffortOption) : undefined,
		(value) => {
			if (acpEffortOption) applyOption(acpEffortOption.id, { value });
		},
		{ disabled: optionDisabled || !onChangeConfigOption, applyOnMount: autoSelectEffortOnOpen },
	);
	const nativeEffortUnset = !settings.reasoningEffort || settings.reasoningEffort === "default";
	useApplyEffortDefault(
		`${harness}:${settings.model ?? fallback?.id}`,
		nativeModelMenu && nativeEffortUnset ? fallbackEffort(efforts, (selected ?? fallback)?.defaultEffort) : undefined,
		(value) => onChange?.({ ...settings, reasoningEffort: value }),
		{ disabled: optionDisabled || !onChange, applyOnMount: autoSelectEffortOnOpen },
	);
	const modeOption = grouped.mode;
	const inlineExecutionMode =
		grouped.executionMode && isPlanBinary(grouped.executionMode) ? grouped.executionMode : undefined;
	const standaloneExecutionMode =
		grouped.executionMode && !isPlanBinary(grouped.executionMode) ? grouped.executionMode : undefined;
	const planning = isPlanMode(grouped.executionMode);
	// Leaving Plan returns to the approval mode the session was on, not to the
	// provider's bare agent mode, which would silently drop the policy while the
	// picker still displayed it.
	const planReturn = modeOption?.choices.find(
		(choice) => choice.permissionMode === (settings.approvalMode ?? "default"),
	)?.value;
	const clubbedLeft =
		grouped.model.length > 0 ||
		grouped.effort.length > 0 ||
		Boolean(inlineExecutionMode) ||
		grouped.toggles.length > 0 ||
		grouped.extra.length > 0;
	const rememberMode = modeOption
		? modeOption.choices.find((choice) => choice.value === modeOption.currentValue)?.permissionMode
		: settings.approvalMode ?? "default";
	const rememberAction = onRememberPermissions && rememberMode && !planning ? (
		<OptionMenuItem
			disabled={optionDisabled}
			onSelect={() => {
				void Promise.resolve(onRememberPermissions(rememberMode)).catch(() => {});
			}}
			className="mt-1 border-t border-border pt-2 text-xs"
		>
			Remember for this project
		</OptionMenuItem>
	) : null;
	const showRightDropdown = Boolean(children || (!planning && ((showApprovalMode && onChange) || modeOption)));

	return (
		<div role="group" aria-label="Turn settings" className="flex min-w-0 flex-1 flex-col gap-0.5">
			<div className="flex h-7 min-w-0 flex-1 items-center justify-between gap-2">
				<div className="flex h-7 min-w-0 flex-wrap items-center gap-0.5">
					{nativeModelMenu && onChange ? (
						<ModelEffortPicker
							models={harness === "claude-code" ? displayModels.filter((model) => model.id !== "default") : displayModels}
							settings={settings}
							onChange={onChange}
							disabled={optionDisabled}
							modelLabel={modelLabel}
							groupLabel={modelGroupLabel}
							efforts={efforts}
							defaultEffort={(selected ?? fallback)?.defaultEffort}
							availability={(selected ?? fallback)?.efforts === undefined ? "unknown" : efforts.length ? "supported" : "unsupported"}
							reroute={reroute}
							rerouted={rerouted}
							chosenLabel={chosenLabel}
							executionMode={inlineExecutionMode}
							planReturn={planReturn}
							toggles={grouped.toggles}
							extraOptions={grouped.extra}
							onChangeConfigOption={onChangeConfigOption ? applyOption : undefined}
						/>
					) : null}

					{onChangeConfigOption && clubbedLeft && !nativeModelMenu ? (
						<ClubbedConfigPicker
							groupClaude={harness === "claude-code"}
							modelOptions={grouped.model}
							effortOptions={grouped.effort}
							executionMode={inlineExecutionMode}
							planReturn={planReturn}
							toggles={grouped.toggles}
							extraOptions={grouped.extra}
							disabled={optionDisabled}
							onChange={applyOption}
						/>
					) : null}

					{standaloneExecutionMode && onChangeConfigOption ? (
						<ExecutionModePicker
							option={standaloneExecutionMode}
							planReturn={planReturn}
							disabled={optionDisabled}
							onChange={applyOption}
						/>
					) : null}
				</div>

				{showRightDropdown || children ? (
					<div className="flex h-7 shrink-0 items-center gap-1">
						{children}
						{!planning && modeOption && onChangeConfigOption ? (
							<ConfigOptionPicker
								option={modeOption}
								disabled={optionDisabled}
								onChange={(value) => applyOption(modeOption.id, value)}
								footer={rememberAction}
							/>
						) : showApprovalMode && onChange ? (
							<Picker
								label={approvalLabel}
													title="Approval policy for the next turn"
													disabled={optionDisabled}
							>
								{approvalOrder.filter((mode) => !approvalModes || approvalModes.includes(mode)).map((mode) => (
									<OptionMenuItem
										key={mode}
										active={mode === (settings.approvalMode ?? "default")}
										radio
										onSelect={() => onChange({ ...settings, approvalMode: mode })}
										className={cn("text-xs")}
									>
										<span
											className={cn(
														"text-xs",
												mode === (settings.approvalMode ?? "default")
													? "text-foreground"
													: "text-muted-foreground",
											)}
										>
											{approvalCopy[mode].label}
										</span>
									</OptionMenuItem>
								))}
								{rememberAction}
							</Picker>
						) : null}
					</div>
				) : null}
			</div>
			{rememberPermissionsPending || (rememberedPermissionMode !== undefined && rememberedPermissionMode === rememberMode && !planning && !configPending) ? (
				<p role="status" className="px-1 text-xs text-muted-foreground">
					{rememberPermissionsPending ? "Saving project permissions…" : "Permission mode saved for new sessions in this project."}
				</p>
			) : null}
			{rememberPermissionsError ? (
				<p role="alert" className="px-1 text-xs text-destructive">{rememberPermissionsError}</p>
			) : null}
			{error ? (
				<p role="alert" className="px-1 text-xs leading-snug text-destructive">
					{error}
				</p>
			) : null}
		</div>
	);
}

/**
 * The provider's model option offers family aliases ("opus") and bare ids. The new-task
 * picker names them by version, so the chat does too: an alias is the newest model of
 * its family in the catalog, and an id is its catalog entry.
 */
function claudeChoiceLabels(catalog: { id: string; displayName: string }[]) {
	if (catalog.length === 0) return undefined;
	const entries = catalog.map((model) => ({ id: model.id, label: model.displayName }));
	const newest = splitClaudeModels(entries).current;
	return (value: string): string | undefined => {
		const id = value.replace(/\[.*?\]$/, "").toLowerCase();
		const exact = entries.find((model) => model.id.toLowerCase() === id);
		const label = exact?.label ?? newest.find((model) => model.id.toLowerCase().includes(`-${id}-`) || model.label.toLowerCase().startsWith(id))?.label;
		// A larger-context row runs the same model; without its mark it would
		// read as a second copy of that model.
		return label && /\[1m\]$/i.test(value) && !/1m/i.test(label) ? `${label} (1M context)` : label;
	};
}

function ModelEffortPicker({
	models,
	settings,
	onChange,
	disabled,
	modelLabel,
	groupLabel,
	efforts,
	defaultEffort,
	availability,
	reroute,
	rerouted,
	chosenLabel,
	executionMode,
	planReturn,
	toggles = [],
	extraOptions = [],
	onChangeConfigOption,
}: {
	models: ChatModel[];
	settings: TurnSettings;
	onChange: (next: TurnSettings) => void;
	disabled?: boolean;
	modelLabel: string;
	groupLabel: string;
	efforts: string[];
	defaultEffort?: string;
	availability: "supported" | "unsupported" | "unknown";
	reroute?: ModelReroute;
	rerouted?: string;
	chosenLabel: string;
	executionMode?: ChatConfigOption;
	planReturn?: string;
	toggles?: ChatConfigOption[];
	extraOptions?: ChatConfigOption[];
	onChangeConfigOption?: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	const { t } = useTranslation();
	const catalog = useMemo(() => models.map((model) => ({ ...model, label: model.displayName })), [models]);
	const effortChoices = useMemo(() => efforts.map((value) => ({ value })), [efforts]);
	const savedEffort = settings.reasoningEffort === "default" ? "" : settings.reasoningEffort ?? "";

	return (
		<OptionMenu>
				<OptionMenuTrigger
					showCaret={false}
					disabled={disabled}
					aria-label="Model and reasoning effort for the next turn"
					title={
						reroute
							? `The provider answered with ${rerouted} instead of ${reroute.fromModel ?? chosenLabel}${
									reroute.reason ? `: ${reroute.reason}` : ""
								}`
							: "Model and reasoning effort for the next turn"
					}
					className={TRIGGER_CLASS}
				>
					<span className="min-w-0 max-w-[38ch] truncate">{groupLabel}</span>
					{reroute ? (
						// A mark, not a second name. Two truncated model names side by side is
						// less legible than one readable name plus a flag that says it is not
						// the one that was asked for; the tooltip and the menu spell out which.
						<Shuffle
							className="size-3 shrink-0 text-warning"
							aria-label={`Substituted for ${reroute.fromModel ?? chosenLabel}`}
						/>
					) : null}
				</OptionMenuTrigger>
			<OptionMenuContent align="start" className={CHAT_MENU_CLASS}>
				<OptionMenuSub>
					<OptionMenuSubTrigger label="Model" value={modelLabel} />
					{/* Scroll on an inner strip: the surface utility caps height but wheel
					    events do not reliably reach an outer overflow on nested submenus. */}
					<OptionMenuSubContent scrollable className={CHAT_MENU_CLASS} onFocus={focusModelSearch}>
						<ModelMenuChoices models={catalog}>
							{(matches, searchActiveID, optionID) => matches.map((model) => (
								<OptionMenuItem
									key={model.id}
									id={optionID?.(model.id)}
									active={model.id === settings.model}
									searchActive={model.id === searchActiveID}
									radio
									onSelect={() => onChange({ ...settings, model: model.id, reasoningEffort: undefined })}
									className={cn("text-xs", model.id === settings.model ? "text-foreground" : "text-muted-foreground")}
								>
									{model.displayName}
								</OptionMenuItem>
							))}
						</ModelMenuChoices>
					</OptionMenuSubContent>
				</OptionMenuSub>

				{efforts.length > 0 || savedEffort ? (
					<OptionMenuSub>
						<OptionMenuSubTrigger
							label={t("settings.models.effort")}
							value={effortDisplayLabel({ value: savedEffort, choices: effortChoices, followLabel: t("settings.models.effort"), t, defaultEffort })}
						/>
						<OptionMenuSubContent className={CHAT_MENU_CLASS}>
							<EffortMenuItems
								value={savedEffort}
								choices={effortChoices}
								availability={availability}
								defaultEffort={defaultEffort}
								compact
								onChange={(value) => onChange({ ...settings, reasoningEffort: value || undefined })}
							/>
						</OptionMenuSubContent>
					</OptionMenuSub>
				) : null}
				{executionMode && onChangeConfigOption ? (
					<PlanModeToggle option={executionMode} planReturn={planReturn} onChange={onChangeConfigOption} />
				) : null}
				{toggles.map((option) => (
					<ConfigToggle key={option.id} option={option} onChange={onChangeConfigOption!} />
				))}
				{extraOptions.length > 0 && onChangeConfigOption ? (
					<MoreOptionsSubmenu options={extraOptions} onChange={onChangeConfigOption} />
				) : null}
			</OptionMenuContent>
		</OptionMenu>
	);
}

function ClubbedConfigPicker({
	modelOptions,
	effortOptions,
	executionMode,
	planReturn,
	toggles,
	extraOptions,
	disabled,
	onChange,
	groupClaude,
}: {
	groupClaude?: boolean;
	modelOptions: ChatConfigOption[];
	effortOptions: ChatConfigOption[];
	executionMode?: ChatConfigOption;
	planReturn?: string;
	toggles: ChatConfigOption[];
	extraOptions: ChatConfigOption[];
	disabled?: boolean;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	const { t } = useTranslation();
	const primaryModel = modelOptions.at(0);
	const primaryEffort = effortOptions.at(0);
	const modelLabel = primaryModel ? optionCurrentLabel(primaryModel) : undefined;
	const effortLabel = primaryEffort ? effortDisplayLabel({ ...acpEffortProps(primaryEffort), followLabel: "", t }) : undefined;
	const groupLabel = [modelLabel, effortLabel].filter(Boolean).join(" ") || "More";
	const leftCount =
		modelOptions.length + effortOptions.length + Number(Boolean(executionMode)) + toggles.length + extraOptions.length;
	if (leftCount === 0) return null;
	if (leftCount === 1) {
		if (primaryEffort) {
			return (
				<EffortPicker
					{...acpEffortMenuProps(primaryEffort)}
					disabled={disabled}
					triggerClassName={TRIGGER_CLASS}
					onChange={(value) => onChange(primaryEffort.id, { value })}
				/>
			);
		}
		if (executionMode)
			return (
				<ExecutionModePicker
					option={executionMode}
					planReturn={planReturn}
					disabled={disabled}
					onChange={onChange}
				/>
			);
		const option = primaryModel ?? toggles[0] ?? extraOptions[0];
		if (!option) return null;
		return (
			<ConfigOptionPicker
				option={option}
				disabled={disabled}
				onChange={(value) => onChange(option.id, value)}
				groupClaude={groupClaude}
			/>
		);
	}

	return (
		<OptionMenu>
				<OptionMenuTrigger
					showCaret={false}
					disabled={disabled}
					aria-label="Model and reasoning effort for the next turn"
					title="Model and reasoning effort for the next turn"
					className={TRIGGER_CLASS}
				>
					<span className="min-w-0 max-w-[38ch] truncate">{groupLabel}</span>
				</OptionMenuTrigger>
			<OptionMenuContent align="start" className={CHAT_MENU_CLASS}>
				{modelOptions.map((option) => (
					<OptionSubmenu key={option.id} option={option} onChange={onChange} scrollable groupClaude={groupClaude} />
				))}
				{effortOptions.map((option) => (
					<EffortOptionSubmenu key={option.id} option={option} onChange={onChange} />
				))}
				{executionMode ? <PlanModeToggle option={executionMode} planReturn={planReturn} onChange={onChange} /> : null}
				{toggles.map((option) => (
					<ConfigToggle key={option.id} option={option} onChange={onChange} />
				))}
				{extraOptions.length > 0 ? (
					<MoreOptionsSubmenu options={extraOptions} onChange={onChange} />
				) : null}
			</OptionMenuContent>
		</OptionMenu>
	);
}

// ACP exposes the provider-owned reset as a "default" choice whose description
// names the concrete level it resolves to.
function acpEffortMenuProps(option: ChatConfigOption) {
	const defaultChoice = option.choices.find((choice) => choice.value === "default");
	return {
		value: option.currentValue ?? "",
		choices: option.choices.map((choice) => ({ value: choice.value, label: choice.name })),
		defaultValue: defaultChoice?.value ?? null,
		defaultEffort: defaultChoice?.description,
	};
}

function acpEffortProps(option: ChatConfigOption) {
	const { value, choices, defaultEffort } = acpEffortMenuProps(option);
	return { value: value === "default" ? "" : value, choices, defaultEffort };
}

// ACP exposes the provider-owned reset as a "default" choice. When it names no
// concrete level, nothing is selected, so AO supplies one.
function acpFallbackEffort(option: ChatConfigOption): string | undefined {
	if (option.currentValue && option.currentValue !== "default") return undefined;
	const concrete = option.choices.filter((choice) => choice.value && choice.value !== "default");
	const described = option.choices.find((choice) => choice.value === "default")?.description?.trim().toLowerCase();
	const reported = concrete.find(
		(choice) => described && (choice.value.toLowerCase() === described || choice.name.toLowerCase() === described),
	)?.value;
	return fallbackEffort(
		concrete.map((choice) => choice.value),
		reported,
	);
}

function EffortOptionSubmenu({
	option,
	onChange,
}: {
	option: ChatConfigOption;
	onChange: (id: string, value: ChatConfigOptionValue) => void;
}) {
	const { t } = useTranslation();
	const followLabel = t("settings.models.effort");
	return (
		<OptionMenuSub>
			<OptionMenuSubTrigger label={followLabel} value={effortDisplayLabel({ ...acpEffortProps(option), followLabel, t })} />
			<OptionMenuSubContent className={CHAT_MENU_CLASS}>
				<EffortMenuItems {...acpEffortMenuProps(option)} compact onChange={(value) => onChange(option.id, { value })} />
			</OptionMenuSubContent>
		</OptionMenuSub>
	);
}

function PlanModeToggle({
	option,
	planReturn,
	onChange,
}: {
	option: ChatConfigOption;
	/** Where turning Plan off goes, when the provider shares one slot for both. */
	planReturn?: string;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	const planning = isPlanMode(option);
	const next = planning
		? planReturn ?? option.choices.find((choice) => !isPlanChoice(choice))?.value
		: option.choices.find((choice) => isPlanChoice(choice))?.value;
	if (!next) return null;
	return (
		<MenuToggle
			label="Plan Mode"
			checked={planning}
			onCheckedChange={() => onChange(option.id, { value: next })}
		/>
	);
}

function ConfigToggle({
	option,
	onChange,
}: {
	option: ChatConfigOption;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	return (
		<MenuToggle
			label={option.name}
			checked={optionIsEnabled(option)}
			onCheckedChange={(enabled) => {
				if (option.type === "boolean") {
					onChange(option.id, { enabled });
					return;
				}
				const next = option.choices.find((choice) => choiceIsEnabled(choice) === enabled);
				if (next) onChange(option.id, { value: next.value });
			}}
		/>
	);
}

function MenuToggle({
	label,
	checked,
	onCheckedChange,
}: {
	label: string;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
}) {
	return (
		<OptionMenuItem
			onSelect={(event) => event.preventDefault()}
			className="justify-between gap-4 px-3 py-2 text-xs"
		>
			<span>{label}</span>
			<Switch
				aria-label={label}
				checked={checked}
				onPointerDown={(event) => event.stopPropagation()}
				onClick={(event) => event.stopPropagation()}
				onCheckedChange={onCheckedChange}
			/>
		</OptionMenuItem>
	);
}

function ExecutionModePicker({
	option,
	planReturn,
	disabled,
	onChange,
}: {
	option: ChatConfigOption;
	planReturn?: string;
	disabled?: boolean;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	return (
		<OptionMenu>
			<OptionMenuTrigger
				showCaret={false}
				disabled={disabled}
				aria-label="Model mode for the next turn"
				title="Model mode for the next turn"
				className={TRIGGER_CLASS}
			>
				<span className="min-w-0 max-w-[16ch] truncate">{executionModeLabel(option)}</span>
			</OptionMenuTrigger>
			<OptionMenuContent align="start" className={CHAT_MENU_CLASS}>
				{isPlanBinary(option) ? (
					<PlanModeToggle option={option} planReturn={planReturn} onChange={onChange} />
				) : (
					<ConfigOptionChoices
						option={option}
						onChange={(value) => onChange(option.id, value)}
					/>
				)}
			</OptionMenuContent>
		</OptionMenu>
	);
}

function MoreOptionsSubmenu({
	options,
	onChange,
}: {
	options: ChatConfigOption[];
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	return (
		<OptionMenuSub>
			<OptionMenuSubTrigger label="More" />
			<OptionMenuSubContent className={CHAT_MENU_CLASS}>
				{options.map((option) => (
					<OptionSubmenu key={option.id} option={option} onChange={onChange} />
				))}
			</OptionMenuSubContent>
		</OptionMenuSub>
	);
}

function OptionSubmenu({
	option,
	label,
	onChange,
	scrollable,
	groupClaude,
}: {
	option: ChatConfigOption;
	/** A semantic label when one provider option is deliberately split in two. */
	label?: string;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
	scrollable?: boolean;
	groupClaude?: boolean;
}) {
	const current = optionCurrentLabel(option);
	return (
		<OptionMenuSub>
			<OptionMenuSubTrigger label={label ?? option.name} value={current} />
			<OptionMenuSubContent
				scrollable={scrollable}
				className={CHAT_MENU_CLASS}
				onFocus={isModelOption(option) ? focusModelSearch : undefined}
			>
				{isModelOption(option) ? (
					<ConfigModelChoices option={option} onChange={(value) => onChange(option.id, value)} groupClaude={groupClaude} />
				) : (
					<ConfigOptionChoices
						option={option}
						onChange={(value) => onChange(option.id, value)}
					/>
				)}
			</OptionMenuSubContent>
		</OptionMenuSub>
	);
}

function ConfigOptionPicker({
	option,
	label,
	title,
	onChange,
	disabled,
	footer,
	groupClaude,
}: {
	option: ChatConfigOption;
	label?: string;
	title?: string;
	onChange: (value: ChatConfigOptionValue) => void;
	disabled?: boolean;
	footer?: ReactNode;
	groupClaude?: boolean;
}) {
	return (
		<Picker
			label={label ?? optionCurrentLabel(option)}
			title={title || option.description || option.name}
			disabled={disabled}
			onFocus={isModelOption(option) ? focusModelSearch : undefined}
		>
			{isModelOption(option) ? (
				<ConfigModelChoices option={option} onChange={onChange} groupClaude={groupClaude} />
			) : (
				<ConfigOptionChoices option={option} onChange={onChange} />
			)}
			{footer}
		</Picker>
	);
}

function ConfigModelChoices({
	option,
	onChange,
	groupClaude,
}: {
	option: ChatConfigOption;
	onChange: (value: ChatConfigOptionValue) => void;
	/** Newest model per Claude family first, the rest behind a toggle, as in the new-task picker. */
	groupClaude?: boolean;
}) {
	const { t } = useTranslation();
	const models = useMemo(() => option.choices.map((choice) => ({
		...choice,
		id: choice.value,
		label: choice.name,
	})), [option.choices]);
	const claude = useMemo(() => (groupClaude ? splitClaudeModels(models) : undefined), [groupClaude, models]);
	const [otherOpen, setOtherOpen] = useState(false);
	const selectedIsOther = Boolean(claude?.other.some((model) => model.id === option.currentValue));
	const showOther = otherOpen || selectedIsOther;
	const menuModels = claude ? (showOther ? [...claude.current, ...claude.other] : claude.current) : models;
	return (
		<ModelMenuChoices models={menuModels}>
			{(matches, searchActiveID, optionID) => (
				<>
					<ConfigOptionChoices
						option={{ ...option, choices: matches }}
						onChange={onChange}
						searchActiveID={searchActiveID}
						optionID={optionID}
					/>
					{claude?.other.length && !selectedIsOther ? (
						<OptionMenuItem
							// The composer focuses its editor on any click that bubbles out of the portal,
							// which would pull focus out of the menu and close it.
							onClick={(event) => event.stopPropagation()}
							onSelect={(event) => {
								event.preventDefault();
								setOtherOpen((open) => !open);
							}}
							className="text-xs text-muted-foreground"
							aria-expanded={otherOpen}
						>
							{t("settings.models.otherModels")}
							<ChevronDown className={cn("ml-auto size-3 opacity-70 transition-transform", otherOpen && "rotate-180")} aria-hidden="true" />
						</OptionMenuItem>
					) : null}
				</>
			)}
		</ModelMenuChoices>
	);
}

function ConfigOptionChoices({
	option,
	onChange,
	searchActiveID,
	optionID,
}: {
	option: ChatConfigOption;
	onChange: (value: ChatConfigOptionValue) => void;
	searchActiveID?: string;
	optionID?: (id: string) => string;
}) {
	if (option.type === "boolean") {
		return (
			<>
				{[true, false].map((enabled) => (
					<OptionMenuItem
							key={String(enabled)}
							active={enabled === option.currentBoolean}
							radio
						onSelect={() => onChange({ enabled })}
						className={cn("text-xs")}
					>
						<span
							className={cn(
								enabled === option.currentBoolean
									? "text-foreground"
									: "text-muted-foreground",
							)}
						>
							{enabled ? "On" : "Off"}
						</span>
					</OptionMenuItem>
				))}
			</>
		);
	}

	return (
		<>
			{option.choices.map((choice, index) => {
				const previousGroup = index > 0 ? option.choices[index - 1]?.group : undefined;
				return (
					<Fragment key={choice.value}>
						{choice.group && choice.group !== previousGroup ? (
							<OptionMenuLabel className="px-3 pb-1 pt-2 text-micro text-muted-foreground">
								{choice.groupName || choice.group}
							</OptionMenuLabel>
						) : null}
						<OptionMenuItem
							id={optionID?.(choice.value)}
							active={choice.value === option.currentValue}
							searchActive={choice.value === searchActiveID}
							radio
							onSelect={() => onChange({ value: choice.value })}
							className={cn("text-xs")}
						>
							<span className="flex w-full items-baseline gap-2">
								<span
									className={cn(
										"text-xs",
										choice.value === option.currentValue
											? "text-foreground"
											: "text-muted-foreground",
									)}
								>
									{choice.name}
								</span>
							</span>
						</OptionMenuItem>
					</Fragment>
				);
			})}
		</>
	);
}

/**
 * One dropdown, wearing the chrome Settings uses. These controls are the same
 * kind of thing as a settings row's — pick one of a list — so they are drawn the
 * same way, and the panel sizes itself from the shared surface rather than each
 * picker naming a width of its own.
 */
function Picker({
	label,
	title,
	disabled,
	badge,
	children,
	onFocus,
}: {
	label: string;
	title: string;
	disabled?: boolean;
	/** A note that belongs on the trigger, e.g. the model that was overridden. */
	badge?: React.ReactNode;
	children: React.ReactNode;
	onFocus?: (event: FocusEvent<HTMLDivElement>) => void;
}) {
	return (
		<OptionMenu>
				<OptionMenuTrigger
					showCaret={false}
					aria-label={title}
					title={title}
					disabled={disabled}
					className={TRIGGER_CLASS}
				>
					<span className="min-w-0 max-w-[16ch] truncate">{label}</span>
					{badge}
				</OptionMenuTrigger>
			<OptionMenuContent align="end" className={CHAT_MENU_CLASS} onFocus={onFocus}>
				{children}
			</OptionMenuContent>
		</OptionMenu>
	);
}

function focusModelSearch(event: FocusEvent<HTMLDivElement>) {
	if (event.target !== event.currentTarget) return;
	const search = event.currentTarget.querySelector<HTMLInputElement>('input[type="search"]');
	if (search) {
		// Focus search before the menu's roving focus chooses a model.
		event.preventDefault();
		search.focus();
	}
}

function isModelOption(option: ChatConfigOption): boolean {
	return option.category === "model" || option.id === "model";
}

/**
 * ACP may advertise an `agent` option for its own multi-agent internals. It is
 * not AO's harness switcher and is not a model choice, so exposing it in the
 * composer promises a meaning AO cannot guarantee. Harness changes remain in
 * the dedicated session switcher.
 */
function isAgentOption(option: ChatConfigOption): boolean {
	return option.id === "agent" || option.category === "agent";
}

function isEffortOption(option: ChatConfigOption): boolean {
	return option.category === "thought_level" || option.id === "effort";
}

function isModeOption(option: ChatConfigOption): boolean {
	return option.category === "mode" || option.id === "mode";
}

function isInlineToggleOption(option: ChatConfigOption): boolean {
	return option.type === "boolean" || /(?:^|[\s_-])fast(?:[\s_-]|$)/i.test(`${option.id} ${option.name}`);
}

function optionIsEnabled(option: ChatConfigOption): boolean {
	if (option.type === "boolean") return Boolean(option.currentBoolean);
	return choiceIsEnabled(option.choices.find((choice) => choice.value === option.currentValue));
}

function choiceIsEnabled(choice: ChatConfigOption["choices"][number] | undefined): boolean {
	return Boolean(choice && /(?:^|[\s_-])(on|enabled|true)(?:[\s_-]|$)/i.test(`${choice.name} ${choice.value}`));
}

/**
 * Whether a provider catalog replaces AO's own approval control. A `mode` option
 * that offers only execution modes (OpenCode's build/plan) is not one: taking it
 * for an approval catalog leaves the session with no permission control at all.
 */
export function hasProviderPermissionMode(options: ChatConfigOption[]): boolean {
	return Boolean(partitionConfigOptions(options).mode);
}

function partitionConfigOptions(options: ChatConfigOption[]): {
	model: ChatConfigOption[];
	effort: ChatConfigOption[];
	/** Provider plan/agent modes, placed with model selection rather than permissions. */
	executionMode: ChatConfigOption | undefined;
	/** Compact boolean controls, such as Fast Mode, rendered beside Plan Mode. */
	toggles: ChatConfigOption[];
	mode: ChatConfigOption | undefined;
	extra: ChatConfigOption[];
} {
	const primaryModel: ChatConfigOption[] = [];
	const otherModel: ChatConfigOption[] = [];
	const effort: ChatConfigOption[] = [];
	const toggles: ChatConfigOption[] = [];
	const extra: ChatConfigOption[] = [];
	let executionMode: ChatConfigOption | undefined;
	let mode: ChatConfigOption | undefined;
	for (const rawOption of options) {
		const option = rawOption.type === "select"
			? isEffortOption(rawOption)
				? rawOption
				: resolveImplicitChoice(rawOption)
			: rawOption;
		if (option.type === "select" && option.choices.length === 0 && !isEffortOption(option)) continue;
		if (isAgentOption(option)) continue;
		if (isModelOption(option)) {
			if (option.category === "model" || option.id === "model") primaryModel.push(option);
			else otherModel.push(option);
			continue;
		}
		if (isEffortOption(option)) {
			if (!option.choices.some((choice) => choice.value && choice.value !== "default")) continue;
			effort.push(option);
			continue;
		}
		if (isInlineToggleOption(option)) {
			toggles.push(option);
			continue;
		}
		if (isModeOption(option) && !mode) {
			// Classify before synthesizing Claude's Agent choice so it cannot promote approval choices.
			const executionValues = executionChoiceValues(option.choices);
			const permissionChoices = option.choices.filter((choice) => !executionValues.has(choice.value));
			const executionChoices = addAgentModeChoice(
				option.choices.filter((choice) => executionValues.has(choice.value)),
				permissionChoices,
			);
			if (executionChoices.length > 0) {
				executionMode = withChoices(option, executionChoices);
				if (permissionChoices.length > 0) mode = withChoices(option, permissionChoices);
			} else {
				mode = option;
			}
			continue;
		}
		extra.push(option);
	}
	return { model: [...primaryModel, ...otherModel], effort, executionMode, toggles, mode, extra };
}

function resolveImplicitChoice(option: ChatConfigOption): ChatConfigOption {
	const mapped = isModeOption(option) ? {
		...option,
		choices: option.choices.map((choice) =>
			choice.permissionMode === "default" && isDefaultPlaceholderLabel(choice.name)
				? { ...choice, name: "Use agent permissions" }
				: choice,
		),
	} : option;
	const implicit = mapped.choices.find((choice) =>
		choice.value === "default" && (
			!isModeOption(mapped) || (choice.permissionMode !== "default" && (isDefaultPlaceholderLabel(choice.name) || /^use agent permissions$/i.test(choice.name.trim())))
		),
	);
	if (!implicit) return mapped;
	if (isModeOption(mapped)) return { ...mapped, choices: mapped.choices.filter((choice) => choice !== implicit) };
	const concrete = mapped.choices.find((choice) =>
		choice.value !== implicit.value && choice.name.toLowerCase() === implicit.description?.trim().toLowerCase(),
	);
	const followLabel = isModelOption(mapped) ? "Use agent model" : "Use agent setting";
	if (concrete && mapped.currentValue !== concrete.value) {
		const label = isDefaultPlaceholderLabel(concrete.name) ? concrete.value : concrete.name;
		return {
			...mapped,
			choices: mapped.choices.filter((choice) => choice !== concrete).map((choice) =>
				choice === implicit ? { ...choice, name: label } : choice,
			),
		};
	}
	// A model picker lists models only; the agent's own choice is not a row of its own.
	if (isModelOption(mapped) && mapped.currentValue !== implicit.value) {
		return { ...mapped, choices: mapped.choices.filter((choice) => choice !== implicit) };
	}
	return {
		...mapped,
		choices: mapped.choices.map((choice) => choice === implicit && isDefaultPlaceholderLabel(choice.name)
			? { ...choice, name: concrete ? `${followLabel} (${concrete.name})` : followLabel }
			: choice),
	};
}

/** Ask is an execution mode only when the same option advertises Agent; otherwise it is an approval policy. */
function executionChoiceValues(choices: ChatConfigOption["choices"]): Set<string> {
	const values = new Set<string>();
	const hasAgent = choices.some((choice) => executionChoiceMatches(choice, "agent"));
	for (const choice of choices) {
		if (executionChoiceMatches(choice, "plan|agent|build")) {
			values.add(choice.value);
			continue;
		}
		if (hasAgent && executionChoiceMatches(choice, "ask")) values.add(choice.value);
	}
	return values;
}

// Match complete mode labels so custom agent names cannot masquerade as native modes.
function executionChoiceMatches(choice: ChatConfigOption["choices"][number], word: string): boolean {
	return [choice.value, choice.name].some((value) =>
		new RegExp(`^(?:${word})(?:[\\s_-]mode)?$`, "i").test(value.trim()),
	);
}

function choiceMatches(
	choice: Pick<ChatConfigOption["choices"][number], "name" | "value">,
	word: string,
): boolean {
	return new RegExp(`(?:^|[\\s_-])(?:${word})(?:[\\s_-]|$)`, "i").test(`${choice.name} ${choice.value}`);
}

/**
 * Claude currently sends Plan as an execution mode but describes its ordinary
 * interactive posture as Manual. Present the latter as Agent Mode next to Plan,
 * without inventing a value: choosing it still sends the provider's own Manual
 * value. Other harnesses that advertise Agent Mode explicitly stay untouched.
 */
function addAgentModeChoice(
	executionChoices: ChatConfigOption["choices"],
	permissionChoices: ChatConfigOption["choices"],
): ChatConfigOption["choices"] {
	// OpenCode names its ordinary agent mode "build", so its catalog already has
	// one and must not gain a synthetic duplicate.
	if (executionChoices.some((choice) => executionChoiceMatches(choice, "agent|build"))) {
		return executionChoices;
	}
	const standard = permissionChoices.find((choice) => choiceMatches(choice, "manual"))
		?? permissionChoices.find((choice) => choiceMatches(choice, "default|standard"));
	return standard
		? [{ ...standard, name: "Agent Mode", description: "Standard agent execution" }, ...executionChoices]
		: executionChoices;
}

function isPlanBinary(option: ChatConfigOption): boolean {
	return option.choices.length === 2 && option.choices.some(isPlanChoice);
}

function executionModeLabel(option: ChatConfigOption): string {
	if (isPlanBinary(option)) return isPlanMode(option) ? "Plan Mode" : "Agent Mode";
	return option.choices.find((choice) => choice.value === option.currentValue)?.name ?? option.name;
}

function isPlanMode(option: ChatConfigOption | undefined): boolean {
	return Boolean(option?.currentValue && isPlanChoice({ value: option.currentValue, name: option.currentValue }));
}

function isPlanChoice(choice: Pick<ChatConfigOption["choices"][number], "name" | "value">): boolean {
	// Custom agent IDs can contain "plan" without enabling native planning.
	return [choice.value, choice.name].some((value) => /^plan(?:[\s_-]mode)?$/i.test(value.trim()));
}

function withChoices(
	option: ChatConfigOption,
	choices: ChatConfigOption["choices"],
	name = option.name,
): ChatConfigOption {
	const currentValue = choices.some((choice) => choice.value === option.currentValue)
		? option.currentValue
		: undefined;
	return { ...option, name, currentValue, choices };
}

function optionCurrentLabel(option: ChatConfigOption): string {
	if (option.type === "boolean") return option.currentBoolean ? "On" : "Off";
	if (option.currentValue === "default" && !option.choices.some((choice) => choice.value === "default")) {
		if (isEffortOption(option)) return "Effort not reported";
		if (isModelOption(option)) return "Model not reported";
		return "Selection not reported";
	}
	return option.choices.find((choice) => choice.value === option.currentValue)?.name
		?? option.currentValue
		?? option.name;
}
