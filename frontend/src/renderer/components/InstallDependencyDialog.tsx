import { RadioGroup } from "radix-ui";
import { Check, Copy, XCircle } from "lucide-react";
import type { TFunction } from "i18next";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { cn } from "../lib/utils";
import { requirementDetailText, requirementDisplayLabel, type SystemRequirement } from "./SystemRequirementsChecklist";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

type InstallJob = components["schemas"]["InstallJob"];
type InstallTarget = "tmux" | "gh" | "claude" | "codex" | "opencode" | "copilot";
type AgentInstallTarget = Exclude<InstallTarget, "tmux" | "gh">;

// Labels are the CLIs' own product names — not translated, same treatment as
// "Agent Orchestrator" itself. Descriptions are ordinary UI copy and go
// through t() at render time (see AGENT_INSTALL_DESCRIPTION_KEYS below).
const AGENT_INSTALL_OPTIONS: Array<{ target: AgentInstallTarget; label: string }> = [
	{ target: "claude", label: "Claude Code" },
	{ target: "codex", label: "Codex" },
	{ target: "opencode", label: "opencode" },
	{ target: "copilot", label: "Copilot CLI" },
];

const AGENT_INSTALL_DESCRIPTION_KEYS: Record<AgentInstallTarget, MessageKey> = {
	claude: "startup.agentDescClaude",
	codex: "startup.agentDescCodex",
	opencode: "startup.agentDescOpencode",
	copilot: "startup.agentDescCopilot",
};

const POLL_INTERVAL_MS = 1_000;

export function isActiveInstallJob(job: InstallJob | undefined): boolean {
	return job?.status === "running" || job?.status === "installing" || job?.status === "verifying";
}

/** Sequential single-target install job runner: POST to start, GET on an
 *  interval while running. One target is ever in flight at a time — this
 *  gate only ever needs one, and serializing keeps the UI unambiguous about
 *  which command is running. */
function useInstallRunner(onSucceeded: () => void) {
	const [target, setTarget] = useState<InstallTarget | null>(null);
	const [job, setJob] = useState<InstallJob | undefined>(undefined);
	const [previews, setPreviews] = useState<Partial<Record<InstallTarget, InstallJob>>>({});
	const [inspectedTargets, setInspectedTargets] = useState<Partial<Record<InstallTarget, boolean>>>({});
	const [isStarting, setIsStarting] = useState(false);
	const [startError, setStartError] = useState<string | undefined>(undefined);
	const pollRef = useRef<number | null>(null);
	const inspectedRef = useRef(new Set<InstallTarget>());
	const onSucceededRef = useRef(onSucceeded);
	onSucceededRef.current = onSucceeded;

	const stopPolling = () => {
		if (pollRef.current !== null) {
			window.clearInterval(pollRef.current);
			pollRef.current = null;
		}
	};
	useEffect(() => stopPolling, []);

	const poll = (polledTarget: InstallTarget) => {
		stopPolling();
		pollRef.current = window.setInterval(() => {
			void (async () => {
				const { data, error } = await apiClient.GET("/api/v1/system/install/{target}", {
					params: { path: { target: polledTarget } },
				});
				if (error || !data) return; // transient — try again next tick
				setJob(data);
				if (isActiveInstallJob(data)) return;
				stopPolling();
				if (data.status === "succeeded") onSucceededRef.current();
			})();
		}, POLL_INTERVAL_MS);
	};

	// GET doubles as a safe install-plan preview. In particular, Linux returns
	// Unsupported plus the exact sudo command, so the renderer can show manual
	// instructions without first POSTing a job that is guaranteed to fail.
	const inspect = useCallback(async (nextTarget: InstallTarget) => {
		if (inspectedRef.current.has(nextTarget)) return;
		inspectedRef.current.add(nextTarget);
		try {
			const { data, error } = await apiClient.GET("/api/v1/system/install/{target}", {
				params: { path: { target: nextTarget } },
			});
			if (error || !data) return;
			setPreviews((current) => ({ ...current, [nextTarget]: data }));
		} catch {
			// The requirements gate remains usable if plan inspection fails. The
			// POST action will still surface its own concrete error when selected.
		} finally {
			setInspectedTargets((current) => ({ ...current, [nextTarget]: true }));
		}
	}, []);

	const start = async (nextTarget: InstallTarget) => {
		stopPolling();
		setTarget(nextTarget);
		setJob(undefined);
		setStartError(undefined);
		setIsStarting(true);
		try {
			const { data, error } = await apiClient.POST("/api/v1/system/install/{target}", {
				params: { path: { target: nextTarget } },
			});
			if (error || !data) throw new Error(apiErrorMessage(error, "Could not start the install."));
			setJob(data);
			if (isActiveInstallJob(data)) poll(nextTarget);
			else if (data.status === "succeeded") onSucceededRef.current();
		} catch (err) {
			setStartError(err instanceof Error ? err.message : "Could not start the install.");
		} finally {
			setIsStarting(false);
		}
	};

	const running = isStarting || isActiveInstallJob(job);
	const jobFor = (nextTarget: InstallTarget) => (target === nextTarget ? job : previews[nextTarget]);
	const inspectionFinished = (nextTarget: InstallTarget) => inspectedTargets[nextTarget] === true;
	return { target, startError, running, start, inspect, jobFor, inspectionFinished };
}

