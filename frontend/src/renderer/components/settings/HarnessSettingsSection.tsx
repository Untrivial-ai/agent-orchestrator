import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Download, LoaderCircle, LogIn, RefreshCw, Search, TriangleAlert, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import {
	agentReadinessQueryKey,
	cacheAgentReadiness,
	ensureAgentReadiness,
	useAgentReadinessQuery,
	useEnsureAgentReadiness,
} from "../../hooks/useAgentReadinessQuery";
import { agentAuthPlansQueryKey, probeAgentAuth, useAgentAuthPlans, useStartAgentAuth } from "../../hooks/useAgentAuth";
import { closeShellTerminal, shellTerminalsQueryKey } from "../../hooks/useShellTerminals";
import type { TerminalSessionState } from "../../hooks/useTerminalSession";
import { agentLabel, AGENT_OPTIONS, type AgentId } from "../../lib/agent-options";
import { apiClient, apiErrorCode, apiErrorMessage } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { useShellMaybe } from "../../lib/shell-context";
import { useResolvedTheme } from "../../stores/ui-store";
import { AgentAvatar } from "../AgentAvatar";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";
import { MENU_TRIGGER_CHROME } from "../ui/option-menu";
import { SettingsSection } from "./SettingsSection";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

type AgentInstallPlan = components["schemas"]["AgentInstallPlan"];
type InstallJob = components["schemas"]["InstallJob"];

const installerQueryKey = ["agent-installers"] as const;
const installJobsQueryKey = ["agent-install-jobs"] as const;
const POLL_INTERVAL_MS = 1_000;
const AUTH_TERMINAL_LIFETIME_MS = 15 * 60_000;

type AgentAuthState = { pending: boolean; checking: boolean; error: string | null };
type AgentAuthStates = Partial<Record<AgentId, AgentAuthState>>;
type AuthTerminalWorkflow = {
	agentId: AgentId;
	action: string;
	terminal: components["schemas"]["ShellTerminalResponse"];
	guidance: string;
	terminalInput?: string;
	phase: "running" | "verifying" | "unauthorized" | "unverified" | "closing" | "cleanup_failed" | "timed_out";
	reason?: string;
	startedAt: number;
};

async function closeAuthTerminal(handleId: string): Promise<void> {
	try {
		await closeShellTerminal(handleId);
	} catch (error) {
		if (apiErrorCode(error) !== "SHELL_TERMINAL_NOT_FOUND") throw error;
	}
}

async function fetchInstallers(): Promise<AgentInstallPlan[]> {
	const { data, error } = await apiClient.GET("/api/v1/agents/installers");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load harness installers."));
	return data.agents;
}

async function fetchInstallJobs(): Promise<InstallJob[]> {
	const { data, error } = await apiClient.GET("/api/v1/agents/install-jobs");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load harness installation jobs."));
	return data.jobs;
}

function upsertJob(current: InstallJob[] | undefined, next: InstallJob): InstallJob[] {
	return [...(current ?? []).filter((job) => job.target !== next.target), next];
}

function isActive(job: InstallJob | undefined): boolean {
	return job?.status === "installing" || job?.status === "verifying";
}

function diagnosticsText(agentId: AgentId, job: InstallJob): string {
	return [
		`${agentLabel(agentId)} installation diagnostics`,
		job.method ? `Method: ${job.method}` : "",
		job.expectedDestination ? `Expected destination: ${job.expectedDestination}` : "",
		job.error ? `Error: ${job.error}` : "",
		job.output ? `Output:\n${job.output}` : "",
	].filter(Boolean).join("\n");
}

function installMethodLabel(method: { id: string; label: string } | undefined, fallback?: string): string | undefined {
	if (!method) {
		if (fallback?.trim().toLocaleLowerCase() === "official-installer") return "Official";
		return fallback;
	}
	if (method.id === "official-installer" || method.label.trim().toLocaleLowerCase() === "official installer") return "Official";
	return method.label;
}

