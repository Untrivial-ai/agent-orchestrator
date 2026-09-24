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

import { Fragment, useMemo, type FocusEvent, type ReactNode } from "react";
import { Shuffle } from "lucide-react";
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
import { cn } from "../../lib/utils";
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
	"h-7 gap-1 bg-transparent rounded-lg px-3 text-[12px]! leading-none text-muted-foreground hover:bg-white/5 hover:text-foreground data-[state=open]:bg-white/5 data-[state=open]:text-foreground";
const CHAT_MENU_CLASS = "chat-settings-menu text-[12px]!";

export function TurnSettingsBar({
	models,
	settings,
	harness,
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
	children,
}: {
	models: ChatModel[];
	settings: TurnSettings;
	/** The active provider selects its own supported permission vocabulary. */
	harness?: string;
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
	error?: string;
	disabled?: boolean;
	/** Inline controls on the right model row, before the mode/approval picker — queue vs steer. */
	children?: ReactNode;
}) {
	const selected = models.find((model) => model.id === settings.model);
	const fallback = settings.model ? undefined : models.find((model) => model.default);
	// A catalog miss must not relabel an explicit choice or borrow another model's
	// effort settings. Custom or newly available models may not be listed yet.
	const chosenLabel =
		selected?.displayName ?? settings.model ?? fallback?.displayName ?? "Choose a model";
	const rerouted = reroute
		? models.find((model) => model.id === reroute.toModel)?.displayName ?? reroute.toModel
		: undefined;
	const modelLabel = rerouted ?? chosenLabel;
	const efforts = (selected ?? fallback)?.efforts ?? [];
	const effortLabel =
		settings.reasoningEffort ?? (selected ?? fallback)?.defaultEffort ?? undefined;
	const approvalCopy = harness === "codex" ? CODEX_APPROVAL_COPY : APPROVAL_COPY;
	const approvalOrder = harness === "codex" ? CODEX_APPROVAL_ORDER : APPROVAL_ORDER;
	const approvalLabel = approvalCopy[settings.approvalMode ?? "default"].label;
	const modelGroupLabel = effortLabel
		? `${modelLabel} ${capitalize(effortLabel)}`
		: modelLabel;
	const grouped = partitionConfigOptions(configOptions ?? []);
	const optionDisabled = Boolean(disabled || configPending || rememberPermissionsPending);
	const applyOption = (optionId: string, value: ChatConfigOptionValue) => {
		if (!onChangeConfigOption) return;
		void Promise.resolve(onChangeConfigOption(optionId, value)).catch(() => {});
	};
	const modeOption = grouped.mode;
	const visibleModeOption = modeOption && {
		...modeOption,
		choices: modeOption.choices.filter((choice) => !isImplicitApprovalChoice(choice)),
	};
	const currentModeChoice = modeOption?.choices.find((choice) => choice.value === modeOption.currentValue);
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
	const nativeModelMenu = Boolean(onChange && models.length > 0 && grouped.model.length === 0);
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
	const showRightDropdown = Boolean(children || (!planning && (onChange || modeOption)));

	return (
		<div role="group" aria-label="Turn settings" className="flex min-w-0 flex-1 flex-col gap-0.5">
			<div className="flex h-7 min-w-0 flex-1 items-center justify-between gap-2">
				<div className="flex h-7 min-w-0 flex-wrap items-center gap-0.5">
					{nativeModelMenu && onChange ? (
						<ModelEffortPicker
							models={models}
							settings={settings}
							onChange={onChange}
							disabled={optionDisabled}
							modelLabel={modelLabel}
							groupLabel={modelGroupLabel}
							effortLabel={effortLabel}
							efforts={efforts}
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
						{!planning && visibleModeOption && onChangeConfigOption ? (
							<ConfigOptionPicker
								option={visibleModeOption}
								label={currentModeChoice && isImplicitApprovalChoice(currentModeChoice) ? "Agent permissions not reported" : undefined}
								disabled={optionDisabled || visibleModeOption.choices.length === 0}
								onChange={(value) => applyOption(visibleModeOption.id, value)}
								footer={rememberAction}
							/>
						) : onChange ? (
							<Picker
								label={approvalLabel}
													title="Approval policy for the next turn"
													disabled={optionDisabled}
							>
								{approvalOrder.map((mode) => (
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
				<p role="status" className="px-1 text-[11px] text-muted-foreground">
					{rememberPermissionsPending ? "Saving project permissions…" : "Permission mode saved for new sessions in this project."}
				</p>
			) : null}
			{rememberPermissionsError ? (
				<p role="alert" className="px-1 text-[11px] text-destructive">{rememberPermissionsError}</p>
			) : null}
			{error ? (
				<p role="alert" className="px-1 text-[11px] leading-snug text-destructive">
					{error}
				</p>
			) : null}
		</div>
	);
}

function ModelEffortPicker({
	models,
	settings,
	onChange,
	disabled,
	modelLabel,
	groupLabel,
	effortLabel,
	efforts,
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
	effortLabel?: string;
	efforts: string[];
	reroute?: ModelReroute;
	rerouted?: string;
	chosenLabel: string;
	executionMode?: ChatConfigOption;
	planReturn?: string;
	toggles?: ChatConfigOption[];
	extraOptions?: ChatConfigOption[];
	onChangeConfigOption?: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	const catalog = useMemo(() => models.map((model) => ({ ...model, label: model.displayName })), [models]);

	return (
		<OptionMenu>
			
				<OptionMenuTrigger
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
							{(matches) => matches.map((model) => (
								<OptionMenuItem
									key={model.id}
									active={model.id === settings.model}
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

				{efforts.length > 0 ? (
					<OptionMenuSub>
						<OptionMenuSubTrigger label="Effort" value={effortLabel ? capitalize(effortLabel) : "Effort"} />
						<OptionMenuSubContent className={CHAT_MENU_CLASS}>
							{efforts.map((effort) => (
								<OptionMenuItem
									key={effort}
									active={effort === settings.reasoningEffort}
									radio
									onSelect={() => onChange({ ...settings, reasoningEffort: effort })}
									className={cn("text-xs")}
								>
									<span
										className={cn(
											effort === settings.reasoningEffort
												? "text-foreground"
												: "text-muted-foreground",
										)}
									>
										{capitalize(effort)}
									</span>
								</OptionMenuItem>
							))}
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
}: {
	modelOptions: ChatConfigOption[];
	effortOptions: ChatConfigOption[];
	executionMode?: ChatConfigOption;
	planReturn?: string;
	toggles: ChatConfigOption[];
	extraOptions: ChatConfigOption[];
	disabled?: boolean;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
}) {
	const primaryModel = modelOptions[0];
	const primaryEffort = effortOptions[0];
	const modelLabel = primaryModel ? optionCurrentLabel(primaryModel) : undefined;
	const effortLabel = primaryEffort ? optionCurrentLabel(primaryEffort) : undefined;
	const groupLabel = [modelLabel, effortLabel].filter(Boolean).join(" ") || "More";
	const leftCount =
		modelOptions.length + effortOptions.length + Number(Boolean(executionMode)) + toggles.length + extraOptions.length;
	if (leftCount === 1) {
		if (executionMode)
			return (
				<ExecutionModePicker
					option={executionMode}
					planReturn={planReturn}
					disabled={disabled}
					onChange={onChange}
				/>
			);
		const option = primaryModel ?? primaryEffort ?? executionMode ?? toggles[0] ?? extraOptions[0];
		if (!option) return null;
		return (
			<ConfigOptionPicker
				option={option}
				disabled={disabled}
				onChange={(value) => onChange(option.id, value)}
			/>
		);
	}

	return (
		<OptionMenu>
			
				<OptionMenuTrigger
					disabled={disabled}
					aria-label="Model and reasoning effort for the next turn"
					title="Model and reasoning effort for the next turn"
					className={TRIGGER_CLASS}
				>
					<span className="min-w-0 max-w-[38ch] truncate">{groupLabel}</span>
				</OptionMenuTrigger>
			<OptionMenuContent align="start" className={CHAT_MENU_CLASS}>
				{modelOptions.map((option) => (
					<OptionSubmenu key={option.id} option={option} onChange={onChange} scrollable />
				))}
				{effortOptions.map((option) => (
					<OptionSubmenu key={option.id} option={option} onChange={onChange} />
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
}: {
	option: ChatConfigOption;
	/** A semantic label when one provider option is deliberately split in two. */
	label?: string;
	onChange: (optionId: string, value: ChatConfigOptionValue) => void;
	scrollable?: boolean;
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
					<ConfigModelChoices option={option} onChange={(value) => onChange(option.id, value)} />
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
}: {
	option: ChatConfigOption;
	label?: string;
	title?: string;
	onChange: (value: ChatConfigOptionValue) => void;
	disabled?: boolean;
	footer?: ReactNode;
}) {
	return (
		<Picker
			label={label ?? optionCurrentLabel(option)}
			title={title || option.description || option.name}
			disabled={disabled}
			onFocus={isModelOption(option) ? focusModelSearch : undefined}
		>
			{isModelOption(option) ? (
				<ConfigModelChoices option={option} onChange={onChange} />
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
}: {
	option: ChatConfigOption;
	onChange: (value: ChatConfigOptionValue) => void;
}) {
	const models = useMemo(() => option.choices.map((choice) => ({
		...choice,
		id: choice.value,
		label: choice.name,
	})), [option.choices]);
	return (
		<ModelMenuChoices models={models}>
			{(matches) => <ConfigOptionChoices option={{ ...option, choices: matches }} onChange={onChange} />}
		</ModelMenuChoices>
	);
}

function ConfigOptionChoices({
	option,
	onChange,
}: {
	option: ChatConfigOption;
	onChange: (value: ChatConfigOptionValue) => void;
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
							<OptionMenuLabel className="px-3 pb-1 pt-2 text-[10px] uppercase tracking-wide text-muted-foreground">
								{choice.groupName || choice.group}
							</OptionMenuLabel>
						) : null}
						<OptionMenuItem
							active={choice.value === option.currentValue}
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
			
				<OptionMenuTrigger aria-label={title} title={title} disabled={disabled} className={TRIGGER_CLASS}>
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

function capitalize(value: string): string {
	return value.charAt(0).toUpperCase() + value.slice(1);
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

function isImplicitApprovalChoice(choice: ChatConfigOption["choices"][number]): boolean {
	return choice.permissionMode === "default" && (
		choice.value === "ao-default" ||
		/^(?:default(?:\s*\([^)]*\))?|use agent permissions)$/i.test(choice.name.trim())
	);
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
		const option = rawOption.type === "select" ? resolveImplicitChoice(rawOption) : rawOption;
		if (option.type === "select" && option.choices.length === 0) continue;
		if (isAgentOption(option)) continue;
		if (isModelOption(option)) {
			if (option.category === "model" || option.id === "model") primaryModel.push(option);
			else otherModel.push(option);
			continue;
		}
		if (isEffortOption(option)) {
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

// ACP may expose an implicit choice whose description names a concrete option.
// Select that option when known; otherwise leave the effective choice unreported.
function resolveImplicitChoice(option: ChatConfigOption): ChatConfigOption {
	const implicit = option.choices.find((choice) =>
		choice.value === "default" && (
			!isModeOption(option) || /^(?:default(?:\s*\([^)]*\))?|use agent permissions)$/i.test(choice.name.trim())
		),
	);
	if (!implicit) return option;
	const concrete = option.choices.find((choice) =>
		choice.value !== implicit.value && choice.name.toLowerCase() === implicit.description?.trim().toLowerCase(),
	);
	return {
		...option,
		currentValue: option.currentValue === implicit.value && concrete ? concrete.value : option.currentValue,
		choices: option.choices.filter((choice) => choice !== implicit),
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
