import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	canSubmitProjectSetup,
	ProjectSetupFormView,
	ProjectSetupHeaderView,
} from "@aoagents/product-ui";
import { useTranslation } from "react-i18next";
import * as Dialog from "@radix-ui/react-dialog";
import { TriangleAlert, X, type LucideIcon } from "lucide-react";
import { memo, useEffect, useState, type ReactNode } from "react";
import type { components } from "../../api/schema";
import { agentsQueryKey, agentsQueryOptions, refreshAgents } from "../hooks/useAgentsQuery";
import { AGENT_OPTIONS } from "../lib/agent-options";
import {
	agentLabelCompare,
	buildRankedAgentOptions,
	DEFAULT_AGENT_PRIORITY,
	DEFAULT_AGENT_PRIORITY_RANK,
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

type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

type AgentInfo = components["schemas"]["AgentInfo"];

export type CreateProjectAgentSelection = {
	workerAgent: string;
	orchestratorAgent: string;
	trackerIntake?: TrackerIntakeConfig;
};

const EMPTY_INTAKE: IntakeForm = { enabled: false, repo: "", assignee: "" };
type CreateProjectAgentSheetProps = {
	error?: string | null;
	isCreating: boolean;
	isInitializing?: boolean;
	kind: ProjectKind;
	onOpenChange: (open: boolean) => void;
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

function projectSheetError(error: string): SheetError {
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
					: appI18n.t("createProject.error.createFailedTitle"),
				message: message || appI18n.t("createProject.error.tryAgain"),
				tone: "error",
			};
	}
}

export function CreateProjectAgentSheet({
	error,
	isCreating,
	isInitializing = false,
	kind,
	onOpenChange,
	onSubmit,
	open,
	path,
	repositorySetupNeeded = false,
	repositorySetupWarning = null,
}: CreateProjectAgentSheetProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const agentsQuery = useQuery({
		...agentsQueryOptions,
		enabled: open,
	});
	const refreshAgentsMutation = useMutation({
		mutationFn: refreshAgents,
		onSuccess: (next) => queryClient.setQueryData(agentsQueryKey, next),
	});
	const agents = agentsQuery.data;
	const installedAgents = agents?.installed ?? [];
	const agentOptions = agents?.authorized ?? [];
	const supportedAgents = agents?.supported ?? [];
	const isLoadingAgents = agents === undefined && agentsQuery.isFetching;
	const agentsError = agentsQuery.isError
		? agentsQuery.error instanceof Error
			? agentsQuery.error.message
			: t("createProject.couldNotLoadAgents")
		: null;
	const displayError = refreshAgentsMutation.isError
		? refreshAgentsMutation.error instanceof Error
			? refreshAgentsMutation.error.message
			: t("createProject.couldNotRefreshAgents")
		: agentsError;
	const [workerAgent, setWorkerAgent] = useState("");
	const [orchestratorAgent, setOrchestratorAgent] = useState("");
	const [workerAgentTouched, setWorkerAgentTouched] = useState(false);
	const [orchestratorAgentTouched, setOrchestratorAgentTouched] = useState(false);
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
	const sheetError = error ? projectSheetError(error) : null;

	useEffect(() => {
		if (!open) return;
		const defaultAgent = defaultAuthorizedAgent(agentOptions);
		if (!workerAgentTouched) setWorkerAgent(defaultAgent);
		if (!orchestratorAgentTouched) setOrchestratorAgent(defaultAgent);
	}, [agentOptions, open, orchestratorAgentTouched, workerAgentTouched]);

	useEffect(() => {
		if (!open) {
			setWorkerAgent("");
			setOrchestratorAgent("");
			setWorkerAgentTouched(false);
			setOrchestratorAgentTouched(false);
			setIntake(EMPTY_INTAKE);
		}
	}, [open, path]);

	return (
		<Dialog.Root open={open} onOpenChange={(next) => !isBusy && onOpenChange(next)}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(480px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-agents-sheet border border-[var(--color-border-agents-sheet)] bg-[var(--color-bg-agents-sheet)] p-0 text-[var(--color-text-agents-sheet-title)] shadow-[var(--shadow-import-modal)] data-[state=open]:animate-modal-in">
					<ProjectSetupHeaderView
						CloseButton={ProjectSheetCloseButton}
						Description={Dialog.Description}
						Title={Dialog.Title}
						closeIcon={<X className="size-icon-base" aria-hidden="true" />}
						closeLabel={t("createProject.closeAgents")}
						disabled={isBusy}
						path={path ?? ""}
						title={
							kind === "workspace"
								? t("createProject.workspaceAgents")
								: t("createProject.projectAgents")
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
									authorized={agentOptions}
									installed={installedAgents}
									supported={supportedAgents}
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
									authorized={agentOptions}
									installed={installedAgents}
									supported={supportedAgents}
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
							cacheMessage: t("createProject.agentsCached"),
							error: displayError,
							loading: isLoadingAgents,
							loadingMessage: t("createProject.loadingAgents"),
							onRefresh: () => refreshAgentsMutation.mutate(),
							refreshLabel: refreshAgentsMutation.isPending
								? t("createProject.refreshing")
								: t("createProject.refreshAgents"),
							refreshing: refreshAgentsMutation.isPending,
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
						cancelLabel={t("createProject.cancel")}
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
									? t("createProject.creating")
									: kind === "workspace"
										? t("createProject.createWorkspaceAndStart")
										: t("createProject.createAndStart")
						}
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
	authorized,
	disabled = false,
	hint,
	icon,
	id,
	invalid = false,
	installed,
	label,
	onChange,
	placeholder,
	supported,
	triggerClassName,
	labelClassName,
	contentClassName,
	value,
	variant = "stacked",
}: {
	authorized?: AgentInfo[];
	disabled?: boolean;
	/** Caption beside the label, e.g. naming where a preselected default came from. */
	hint?: string;
	icon?: LucideIcon;
	id: string;
	invalid?: boolean;
	installed?: AgentInfo[];
	label: string;
	onChange: (value: string) => void;
	placeholder: string;
	supported?: AgentInfo[];
	triggerClassName?: string;
	labelClassName?: string;
	contentClassName?: string;
	value: string;
	variant?: "stacked" | "settings-row" | "chip";
}) {
	const fallbackAgents: AgentInfo[] = AGENT_OPTIONS.map((agent) => ({ id: agent, label: agent }));
	const options = buildRankedAgentOptions({
		supported,
		installed,
		authorized,
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
	const authorizedIds = new Set(authorizedAgents.map((agent) => agent.id));
	const prioritized = DEFAULT_AGENT_PRIORITY.find((agent) => authorizedIds.has(agent));
	if (prioritized) return prioritized;
	return [...authorizedAgents].sort(agentLabelCompare)[0]?.id ?? "";
}