export function HarnessSettingsSection({ titleHidden = false }: { titleHidden?: boolean }) {
	const { i18n, t } = useTranslation();
	const queryClient = useQueryClient();
	const agents = useAgentReadinessQuery();
	useEnsureAgentReadiness();
	const installers = useQuery({ queryKey: installerQueryKey, queryFn: fetchInstallers, staleTime: 60_000 });
	const jobs = useQuery({ queryKey: installJobsQueryKey, queryFn: fetchInstallJobs, retry: false });
	const authPlans = useAgentAuthPlans();
	const startAgentAuth = useStartAgentAuth();
	const [search, setSearch] = useState("");
	const [authStates, setAuthStates] = useState<AgentAuthStates>({});
	const [refreshError, setRefreshError] = useState<string | null>(null);
	const [actionErrors, setActionErrors] = useState<Partial<Record<AgentId, string>>>({});
	const [selectedMethods, setSelectedMethods] = useState<Partial<Record<AgentId, string>>>({});
	const [expandedDiagnostics, setExpandedDiagnostics] = useState<Partial<Record<AgentId, boolean>>>({});
	const [copiedAgent, setCopiedAgent] = useState<AgentId | null>(null);
	const [authWorkflow, setAuthWorkflow] = useState<AuthTerminalWorkflow | null>(null);
	const authWorkflowRef = useRef<AuthTerminalWorkflow | null>(null);
	const authStartPendingRef = useRef(false);
	authWorkflowRef.current = authWorkflow;
	const refreshedSuccess = useRef(new Set<string>());
	const pendingActions = useRef(new Set<AgentId>());
	const [pendingAgentIds, setPendingAgentIds] = useState<Set<AgentId>>(new Set());

	const plans = useMemo(() => new Map(installers.data?.map((plan) => [plan.agentId, plan]) ?? []), [installers.data]);
	const jobMap = useMemo(() => new Map(jobs.data?.map((job) => [job.target, job]) ?? []), [jobs.data]);
	const agentAuthPlans = useMemo(() => new Map(authPlans.data?.map((plan) => [plan.agentId, plan]) ?? []), [authPlans.data]);
	const readinessAgents = useMemo(() => new Map(agents.data?.agents.map((agent) => [agent.id, agent]) ?? []), [agents.data]);
	const installed = useMemo(
		() => new Set<AgentId>(agents.data?.agents.filter((agent) => agent.installation.state === "installed").map((agent) => agent.id as AgentId) ?? []),
		[agents.data],
	);
	const normalizedSearch = search.trim().toLowerCase();
	const rows = AGENT_OPTIONS.filter((agentId) => agentId === authWorkflow?.agentId || agentLabel(agentId).toLowerCase().includes(normalizedSearch));
	const updateAuthState = useCallback((agentId: AgentId, patch: Partial<AgentAuthState>) => {
		setAuthStates((current) => ({
			...current,
			[agentId]: { pending: false, checking: false, error: null, ...current[agentId], ...patch },
		}));
	}, []);
	const activeKey = useMemo(
		() => (jobs.data ?? []).filter((job) => isActive(job)).map((job) => job.target).sort().join(","),
		[jobs.data],
	);
	const succeededKey = useMemo(
		() => (jobs.data ?? []).filter((job) => job.status === "succeeded").map((job) => `${job.target}:${job.updatedAt ?? job.finishedAt ?? "done"}`).sort().join(","),
		[jobs.data],
	);

	useEffect(() => {
		if (!activeKey) return;
		const timer = window.setInterval(() => void jobs.refetch(), POLL_INTERVAL_MS);
		return () => window.clearInterval(timer);
	}, [activeKey, jobs.refetch]);

	useEffect(() => {
		for (const token of succeededKey ? succeededKey.split(",") : []) {
			if (!token || refreshedSuccess.current.has(token)) continue;
			refreshedSuccess.current.add(token);
			const agentId = token.split(":", 1)[0] as AgentId;
			setActionErrors((current) => ({ ...current, [agentId]: undefined }));
			void apiClient.POST("/api/v1/agents/{agent}/probe", {
				params: { path: { agent: agentId } },
			}).finally(async () => {
				try {
					const readiness = await ensureAgentReadiness([agentId], "display");
					cacheAgentReadiness(queryClient, readiness);
				} catch {
					await queryClient.invalidateQueries({ queryKey: agentReadinessQueryKey });
				} finally {
					await Promise.all([
						queryClient.invalidateQueries({ queryKey: installerQueryKey }),
						queryClient.invalidateQueries({ queryKey: agentAuthPlansQueryKey }),
					]);
				}
			});
		}
	}, [queryClient, succeededKey]);

	useEffect(() => {
		setExpandedDiagnostics((current) => {
			let changed = false;
			const next = { ...current };
			for (const agentId of installed) {
				if (next[agentId]) {
					delete next[agentId];
					changed = true;
				}
			}
			return changed ? next : current;
		});
	}, [installed]);

	const updateJob = (job: InstallJob) => {
		setActionErrors((current) => ({ ...current, [job.target as AgentId]: undefined }));
		queryClient.setQueryData<InstallJob[]>(installJobsQueryKey, (current) => upsertJob(current, job));
	};

	const beginAction = (agentId: AgentId): boolean => {
		if (pendingActions.current.has(agentId)) return false;
		pendingActions.current.add(agentId);
		setPendingAgentIds(new Set(pendingActions.current));
		return true;
	};

	const endAction = (agentId: AgentId) => {
		pendingActions.current.delete(agentId);
		setPendingAgentIds(new Set(pendingActions.current));
	};

	const startInstall = async (agentId: AgentId, method: string) => {
		if (!beginAction(agentId)) return;
		setActionErrors((current) => ({ ...current, [agentId]: undefined }));
		try {
			const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/install", {
				params: { path: { agent: agentId } },
				body: { method, operation: "install" },
			});
			if (error || !data) {
				setActionErrors((current) => ({ ...current, [agentId]: apiErrorMessage(error, t("settings.harness.startFailed")) }));
				return;
			}
			updateJob(data);
		} finally {
			endAction(agentId);
		}
	};

	const verifyInstall = async (agentId: AgentId) => {
		if (!beginAction(agentId)) return;
		setActionErrors((current) => ({ ...current, [agentId]: undefined }));
		try {
			const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/verify", {
				params: { path: { agent: agentId } },
			});
			if (error || !data) {
				setActionErrors((current) => ({ ...current, [agentId]: apiErrorMessage(error, t("settings.harness.verifyFailed", { agent: agentLabel(agentId) })) }));
				return;
			}
			updateJob(data);
		} finally {
			endAction(agentId);
		}
	};

	const copyText = async (agentId: AgentId, text: string) => {
		await aoBridge.clipboard.writeText(text);
		setCopiedAgent(agentId);
		window.setTimeout(() => setCopiedAgent((current) => (current === agentId ? null : current)), 1_500);
	};

	const startAuth = async (agentId: AgentId) => {
		if (authWorkflowRef.current || authStartPendingRef.current) return;
		authStartPendingRef.current = true;
		updateAuthState(agentId, { pending: true, error: null });
		try {
			const plan = agentAuthPlans.get(agentId);
			if (plan?.launchMode === "documentation") {
				await aoBridge.app.openExternal(plan.documentationUrl);
				return;
			}
			const result = await startAgentAuth.mutateAsync(agentId);
			const workflow: AuthTerminalWorkflow = {
				agentId,
				action: result.action,
				terminal: result.terminal,
				guidance: result.guidance ?? "",
				terminalInput: result.terminalInput,
				phase: "running",
				startedAt: Date.now(),
			};
			authWorkflowRef.current = workflow;
			setAuthWorkflow(workflow);
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		} catch (error) {
			updateAuthState(agentId, { error: error instanceof Error ? error.message : t("settings.harness.authFailed") });
		} finally {
			authStartPendingRef.current = false;
			updateAuthState(agentId, { pending: false });
		}
	};

	const checkAuth = useCallback(async (agentId: AgentId) => {
		updateAuthState(agentId, { checking: true, error: null });
		try {
			const result = await probeAgentAuth(agentId);
			const readiness = await ensureAgentReadiness([agentId], "display");
			cacheAgentReadiness(queryClient, readiness);
			return result;
		} catch (error) {
			updateAuthState(agentId, { error: error instanceof Error ? error.message : t("settings.harness.authFailed") });
			return undefined;
		} finally {
			updateAuthState(agentId, { checking: false });
		}
	}, [queryClient, t, updateAuthState]);

	const finishAuth = useCallback(async (workflow: AuthTerminalWorkflow) => {
		if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return;
		setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "verifying", reason: undefined } : current);
		const result = await checkAuth(workflow.agentId);
		if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return;
		if (result?.agent.authStatus === "authorized") {
			try {
				await closeAuthTerminal(workflow.terminal.handleId);
			} catch (error) {
				setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "cleanup_failed", reason: error instanceof Error ? error.message : t("settings.harness.authFailed") } : current);
				return;
			}
			authWorkflowRef.current = null;
			setAuthWorkflow(null);
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
			return;
		}
		setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? {
			...current,
			phase: result?.agent.authStatus === "unauthorized" ? "unauthorized" : "unverified",
			reason: result?.agent.authStatus === "unauthorized" ? t("settings.harness.notLoggedIn") : t("settings.harness.loginUnknown"),
		} : current);
	}, [checkAuth, queryClient, t]);

	const closeAuth = useCallback(async (workflow: AuthTerminalWorkflow): Promise<boolean> => {
		if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return false;
		setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "closing", reason: undefined } : current);
		try {
			await closeAuthTerminal(workflow.terminal.handleId);
			authWorkflowRef.current = null;
			setAuthWorkflow(null);
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
			await checkAuth(workflow.agentId);
			return true;
		} catch (error) {
			setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "cleanup_failed", reason: error instanceof Error ? error.message : t("settings.harness.authFailed") } : current);
			return false;
		}
	}, [checkAuth, queryClient, t]);

	useEffect(() => {
		if (!authWorkflow || authWorkflow.phase !== "running") return;
		const handleId = authWorkflow.terminal.handleId;
		const remaining = Math.max(0, AUTH_TERMINAL_LIFETIME_MS - (Date.now() - authWorkflow.startedAt));
		const timeout = window.setTimeout(async () => {
			if (authWorkflowRef.current?.terminal.handleId !== handleId) return;
			setAuthWorkflow((current) => current?.terminal.handleId === handleId ? { ...current, phase: "closing", reason: undefined } : current);
			try {
				await closeAuthTerminal(handleId);
				setAuthWorkflow((current) => current?.terminal.handleId === handleId ? { ...current, phase: "timed_out", reason: t("settings.harness.authTimedOut") } : current);
				void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
			} catch (error) {
				setAuthWorkflow((current) => current?.terminal.handleId === handleId ? { ...current, phase: "cleanup_failed", reason: error instanceof Error ? error.message : t("settings.harness.authFailed") } : current);
			}
		}, remaining);
		return () => window.clearTimeout(timeout);
	}, [authWorkflow, queryClient, t]);

	useEffect(() => () => {
		const workflow = authWorkflowRef.current;
		if (workflow) void closeAuthTerminal(workflow.terminal.handleId).catch(() => undefined);
	}, []);

	const refresh = async () => {
		setRefreshError(null);
		try {
			const [{ error }] = await Promise.all([
								apiClient.POST("/api/v1/agents/refresh"),
								queryClient.invalidateQueries({ queryKey: installerQueryKey }),
								queryClient.invalidateQueries({ queryKey: installJobsQueryKey }),
								queryClient.invalidateQueries({ queryKey: agentAuthPlansQueryKey }),
			]);
			if (error) throw new Error(apiErrorMessage(error));
			await queryClient.invalidateQueries({ queryKey: agentReadinessQueryKey });
		} catch (error) {
			setRefreshError(error instanceof Error ? error.message : t("settings.harness.loadFailed"));
		}
	};

	return (
		<SettingsSection title={t("settings.harness")} titleHidden={titleHidden} sectionId="harness">
			<div className="sticky top-0 z-10 flex items-center gap-2 bg-card pb-2">
				<label className="flex h-9! min-w-0 flex-1 items-center gap-2 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3">
					<Search aria-hidden="true" className="size-4 shrink-0 text-settings-muted" />
					<span className="sr-only">{t("settings.harness.search")}</span>
					<input aria-label={t("settings.harness.search")} className="min-w-0 flex-1 bg-transparent text-sm text-settings-label outline-none placeholder:text-settings-muted" placeholder={t("settings.harness.searchPlaceholder")} value={search} onChange={(event) => setSearch(event.target.value)} />
				</label>
				<Button
					aria-label={t("settings.harness.refresh")}
					className="h-9! w-9! min-h-9! min-w-9! shrink-0 aspect-square p-0"
					size="icon-sm"
					variant="outline"
					onClick={() => void refresh()}
				>
					<RefreshCw className={cn((agents.isFetching || installers.isFetching || jobs.isFetching) && "animate-spin")} />
				</Button>
			</div>

			{installers.error || authPlans.error || agents.error || jobs.error || refreshError ? (
				<div className="flex items-center gap-2 rounded-md border border-error/30 bg-error/10 px-3 py-2 text-xs text-error">
					<TriangleAlert className="size-4" aria-hidden="true" />
					{refreshError ?? (jobs.error instanceof Error ? jobs.error.message : t("settings.harness.loadFailed"))}
				</div>
			) : null}

			<div className="settings-grouped-rows flex w-full flex-col">
			{rows.map((agentId) => {
					const plan = plans.get(agentId);
					const job = jobMap.get(agentId);
					const isInstalled = installed.has(agentId);
					const availableMethods = plan?.methods.filter((method) => method.available) ?? [];
					const recommendedMethod = availableMethods.find((method) => method.recommended) ?? availableMethods[0];
					const selectedMethodId = selectedMethods[agentId] ?? (availableMethods.some((method) => method.id === job?.method) ? job?.method : recommendedMethod?.id) ?? "";
					const selectedMethod = availableMethods.find((method) => method.id === selectedMethodId);
					const actionError = actionErrors[agentId];
					const failed = job?.status === "failed" || job?.status === "unsupported" || job?.status === "interrupted" || Boolean(actionError);
					const active = isActive(job);
					const pending = pendingAgentIds.has(agentId);
						const readinessAgent = readinessAgents.get(agentId);
						const authPlan = agentAuthPlans.get(agentId);
						const isSetupAction = authPlan?.action === "setup";
						const authState = authStates[agentId];
						const authStatus = readinessAgent?.authentication.state;
						const rowHasError = failed || Boolean(authState?.error);
						const rowAuthWorkflow = authWorkflow?.agentId === agentId ? authWorkflow : null;
						const hasDiagnostics = Boolean(
							job &&
							(job.status === "failed" || job.status === "unsupported" || job.status === "interrupted") &&
							(job.error || job.output || job.method || job.expectedDestination),
						);

						const authSummary = authState?.error
							? authState.error
							: authStatus === "authorized"
								? (isSetupAction ? t("settings.harness.configured") : t("settings.harness.loggedIn"))
								: authPlan && !authPlan.available
									? (authPlan.reason ?? t("settings.harness.authFailed"))
									: authStatus === "unauthorized"
										? (isSetupAction ? t("settings.harness.notConfigured") : t("settings.harness.notLoggedIn"))
										: isSetupAction ? t("settings.harness.configurationUnknown") : t("settings.harness.loginUnknown");
					const methodLabel = installMethodLabel(selectedMethod, plan?.method);
					const availableMethodsLabel = availableMethods.length > 0
						? new Intl.ListFormat(i18n.resolvedLanguage ?? "en", { style: "short", type: "conjunction" }).format(availableMethods.map((method) => installMethodLabel(method) ?? method.label))
						: methodLabel;
						const methodSelect = availableMethods.length > 1 ? (
										<SettingsOptionMenu
											aria-label={t("settings.harness.installMethod")}
											value={selectedMethodId}
											options={availableMethods.map((method) => ({ value: method.id, label: installMethodLabel(method) ?? method.label }))}
											triggerClassName="h-8! min-h-8! w-8! min-w-8! justify-center! rounded-none! border-l border-settings-menu bg-transparent px-0! text-xs leading-4 hover:bg-[var(--color-bg-settings-trigger-hover)]!"
							renderTrigger={(selected) => <span className="sr-only">{selected?.label}</span>}
							onChange={(value) => setSelectedMethods((current) => ({ ...current, [agentId]: value }))}
						/>
						) : null;
						const authControls = !authPlan && authPlans.isPending ? (
							<LoaderCircle className="size-4 animate-spin text-settings-muted" aria-hidden="true" />
						) : authPlan && authPlan.action !== "instructions" ? (
							<>
								{authStatus === "authorized" ? (
									<span className="inline-flex items-center gap-1 text-xs font-medium text-success"><Check className="size-4" aria-hidden="true" />{isSetupAction ? t("settings.harness.configured") : t("settings.harness.loggedIn")}</span>
								) : (
									<Button disabled={!authPlan.available || authState?.pending || Boolean(authWorkflow)} size="sm" onClick={() => void startAuth(agentId)}>
										{authState?.pending ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
										{authState?.pending ? t("settings.harness.loggingIn") : isSetupAction ? t("settings.harness.setup") : t("settings.harness.login")}
									</Button>
								)}
								{authPlan.available && (authStatus === "unknown" || authStatus === "unauthorized") ? (
									<Button disabled={authState?.checking} size="sm" variant="outline" onClick={() => void checkAuth(agentId)}>
										{authState?.checking ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : <RefreshCw aria-hidden="true" />}
										{authState?.checking ? t("settings.harness.checkingLogin") : isSetupAction ? t("settings.harness.checkConfiguration") : t("settings.harness.checkLogin")}
									</Button>
								) : null}
							</>
						) : null;
					return (
						<div className="settings-row-bar min-h-14 flex-wrap gap-3" data-agent={agentId} key={agentId}>
							<AgentAvatar className="size-7 shrink-0" decorative provider={agentId} />
							<div className="min-w-0 flex-1">
								<p className="truncate text-sm font-medium text-settings-label" id={`harness-agent-${agentId}`}>{agentLabel(agentId)}</p>
								<p className={cn("truncate text-xs text-settings-muted", rowHasError && "text-error")} title={authState?.error ?? actionError ?? job?.error ?? authPlan?.reason ?? plan?.reason}>
										{isInstalled ? authSummary : actionError ?? (job?.status === "interrupted" ? t("settings.harness.interrupted") : failed ? (job?.error ?? t("settings.harness.installFailed")) : plan?.available ? t("settings.harness.availableWith", { method: availableMethodsLabel }) : (plan?.reason ?? t("settings.harness.manualRequired")))}
								</p>
							</div>

			{active ? (
				<span className="inline-flex items-center gap-1.5 text-xs text-settings-muted" role="status"><LoaderCircle className="size-4 animate-spin" aria-hidden="true" />{job?.status === "installing" ? t("settings.harness.installing") : t("settings.harness.verifying")}</span>
							) : isInstalled ? (
								<div className="flex shrink-0 items-center gap-2">
								<Button
					type="button"
					size="none"
					variant="ghost"
					className={cn(MENU_TRIGGER_CHROME, "h-8! min-h-8! shrink-0 rounded-md! border-0! bg-[var(--color-bg-settings-trigger)] px-3! text-xs leading-4")}
					aria-label={t("settings.harness.installed")}
					disabled
								>
									{t("settings.harness.installed")}
								</Button>
								{authControls}
								</div>
							) : failed ? (
								<div className="flex items-center gap-1.5">
									{methodSelect}
									<Button size="sm" variant="outline" disabled={pending} onClick={() => void verifyInstall(agentId)}>{t("settings.harness.verifyAgain")}</Button>
									{selectedMethodId ? <Button className={MENU_TRIGGER_CHROME} size="sm" variant="ghost" onClick={() => void startInstall(agentId, selectedMethodId)} disabled={pending}>{t("settings.harness.retry")}</Button> : null}
								</div>
							) : !plan && installers.isPending ? (
								<span className="inline-flex items-center gap-1.5 text-xs text-settings-muted" role="status"><LoaderCircle className="size-4 animate-spin" aria-hidden="true" /></span>
							) : availableMethods.length > 0 ? (
				<div className="flex items-stretch overflow-hidden rounded-md bg-[var(--color-bg-settings-trigger)]">
									<Button
										className={cn(
											MENU_TRIGGER_CHROME,
											"h-8! min-h-8! rounded-none! border-0! bg-transparent px-3! text-xs leading-4 hover:bg-[var(--color-bg-settings-trigger-hover)]! dark:hover:bg-[var(--color-bg-settings-trigger-hover)]!",
											availableMethods.length > 1 && "pr-3",
										)}
										size="none"
										variant="ghost"
										aria-label={t("settings.harness.install")}
										disabled={pending}
										onClick={() => selectedMethodId && void startInstall(agentId, selectedMethodId)}
									>
										<Download aria-hidden="true" />{methodLabel}
									</Button>
									{methodSelect}
								</div>
							) : plan?.command ? (
								<Button size="sm" variant="outline" onClick={() => void copyText(agentId, plan.command!)}>{copiedAgent === agentId ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}{copiedAgent === agentId ? t("settings.harness.copied") : t("settings.harness.copyCommand")}</Button>
							) : null}

				{!isInstalled && hasDiagnostics ? (
				<div className="basis-full">
					<div className={cn("grid transition-[grid-template-rows] duration-200 ease-out", expandedDiagnostics[agentId] ? "grid-rows-[1fr]" : "grid-rows-[0fr]")}>
						<div className="min-h-0 overflow-hidden">
							<div className="mt-1 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) p-3 text-xs text-settings-muted">
								{job?.method ? <p><span className="font-medium text-settings-label">{t("settings.harness.method")}:</span> {job.method}</p> : null}
								{job?.expectedDestination ? <p className="break-all"><span className="font-medium text-settings-label">{t("settings.harness.expectedDestination")}:</span> {job.expectedDestination}</p> : null}
								{job?.error ? <p className="mt-2 whitespace-pre-wrap text-error">{job.error}</p> : null}
								{job?.output ? <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-words font-mono">{job.output}</pre> : null}
								<Button className="mt-2" size="sm" variant="outline" onClick={() => job && void copyText(agentId, diagnosticsText(agentId, job))}><Copy aria-hidden="true" />{t("settings.harness.copyDiagnostics")}</Button>
							</div>
						</div>
					</div>
					<Button aria-expanded={expandedDiagnostics[agentId] === true} size="sm" variant="ghost" onClick={() => setExpandedDiagnostics((current) => ({ ...current, [agentId]: !current[agentId] }))}>
						{expandedDiagnostics[agentId] ? t("settings.harness.hideDiagnostics") : t("settings.harness.showDiagnostics")}
					</Button>
					</div>
										) : null}
										{rowAuthWorkflow ? (
											<div className="basis-full pl-10">
												<HarnessAuthTerminalPanel
													workflow={rowAuthWorkflow}
													onClose={() => void closeAuth(rowAuthWorkflow)}
								onRetry={() => void closeAuth(rowAuthWorkflow).then((closed) => { if (closed) void startAuth(agentId); })}
													onTerminalState={(state) => {
														if (state === "exited" && authWorkflowRef.current?.phase === "running") void finishAuth(rowAuthWorkflow);
													}}
												/>
											</div>
										) : null}
						</div>
					);
				})}
				{rows.length === 0 ? <p className="px-3 py-6 text-center text-sm text-settings-muted">{t("settings.harness.noResults")}</p> : null}
			</div>
		</SettingsSection>
	);
}

function HarnessAuthTerminalPanel({ workflow, onClose, onRetry, onTerminalState }: {
	workflow: AuthTerminalWorkflow;
	onClose: () => void;
	onRetry: () => void;
	onTerminalState: (state: TerminalSessionState) => void;
}) {
	const { t } = useTranslation();
	const theme = useResolvedTheme();
	const shell = useShellMaybe();
	const panelRef = useRef<HTMLDivElement>(null);
	const inputRequestIdRef = useRef(0);
	const activeInputRequestIdRef = useRef<number | null>(null);
	const [terminalState, setTerminalState] = useState<TerminalSessionState>("connecting");
	const [inputRequest, setInputRequest] = useState<{ id: number; data: string }>();
	const [commandPending, setCommandPending] = useState(false);
	const [commandSent, setCommandSent] = useState(false);
	const handlerRef = useRef(onTerminalState);
	handlerRef.current = onTerminalState;
	const handleTerminalState = useCallback((state: TerminalSessionState) => {
		setTerminalState(state);
		handlerRef.current(state);
	}, []);
	useEffect(() => {
		panelRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
	}, [workflow.terminal.handleId]);
	const status = workflow.phase === "running"
		? workflow.guidance || t("settings.harness.loggingIn")
		: workflow.phase === "verifying" ? t("settings.harness.checkingLogin")
			: workflow.phase === "closing" ? t("settings.harness.authClosing")
				: workflow.reason ?? t("settings.harness.loginUnknown");
	const retryable = workflow.phase === "unauthorized" || workflow.phase === "unverified" || workflow.phase === "timed_out" || workflow.phase === "cleanup_failed";
	const openAuthAction = () => {
		if (!workflow.terminalInput || terminalState !== "attached" || commandPending || commandSent) return;
		inputRequestIdRef.current += 1;
		activeInputRequestIdRef.current = inputRequestIdRef.current;
		setCommandPending(true);
		setInputRequest({ id: inputRequestIdRef.current, data: workflow.terminalInput });
	};
	const handleInputRequestResult = useCallback((id: number, accepted: boolean) => {
		if (activeInputRequestIdRef.current !== id) return;
		activeInputRequestIdRef.current = null;
		setInputRequest(undefined);
		setCommandPending(false);
		if (accepted) setCommandSent(true);
	}, []);
	return (
		<div ref={panelRef} className="mt-1 scroll-my-3 overflow-hidden rounded-md border border-(--color-border-settings-input) bg-terminal" data-testid="harness-auth-terminal">
			<div className="flex min-h-10 items-center justify-between gap-3 border-b border-(--color-border-settings-input) bg-surface/90 px-3 py-2">
				<div className="min-w-0"><p className="truncate text-xs font-medium text-settings-label">{workflow.terminal.title}</p><p className="truncate text-[11px] text-settings-muted" aria-live="polite" role="status">{status}</p></div>
				<div className="flex shrink-0 items-center gap-2">
					{workflow.terminalInput && workflow.phase === "running" ? <Button type="button" size="sm" variant="outline" disabled={terminalState !== "attached" || commandPending || commandSent} onClick={openAuthAction}>{commandSent ? <Check aria-hidden="true" /> : <LogIn aria-hidden="true" />}{workflow.action === "setup" ? commandSent ? t("settings.harness.setupOpened") : t("settings.harness.openSetup") : commandSent ? t("settings.harness.loginOpened") : t("settings.harness.openLogin")}</Button> : null}
					<button type="button" aria-label={t("settings.close")} className="grid size-7 place-items-center rounded text-settings-muted hover:bg-interactive-hover" disabled={workflow.phase === "closing" || workflow.phase === "verifying"} onClick={onClose}><X className="size-4" aria-hidden="true" /></button>
				</div>
			</div>
			<div className="h-[300px] min-h-0"><TerminalPane daemonReady={shell ? shell.daemonStatus.state === "ready" : true} fontSize={12} inputRequest={inputRequest} onInputRequestResult={handleInputRequestResult} onTerminalStateChange={handleTerminalState} terminalTarget={{ kind: "shell", handleId: workflow.terminal.handleId, generation: workflow.terminal.createdAt, title: workflow.terminal.title }} theme={theme} /></div>
			{retryable ? <div className="flex items-center justify-end border-t border-(--color-border-settings-input) bg-surface/90 px-3 py-2"><Button type="button" size="sm" variant="outline" onClick={workflow.phase === "cleanup_failed" ? onClose : onRetry}>{workflow.phase === "cleanup_failed" ? t("settings.harness.retry") : workflow.action === "setup" ? t("settings.harness.setup") : t("settings.harness.login")}</Button></div> : null}
		</div>
	);
}