export function InstallDependencyDialog({
	requirements,
	onRefetchRequirements,
}: {
	requirements: SystemRequirement[];
	onRefetchRequirements: () => Promise<unknown> | void;
}) {
	const { t } = useTranslation();
	const [selectedAgent, setSelectedAgent] = useState<AgentInstallTarget | null>(null);
	const [ghDismissed, setGhDismissed] = useState(false);
	const [isCheckingAgain, setIsCheckingAgain] = useState(false);
	const install = useInstallRunner(() => void onRefetchRequirements());

	const byId = new Map(requirements.map((requirement) => [requirement.id, requirement]));
	const git = byId.get("git");
	const tmux = byId.get("tmux");
	const harness = byId.get("harness");
	const gh = byId.get("gh");
	const gitBlocking = Boolean(git && git.required && !git.satisfied);
	const tmuxBlocking = Boolean(tmux && tmux.required && !tmux.satisfied);
	const harnessBlocking = Boolean(harness && harness.required && !harness.satisfied);
	const ghAdvisory = Boolean(gh && !gh.satisfied);

	useEffect(() => {
		if (tmuxBlocking) void install.inspect("tmux");
	}, [install.inspect, tmuxBlocking]);

	useEffect(() => {
		if (ghAdvisory) void install.inspect("gh");
	}, [ghAdvisory, install.inspect]);

	useEffect(() => {
		if (selectedAgent) void install.inspect(selectedAgent);
	}, [install.inspect, selectedAgent]);

	const checkAgain = async () => {
		setIsCheckingAgain(true);
		try {
			await onRefetchRequirements();
		} finally {
			setIsCheckingAgain(false);
		}
	};

	const title = harnessBlocking ? t("startup.blockedTitleAgent") : t("startup.blockedTitleDependency");

	return (
		<Dialog open onOpenChange={() => {}}>
			<DialogContent
				showCloseButton={false}
				className={settingsDialogContentClass}
				onEscapeKeyDown={(event) => event.preventDefault()}
				onPointerDownOutside={(event) => event.preventDefault()}
			>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{title}</DialogTitle>
					<DialogDescription asChild>
						<div className="text-control leading-4 text-settings-muted">{t("startup.blockedBody")}</div>
					</DialogDescription>
				</div>

				<div className={cn(settingsDialogBodyClass, "gap-5")}>
					{gitBlocking && git ? (
						<IssueSection label={requirementDisplayLabel(git, t)} detail={requirementDetailText(git, t)}>
							<p className="text-caption leading-snug text-settings-muted">{t("startup.installGitInstructions")}</p>
						</IssueSection>
					) : null}

					{tmuxBlocking && tmux ? (
						<IssueSection label={requirementDisplayLabel(tmux, t)} detail={requirementDetailText(tmux, t)}>
							<InstallAction
								primaryLabel={t("startup.installTmux")}
								disabled={install.running && install.target !== "tmux"}
								job={install.jobFor("tmux")}
								planChecked={install.inspectionFinished("tmux")}
								error={install.target === "tmux" ? install.startError : undefined}
								onInstall={() => void install.start("tmux")}
								t={t}
							/>
						</IssueSection>
					) : null}

					{harnessBlocking && harness ? (
						<IssueSection label={requirementDisplayLabel(harness, t)} detail={requirementDetailText(harness, t)}>
							<RadioGroup.Root
								aria-label={t("startup.chooseAgentAriaLabel")}
								className="mt-2 flex flex-col gap-1.5"
								value={selectedAgent ?? ""}
								onValueChange={(value) => setSelectedAgent(value as AgentInstallTarget)}
							>
								{AGENT_INSTALL_OPTIONS.map((option) => (
									<RadioGroup.Item
										key={option.target}
										value={option.target}
										disabled={install.running}
										className="group flex items-start gap-2.5 rounded-md border border-border px-3 py-2 text-left transition-colors hover:bg-interactive-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-accent data-[state=checked]:bg-accent-weak"
									>
										<span
											aria-hidden="true"
											className="mt-0.5 grid size-icon-sm shrink-0 place-items-center rounded-full border border-border-strong group-data-[state=checked]:border-accent"
										>
											<RadioGroup.Indicator className="size-1.5 rounded-full bg-accent" />
										</span>
										<span className="min-w-0">
											<span className="block text-control font-medium text-settings-title">{option.label}</span>
											<span className="block text-caption text-settings-muted">
												{t(AGENT_INSTALL_DESCRIPTION_KEYS[option.target])}
											</span>
										</span>
									</RadioGroup.Item>
								))}
							</RadioGroup.Root>
							<div className="mt-2">
								<InstallAction
									primaryLabel={t("startup.installSelected")}
									disabled={
										!selectedAgent || (install.running && install.target !== selectedAgent)
									}
									job={selectedAgent ? install.jobFor(selectedAgent) : undefined}
									planChecked={selectedAgent ? install.inspectionFinished(selectedAgent) : true}
									error={selectedAgent && install.target === selectedAgent ? install.startError : undefined}
									onInstall={() => selectedAgent && void install.start(selectedAgent)}
									t={t}
								/>
							</div>
						</IssueSection>
					) : null}

					{ghAdvisory && gh && !ghDismissed ? (
						<div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2.5 text-xs leading-body-md">
							<p className="font-medium text-settings-title">{t("startup.recommendedInstallGh")}</p>
							<p className="mt-0.5 text-settings-muted">{requirementDetailText(gh, t)}</p>
							<div className="mt-2">
								<InstallAction
									primaryLabel={t("startup.installGh")}
									disabled={install.running && install.target !== "gh"}
									job={install.jobFor("gh")}
									planChecked={install.inspectionFinished("gh")}
									error={install.target === "gh" ? install.startError : undefined}
									onInstall={() => void install.start("gh")}
									t={t}
								/>
							</div>
							<button
								type="button"
								className="mt-2 text-caption text-settings-muted underline-offset-2 hover:underline"
								onClick={() => setGhDismissed(true)}
							>
								{t("startup.dismissGhSession")}
							</button>
						</div>
					) : null}
				</div>

				<div className={settingsDialogFooterClass}>
					<button
						type="button"
						className="settings-footer-button"
						onClick={() => void window.ao?.menu?.action("app.quit")}
					>
						{t("startup.quit")}
					</button>
					<button
						type="button"
						className="settings-footer-button settings-footer-button-primary"
						disabled={isCheckingAgain || install.running}
						onClick={() => void checkAgain()}
					>
						{isCheckingAgain ? t("startup.checkingAgain") : t("startup.checkAgain")}
					</button>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function IssueSection({
	label,
	detail,
	children,
}: {
	label: string;
	detail?: string;
	children?: React.ReactNode;
}) {
	return (
		<div className="flex flex-col gap-1">
			<div className="flex items-start gap-2">
				<XCircle className="mt-0.5 size-icon-sm shrink-0 text-destructive" aria-hidden="true" />
				<div className="min-w-0">
					<p className="text-control font-medium text-settings-title">{label}</p>
					{detail ? <p className="text-caption text-settings-muted">{detail}</p> : null}
				</div>
			</div>
			{children ? <div className="pl-[calc(var(--size-icon-sm)+0.625rem)]">{children}</div> : null}
		</div>
	);
}

function InstallAction({
	primaryLabel,
	disabled,
	job,
	planChecked,
	error,
	onInstall,
	t,
}: {
	primaryLabel: string;
	disabled: boolean;
	job: InstallJob | undefined;
	planChecked: boolean;
	error: string | undefined;
	onInstall: () => void;
	t: TFunction;
}) {
	const running = isActiveInstallJob(job);
	const failed = job?.status === "failed";
	const unsupported = job?.status === "unsupported";

	if (!planChecked) {
		return <p className="text-caption text-settings-muted">{t("startup.checkingInstallOptions")}</p>;
	}

	if (running) {
		return (
			<div className="flex flex-col gap-1.5">
				<p className="text-caption text-settings-muted">
					{job?.command ? t("startup.installingCommand", { command: job.command }) : t("startup.installingEllipsis")}
				</p>
				<div className="ao-install-progress" aria-hidden="true">
					<div className="ao-install-progress__bar" />
				</div>
			</div>
		);
	}

	if (unsupported) {
		return (
			<div className="flex flex-col gap-1.5">
				{job.error ? <p className="text-caption text-settings-muted">{job.error}</p> : null}
				{job.command ? <ManualCommand command={job.command} t={t} /> : null}
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-1.5">
			<button
				type="button"
				className="settings-footer-button settings-footer-button-primary self-start"
				disabled={disabled}
				onClick={onInstall}
			>
				{failed ? t("startup.retryPrefix", { label: primaryLabel }) : primaryLabel}
			</button>
			{error ? <p className="text-caption text-error">{error}</p> : null}
			{failed ? (
				<>
					{job?.error ? <p className="text-caption text-error">{job.error}</p> : null}
					{job?.output ? (
						<pre className="max-h-daemon-failure-details-max overflow-auto rounded-md border border-[var(--color-border-settings-dialog)] bg-[var(--color-bg-settings-input)] px-2 py-1.5 font-mono text-caption leading-relaxed text-settings-muted">
							{job.output}
						</pre>
					) : null}
				</>
			) : null}
		</div>
	);
}

function ManualCommand({ command, t }: { command: string; t: TFunction }) {
	const [copied, setCopied] = useState(false);
	const resetTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

	useEffect(() => () => clearTimeout(resetTimer.current), []);

	const copy = () => {
		void aoBridge.clipboard.writeText(command).then(
			() => {
				setCopied(true);
				clearTimeout(resetTimer.current);
				resetTimer.current = setTimeout(() => setCopied(false), 1_400);
			},
			() => undefined,
		);
	};

	return (
		<div className="flex items-center gap-2 rounded-md border border-[var(--color-border-settings-dialog)] bg-[var(--color-bg-settings-input)] px-2 py-1.5">
			<code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-caption text-settings-title">
				{command}
			</code>
			<button
				type="button"
				className="settings-footer-button shrink-0"
				aria-label={copied ? t("startup.commandCopied") : t("startup.copyCommand")}
				onClick={copy}
			>
				{copied ? <Check className="size-icon-sm" aria-hidden="true" /> : <Copy className="size-icon-sm" aria-hidden="true" />}
				{copied ? t("startup.commandCopied") : t("startup.copyCommand")}
			</button>
		</div>
	);
}
