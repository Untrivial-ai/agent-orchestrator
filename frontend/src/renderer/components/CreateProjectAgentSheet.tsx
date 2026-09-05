import {
	canSubmitProjectSetup,
	ProjectSetupFormView,
	ProjectSetupHeaderView,
} from "@aoagents/product-ui";
import { useTranslation } from "react-i18next";
import * as Dialog from "@radix-ui/react-dialog";
import { ChevronLeft, TriangleAlert, X, type LucideIcon } from "lucide-react";
import { memo, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { components } from "../../api/schema";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import { AGENT_OPTIONS } from "../lib/agent-options";
import {
	agentLabelCompare,
	agentUsageCompare,
	buildRankedAgentOptions,
	DEFAULT_AGENT_PRIORITY_RANK,
	type AgentInfo,
	unknownAgentReadiness,
} from "../lib/agent-select-options";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./AgentAvatar";
import { FieldDefaultHint } from "./FieldDefaultHint";
import { buildIntake, type IntakeForm, IntakeFields, intakeNeedsRule } from "./IntakeFields";
import { AgentSelectMenuItem } from "./settings/AgentSelectMenuItem";
import { SettingsRow } from "./settings/SettingsRow";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import type { ProjectKind } from "../types/workspace";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { appI18n } from "../i18n";
import { Button } from "./ui/button";

type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

export type CreateProjectAgentSelection = {
	workerAgent: string;
	orchestratorAgent: string;
	trackerIntake?: TrackerIntakeConfig;
};

const EMPTY_INTAKE: IntakeForm = { enabled: false, repo: "", assignee: "" };
type CreateProjectAgentSheetProps = {
	error?: string | null;
	action?: "create" | "clone";
	isCreating: boolean;
	isInitializing?: boolean;
	kind: ProjectKind;
	onOpenChange: (open: boolean) => void;
	onBack?: () => void;
	onSubmit: (selection: CreateProjectAgentSelection) => Promise<void>;
	open: boolean;
	path: string | null;
	repositorySetupNeeded?: boolean;
	repositorySetupWarning?: string | null;
};

type SheetError = {
	title: string;
	message: string;
	tone: "warning" | "error";
};

function projectSheetError(error: string, action: "create" | "clone"): SheetError {
	const setupMessage = error.replace(/^Setup failed:\s*/i, "").trim();
	const codeMatch = setupMessage.match(/\(([A-Z0-9_]+)\)\s*$/);
	const code = codeMatch?.[1];
	const message = codeMatch ? setupMessage.slice(0, codeMatch.index).trim() : setupMessage;

	switch (code) {
		case "PROJECT_PATH_NOT_REPO_ROOT":
			return {
				title: appI18n.t("createProject.error.notRepoRootTitle"),
				message: appI18n.t("createProject.error.notRepoRootBody"),
				tone: "warning",
			};
		case "PROJECT_BARE_REPOSITORY":
			return {
				title: appI18n.t("createProject.error.bareTitle"),
				message: appI18n.t("createProject.error.bareBody"),
				tone: "warning",
			};
		case "UNSUPPORTED_GIT_REPO":
			return {
				title: appI18n.t("createProject.error.unsupportedTitle"),
				message: appI18n.t("createProject.error.unsupportedBody"),
				tone: "warning",
			};
		default:
			return {
				title: error.toLowerCase().startsWith("setup failed:")
					? appI18n.t("createProject.error.setupFailedTitle")
					: action === "clone"
						? appI18n.t("createProject.cloneFailedTitle")
						: appI18n.t("createProject.error.createFailedTitle"),
				message: message || appI18n.t("createProject.error.tryAgain"),
				tone: "error",
			};
	}
}

export function CreateProjectAgentSheet({
	action = "create",
	error,
	isCreating,
	isInitializing = false,
	kind,
	onBack,
	onOpenChange,
	onSubmit,
	open,
	path,
	repositorySetupNeeded = false,
	repositorySetupWarning = null,
}: CreateProjectAgentSheetProps) {
	const { t } = useTranslation();
	const [isExiting, setIsExiting] = useState(false);
	const contentOpen = open || isExiting;
	const displayedAction = useRef(action);
	const displayedError = useRef(error);
	const displayedOnBack = useRef(onBack);
	if (open) {
		displayedAction.current = action;
		displayedError.current = error;
		displayedOnBack.current = onBack;
	}
	const agentsQuery = useAgentReadinessQuery(contentOpen);
	useEnsureAgentReadiness({ enabled: contentOpen });
	const agents = agentsQuery.data;
	const agentOptions = useMemo(() => agents?.agents ?? [], [agents]);
	const authorizedAgents = useMemo(
		() =>
			agentOptions.filter((agent) =>
				["authorized", "not_applicable"].includes(agent.authentication.state),
			),
		[agentOptions],
	);
	const isLoadingAgents = agents === undefined && agentsQuery.isFetching;
	const agentsError = agentsQuery.isError
		? agentsQuery.error instanceof Error
			? agentsQuery.error.message
			: t("createProject.couldNotLoadAgents")
		: null;
	const displayError = agentsError;
	const [workerAgent, setWorkerAgent] = useState("");
	const [orchestratorAgent, setOrchestratorAgent] = useState("");
	const [workerAgentTouched, setWorkerAgentTouched] = useState(false);
	const [orchestratorAgentTouched, setOrchestratorAgentTouched] = useState(false);
	useEnsureAgentReadiness({
		agentIds: [workerAgent, orchestratorAgent],
		enabled: contentOpen && (workerAgent !== "" || orchestratorAgent !== ""),
	});
	const isBusy = isCreating || isInitializing;
	const [intake, setIntake] = useState<IntakeForm>(EMPTY_INTAKE);
	const intakeIncomplete = intakeNeedsRule(intake);
	const canSubmit =
		canSubmitProjectSetup({
			workerAgent,
			orchestratorAgent,
			intakeEnabled: intake.enabled,
			intakeAssignee: intake.assignee,
		}) &&
		!intakeIncomplete &&
		!isBusy &&
		!isLoadingAgents;
	const sheetError = displayedError.current
		? projectSheetError(displayedError.current, displayedAction.current)
		: null;
	const wasOpen = useRef(false);

	useEffect(() => {
		if (open && !wasOpen.current) {
			setWorkerAgent("");
			setOrchestratorAgent("");
			setWorkerAgentTouched(false);
			setOrchestratorAgentTouched(false);
			setIntake(EMPTY_INTAKE);
		}
		wasOpen.current = open;
	}, [open]);

	useEffect(() => {
		if (!open) return;
		const defaultAgent = defaultAuthorizedAgent(authorizedAgents);
		if (!workerAgentTouched) setWorkerAgent(defaultAgent);
		if (!orchestratorAgentTouched) setOrchestratorAgent(defaultAgent);
	}, [authorizedAgents, open, orchestratorAgentTouched, workerAgentTouched]);

	return (
		<Dialog.Root
			open={open}
			onOpenChange={(next) => {
				if (isBusy) return;
				setIsExiting(!next);
				onOpenChange(next);
			}}
		>
			<Dialog.Portal>
				<Dialog.Content
					className="fixed left-1/2 top-1/2 z-overlay w-dialog-lg -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none"
					onAnimationEnd={(event) => {
						if (!open && event.target === event.currentTarget) setIsExiting(false);
					}}
				>
					<ProjectSetupHeaderView
						CloseButton={ProjectSheetCloseButton}
						Description={Dialog.Description}
						Title={Dialog.Title}
						closeIcon={<X className="size-icon-base" aria-hidden="true" />}
						closeLabel={t("createProject.closeAgents")}
						disabled={isBusy}
						leadingAction={
							displayedOnBack.current ? (
								<Button
									type="button"
									variant="outline"
									size="icon"
									aria-label={t("createProject.cloneBackToDetails")}
									disabled={isBusy}
								onClick={displayedOnBack.current}
								>
									<ChevronLeft className="size-4" aria-hidden="true" />
								</Button>
							) : undefined
						}
						path={path ?? ""}
						showPath={false}
						title={
							kind === "workspace"
								? t("createProject.setupWorkspace")
								: t("createProject.setupProject")
						}
					/>
					<ProjectSetupFormView
						agentControls={{
							worker: (
								<RequiredAgentField
									id="newProjectWorkerAgent"
									label={t("createProject.workerAgent")}
									placeholder={t("createProject.selectWorker")}
									value={workerAgent}
									agents={agentOptions}
									disabled={isLoadingAgents}
									labelClassName="agents-sheet-label"
									triggerClassName="agents-sheet-control"
									contentClassName="agents-sheet-menu"
									onChange={(value) => {
										setWorkerAgent(value);
										setWorkerAgentTouched(true);
									}}
								/>
							),
							orchestrator: (
								<RequiredAgentField
									id="newProjectOrchestratorAgent"
									label={t("createProject.orchestratorAgent")}
									placeholder={t("createProject.selectOrchestrator")}
									value={orchestratorAgent}
									agents={agentOptions}
									disabled={isLoadingAgents}
									labelClassName="agents-sheet-label"
									triggerClassName="agents-sheet-control"
									contentClassName="agents-sheet-menu"
									onChange={(value) => {
										setOrchestratorAgent(value);
										setOrchestratorAgentTouched(true);
									}}
								/>
							),
						}}
						agents={{
							error: displayError,
							loading: isLoadingAgents,
							loadingMessage: t("createProject.loadingAgents"),
							onRetry: () => void agentsQuery.refetch(),
							retrying: agentsQuery.isFetching,
							retryLabel: t("createProject.retry"),
						}}
						alert={
							sheetError
								? {
										...sheetError,
										icon: (
											<TriangleAlert
												className={
													sheetError.tone === "warning"
														? "mt-0.5 size-icon-sm shrink-0 text-warning"
														: "mt-0.5 size-icon-sm shrink-0 text-destructive"
												}
												aria-hidden="true"
											/>
										),
									}
								: null
						}
						canSubmit={canSubmit}
						intakeControl={
							<IntakeFields
								form={intake}
								onChange={(patch) => setIntake((f) => ({ ...f, ...patch }))}
								compact
								controlClassName="agents-sheet-control"
								labelClassName="agents-sheet-label"
							/>
						}
						isBusy={isBusy}
						onCancel={() => onOpenChange(false)}
						onSubmit={() =>
							void onSubmit({ workerAgent, orchestratorAgent, trackerIntake: buildIntake(intake) })
						}
						setupNotice={
							repositorySetupNeeded
								? { message: t("createProject.gitSetupNotice"), warning: repositorySetupWarning }
								: null
						}
						submitLabel={
							isInitializing
								? t("createProject.settingUp")
								: isCreating
									? action === "clone"
										? t("createProject.cloning")
										: t("createProject.creating")
									: action === "clone"
										? t("createProject.clone")
										: kind === "workspace"
											? t("createProject.createWorkspaceAndStart")
											: t("createProject.createAndStart")
						}
						submitClassName={cn(
							"inline-flex h-control-form items-center gap-2 rounded-md bg-primary px-3 text-sm text-primary-foreground hover:bg-primary/80",
							(isCreating || isInitializing) && "before:size-3.5 before:shrink-0 before:animate-spin before:rounded-full before:border-2 before:border-current before:border-r-transparent before:content-['']",
						)}
					/>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function ProjectSheetCloseButton({
	children,
	disabled,
	"aria-label": ariaLabel,
}: {
	children: ReactNode;
	disabled: boolean;
	"aria-label": string;
}) {
	return (
		<Dialog.Close asChild>
			<button
				type="button"
				className="settings-close-button"
				aria-label={ariaLabel}
				disabled={disabled}
			>
				{children}
			</button>
		</Dialog.Close>
	);
}

export const RequiredAgentField = memo(function RequiredAgentField({
	agents,
	disabled = false,
	hint,
	icon,
	id,
	invalid = false,
	label,
	onChange,
	placeholder,
	triggerClassName,
	labelClassName,
	contentClassName,
	value,
	variant = "stacked",
}: {
	agents?: AgentInfo[];
	disabled?: boolean;
	/** Caption beside the label, e.g. naming where a preselected default came from. */
	hint?: string;
	icon?: LucideIcon;
	id: string;
	invalid?: boolean;
	label: string;
	onChange: (value: string) => void;
	placeholder: string;
	triggerClassName?: string;
	labelClassName?: string;
	contentClassName?: string;
	value: string;
	variant?: "stacked" | "settings-row" | "chip";
}) {
	const fallbackAgents: AgentInfo[] = AGENT_OPTIONS.map((agent) => unknownAgentReadiness(agent, agent));
	const options = buildRankedAgentOptions({
		agents,
		priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
		fallbackAgents,
	});

	if (variant === "settings-row") {
		const menuOptions = options.map((agent) => ({
			value: agent.id,
			label: agent.label,
			disabled: agent.disabled,
		}));

		return (
			<SettingsRow icon={icon} label={label}>
				<SettingsOptionMenu
					aria-label={label}
					value={value}
					placeholder={placeholder}
					options={menuOptions}
					disabled={disabled}
					onChange={onChange}
					triggerClassName={invalid ? "text-error" : undefined}
					menuClassName="settings-agent-menu-surface"
					menuItemClassName="settings-agent-menu-item"
					renderTrigger={(selected, triggerPlaceholder) => (
						<>
							{selected ? <AgentAvatar provider={selected.value} className="size-icon-lg" /> : null}
							<span className="min-w-0 truncate">{selected?.label ?? triggerPlaceholder}</span>
						</>
					)}
					renderMenuItem={(option, selected) => {
						const agent = options.find((entry) => entry.id === option.value);
						if (!agent) return option.label;
						return (
							<AgentSelectMenuItem
								agentId={agent.id}
								label={agent.label}
								selected={selected}
								status={agent.status}
								statusTone={agent.statusTone}
								disabled={agent.disabled}
							/>
						);
					}}
				/>
			</SettingsRow>
		);
	}

	const selectedOption = options.find((agent) => agent.id === value);

	// Chip: the value reads as part of a sentence ("Runs with Codex") rather than
	// as a form field, so the label is carried by that sentence, not by a <Label>.
	// Built on the same SettingsOptionMenu as the settings-row variant (and the
	// model chip beside it) so both halves of the pill share one dropdown
	// component instead of a Select-based menu and a DropdownMenu-based one.
	if (variant === "chip") {
		const menuOptions = options.map((agent) => ({
			value: agent.id,
			label: agent.label,
			disabled: agent.disabled,
		}));

		return (
			<SettingsOptionMenu
				aria-label={label}
				value={value}
				placeholder={placeholder}
				options={menuOptions}
				disabled={disabled}
				onChange={onChange}
				menuAlign="start"
				triggerClassName={cn(
					"composer-chip composer-toolbar-option w-full justify-between",
					invalid && "text-error",
					triggerClassName,
				)}
				menuClassName={contentClassName}
				renderTrigger={() => (
					<span className="flex min-w-0 items-center gap-2">
						{selectedOption ? (
							<AgentAvatar provider={selectedOption.id} className="size-icon-base" decorative />
						) : null}
						<span className="min-w-0 truncate text-control text-foreground" title={selectedOption?.label ?? placeholder}>
							{selectedOption?.label ?? placeholder}
						</span>
					</span>
				)}
				renderMenuItem={(option, selected) => {
					const agent = options.find((entry) => entry.id === option.value);
					if (!agent) return option.label;
					return (
						<AgentSelectMenuItem
							agentId={agent.id}
							label={agent.label}
							selected={selected}
							status={agent.status}
							statusTone={agent.statusTone}
							disabled={agent.disabled}
						/>
					);
				}}
			/>
		);
	}

	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex min-w-0 items-baseline gap-1.5">
				<Label htmlFor={id} className={cn("text-xs font-medium text-muted-foreground", labelClassName)}>
					{label}
				</Label>
				{hint && <FieldDefaultHint text={hint} />}
			</div>
			<Select value={value} onValueChange={onChange} disabled={disabled}>
				<SelectTrigger
					id={id}
					size="sm"
					className={cn("w-full text-control", triggerClassName)}
					aria-label={label}
					aria-invalid={invalid || undefined}
				>
					{/* Radix would otherwise clone the whole menu row into the trigger,
					    dragging the selected checkmark and install status with it. */}
					<SelectValue placeholder={placeholder}>
						{selectedOption ? (
							<span className="flex min-w-0 items-center gap-3">
								<AgentAvatar provider={selectedOption.id} className="size-icon-lg" decorative />
								<span className="min-w-0 truncate">{selectedOption.label}</span>
							</span>
						) : null}
					</SelectValue>
				</SelectTrigger>
				<SelectContent
					position="popper"
					side="bottom"
					align="start"
					sideOffset={4}
					className={cn("max-h-select-menu-max!", contentClassName)}
				>
					{options.map((agent) => (
						<SelectItem
							key={agent.id}
							value={agent.id}
							disabled={agent.disabled}
							className="[&>span:last-child]:w-full"
						>
							<AgentSelectMenuItem
								agentId={agent.id}
								label={agent.label}
								selected={value === agent.id}
								status={agent.status}
								statusTone={agent.statusTone}
								disabled={agent.disabled}
							/>
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</div>
	);
});

export function defaultAuthorizedAgent(authorizedAgents: AgentInfo[]): string {
	return [...authorizedAgents]
		.sort(
			(a, b) =>
				agentUsageCompare(a, b) ||
				(DEFAULT_AGENT_PRIORITY_RANK.get(a.id) ?? Number.MAX_SAFE_INTEGER) -
					(DEFAULT_AGENT_PRIORITY_RANK.get(b.id) ?? Number.MAX_SAFE_INTEGER) ||
				agentLabelCompare(a, b),
		)[0]?.id ?? "";
}
