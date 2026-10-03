import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen, Check, Copy, Download, KeyRound, LoaderCircle, LogIn, Search, TriangleAlert, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import {
	agentReadinessQueryKeyForHost,
	cacheAgentReadiness,
	ensureAgentReadiness,
	useAgentReadinessQuery,
} from "../../hooks/useAgentReadinessQuery";
import { agentAuthPlansQueryKeyForHost, probeAgentAuth, useAgentAuthPlans, useStartAgentAuth } from "../../hooks/useAgentAuth";
import { agentModelsQueryPrefix } from "../../hooks/useAgentModelsQuery";
import { closeShellTerminal, shellTerminalsQueryKeyForHost, type ShellTerminal } from "../../hooks/useShellTerminals";
import type { TerminalSessionState } from "../../hooks/useTerminalSession";
import { agentLabel, AGENT_OPTIONS, type AgentId } from "../../lib/agent-options";
import { CLOUD_AGENT_PROVIDERS, isCloudHarnessConnected } from "../../lib/cloud-agents";
import { useCloudCp } from "../../hooks/useCloudCp";
import { useCloudOrg } from "../../hooks/useCloudOrg";
import { providerConnectionsQueryKey, useProviderConnections } from "../../hooks/useProviderConnections";
import { GitHubTokenField } from "../onboarding/GitHubTokenField";
import { CloudHarnessLoginPanel, type CloudHarness } from "./CloudHarnessLoginPanel";
import { SettingsRow } from "./SettingsRow";
import { apiErrorCode, apiErrorMessage } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { baseUrlForHost, clientForSessionHost, labelForHost } from "../../lib/host-clients";
import { useConnectedHosts } from "../../hooks/useHostConnection";
import { LOCAL_HOST } from "../../lib/hosts";
import { createTerminalMux, muxUrlFromApiBase } from "../../lib/terminal-mux";
import { cn } from "../../lib/utils";
import { useShellMaybe } from "../../lib/shell-context";
import { useResolvedTheme } from "../../stores/ui-store";
import { AgentAvatar } from "../AgentAvatar";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";
import { Tabs, TabsList, TabsTrigger } from "../ui/tabs";
import { useCloudGate } from "../../hooks/useCloudGate";
import { MENU_TRIGGER_CHROME } from "../ui/option-menu";
import { SettingsSection } from "./SettingsSection";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

type AgentInstallPlan = components["schemas"]["AgentInstallPlan"];
type InstallJob = components["schemas"]["InstallJob"];

const installerQueryKey = ["agent-installers"] as const;
const installJobsQueryKey = ["agent-install-jobs"] as const;
const POLL_INTERVAL_MS = 1_000;
const AUTH_TERMINAL_LIFETIME_MS = 15 * 60_000;
// The first check right after a login terminal exits can fail transiently (the
// daemon's own readiness retry succeeds seconds later), so a login is re-checked
// a few times before the panel reports it did not take.
const AUTH_VERIFY_ATTEMPTS = 4;
const AUTH_VERIFY_RETRY_MS = 1_500;
const FOCUS_HIGHLIGHT_MS = 2_000;

type AgentAuthState = { pending: boolean; error: string | null };
type AgentAuthStates = Partial<Record<AgentId, AgentAuthState>>;
type AgentAuthProbeResult = Awaited<ReturnType<typeof probeAgentAuth>>;
const AUTH_STATE_RANK = {
	authorized: 0,
	not_applicable: 0,
	unauthorized: 1,
	configured: 2,
	unknown: 2,
} as const;
const INSTALL_STATE_RANK = {
	installed: 0,
	unknown: 1,
	not_installed: 2,
} as const;
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

async function closeAuthTerminal(handleId: string, hostId?: string): Promise<void> {
	try {
		await closeShellTerminal(handleId, hostId);
	} catch (error) {
		if (apiErrorCode(error) !== "SHELL_TERMINAL_NOT_FOUND") throw error;
	}
}

async function fetchInstallers(hostId?: string): Promise<AgentInstallPlan[]> {
	const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/agents/installers");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load harness installers."));
	return data.agents;
}

async function fetchInstallJobs(hostId?: string): Promise<InstallJob[]> {
	const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/agents/install-jobs");
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

export type HarnessView = "local" | "cloud";

export function HarnessSettingsSection({
	focusAgentId,
	hostId,
	initialView = "local",
	titleHidden = false,
}: {
	focusAgentId?: string;
	hostId?: string;
	initialView?: HarnessView;
	titleHidden?: boolean;
}) {
	const { t } = useTranslation();
	const { cloudEnabled } = useCloudGate();
	const [view, setView] = useState<HarnessView>(initialView);
	const [search, setSearch] = useState("");
	useEffect(() => setView(initialView), [initialView]);
	const cloudView = cloudEnabled && view === "cloud";
	const connected = useConnectedHosts();
	const [selectedHostId, setSelectedHostId] = useState(hostId ?? LOCAL_HOST);
	useEffect(() => setSelectedHostId(hostId ?? LOCAL_HOST), [hostId]);
	const remoteOffline = selectedHostId !== LOCAL_HOST && !connected.includes(selectedHostId);
	return <SettingsSection title={t("settings.harness")} titleHidden={titleHidden} sectionId="harness">
		<div className="sticky top-0 z-10 flex items-center gap-2 bg-card pb-2">
			<label className="flex h-9! min-w-0 flex-1 items-center gap-2 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3">
				<Search aria-hidden="true" className="size-4 shrink-0 text-settings-muted" />
				<span className="sr-only">{t("settings.harness.search")}</span>
				<input aria-label={t("settings.harness.search")} className="min-w-0 flex-1 bg-transparent text-sm text-settings-label outline-none placeholder:text-settings-muted" placeholder={t("settings.harness.searchPlaceholder")} value={search} onChange={(event) => setSearch(event.target.value)} />
			</label>
			{cloudEnabled ? <Tabs value={cloudView ? "cloud" : "local"} onValueChange={(value) => setView(value as HarnessView)}><TabsList aria-label={t("settings.harness.viewLabel")}><TabsTrigger value="local">{t("settings.harness.viewLocal")}</TabsTrigger><TabsTrigger value="cloud">{t("settings.harness.viewCloud")}</TabsTrigger></TabsList></Tabs> : null}
		</div>
		{!cloudView && (connected.length > 0 || remoteOffline) ? <SettingsOptionMenu
				aria-label={t("remote.host")}
				value={selectedHostId}
				options={[{ value: LOCAL_HOST, label: t("settings.harness.thisComputer") }, ...connected.map((id) => ({ value: id, label: labelForHost(id) ?? id })), ...(remoteOffline ? [{ value: selectedHostId, label: t("remote.hostLabel", { hostId: selectedHostId }) }] : [])]}
				onChange={setSelectedHostId}
				triggerClassName="w-fit max-w-full"
			/> : null}
		{!cloudView && selectedHostId !== LOCAL_HOST && !remoteOffline ? <p className="text-xs text-muted-foreground">{t("settings.harness.remoteBrowserAuthNote")}</p> : null}
		{cloudView ? <CloudHarnessContent focusAgentId={focusAgentId} search={search} /> : remoteOffline ? <p className="text-xs text-error" role="alert">{t("remote.hostOffline")}</p> : <LocalHarnessContent key={selectedHostId} focusAgentId={focusAgentId} hostId={selectedHostId === LOCAL_HOST ? undefined : selectedHostId} search={search} />}
	</SettingsSection>;
}

function CloudHarnessContent({ focusAgentId, search }: { focusAgentId?: string; search: string }) {
	const { t } = useTranslation();
	const { org } = useCloudOrg();
	const connections = useProviderConnections();
	const [loginAgent, setLoginAgent] = useState<CloudHarness | null>(null);
	const [highlightedAgentId, setHighlightedAgentId] = useState<AgentId | null>(null);
	const rowsRef = useRef<HTMLDivElement>(null);
	const focusHandledRef = useRef(false);
	const targetAgentId = CLOUD_AGENT_PROVIDERS.find((agentId) => agentId === focusAgentId);
	const rows = CLOUD_AGENT_PROVIDERS.filter((agentId) =>
		agentId === targetAgentId || agentLabel(agentId).toLowerCase().includes(search.trim().toLowerCase()),
	);
	useEffect(() => {
		if (focusHandledRef.current || !targetAgentId || !org?.id || connections.isPending) return;
		const row = rowsRef.current?.querySelector<HTMLElement>(`[data-agent="${targetAgentId}"]`);
		if (!row) return;
		focusHandledRef.current = true;
		row.scrollIntoView({ behavior: "smooth", block: "center" });
		(row.querySelector<HTMLElement>("[data-harness-primary-action]:not(:disabled)") ?? row).focus({ preventScroll: true });
		setHighlightedAgentId(targetAgentId);
		const timer = window.setTimeout(() => setHighlightedAgentId(null), FOCUS_HIGHLIGHT_MS);
		return () => window.clearTimeout(timer);
	}, [connections.isPending, org?.id, targetAgentId]);

	return <>
		{!org?.id ? <p className="px-3 py-6 text-center text-sm text-settings-muted">{t("settings.cloudAgents.signIn")}</p>
			: connections.error ? <p className="px-3 py-6 text-sm text-error" role="alert">{String(connections.error)}</p>
			: connections.isPending ? null
			: <div className="settings-grouped-rows flex w-full flex-col" ref={rowsRef}>
				{rows.map((agentId) => {
					const connected = isCloudHarnessConnected(connections.data, agentId);
					return <div
						aria-labelledby={`harness-agent-${agentId}`}
						className={cn("settings-row-bar min-h-14 flex-wrap gap-3 transition-[background-color,box-shadow] duration-200", highlightedAgentId === agentId && "bg-accent-weak ring-2 ring-inset ring-accent")}
						data-agent={agentId}
						data-focus-highlighted={highlightedAgentId === agentId ? "" : undefined}
						key={agentId}
						tabIndex={-1}
					>
						<AgentAvatar className="size-7 shrink-0" decorative provider={agentId} />
						<div className="min-w-0 flex-1">
							<p className="truncate text-sm font-medium text-settings-label" id={`harness-agent-${agentId}`}>{agentLabel(agentId)}</p>
							<p className="truncate text-xs text-settings-muted">{connected ? t("settings.harness.loggedIn") : t("settings.harness.cloudNotConnected")}</p>
						</div>
						{loginAgent === agentId ? null : <Button data-harness-primary-action="" size="sm" variant={connected ? "outline" : "primary"} onClick={() => setLoginAgent(agentId)}>{t(connected ? "settings.harness.refreshLogin" : "settings.harness.login")}</Button>}
						{loginAgent === agentId ? <div className="basis-full pl-10"><CloudHarnessLoginPanel agent={agentId} onClose={() => setLoginAgent(null)} /></div> : null}
					</div>;
				})}
				{rows.length === 0 ? <p className="px-3 py-6 text-center text-sm text-settings-muted">{t("settings.harness.noResults")}</p> : null}
			</div>}
		{org?.id ? <div className="mt-3 border-t border-border px-3 pt-3"><CloudGitHubPatRow /></div> : null}
	</>;
}

function LocalHarnessContent({ focusAgentId, hostId, search }: { focusAgentId?: string; hostId?: string; search: string }) {
	const { i18n, t } = useTranslation();
	const queryClient = useQueryClient();
	const client = clientForSessionHost(hostId);
	const readinessKey = useMemo(() => agentReadinessQueryKeyForHost(hostId), [hostId]);
	const installerKey = useMemo(() => hostId ? [...installerQueryKey, hostId] : installerQueryKey, [hostId]);
	const jobsKey = useMemo(() => hostId ? [...installJobsQueryKey, hostId] : installJobsQueryKey, [hostId]);
	const authPlansKey = useMemo(() => agentAuthPlansQueryKeyForHost(hostId), [hostId]);
	const shellKey = useMemo(() => shellTerminalsQueryKeyForHost(hostId), [hostId]);
	const agents = useAgentReadinessQuery(true, hostId);
	const installers = useQuery({ queryKey: installerKey, queryFn: () => fetchInstallers(hostId), staleTime: 60_000 });
	const jobs = useQuery({ queryKey: jobsKey, queryFn: () => fetchInstallJobs(hostId), retry: false });
	const authPlans = useAgentAuthPlans(hostId);
	const startAgentAuth = useStartAgentAuth(hostId);
	const [authStates, setAuthStates] = useState<AgentAuthStates>({});
	const [actionErrors, setActionErrors] = useState<Partial<Record<AgentId, string>>>({});
	const [selectedMethods, setSelectedMethods] = useState<Partial<Record<AgentId, string>>>({});
	const [expandedDiagnostics, setExpandedDiagnostics] = useState<Partial<Record<AgentId, boolean>>>({});
	const [copiedAgent, setCopiedAgent] = useState<AgentId | null>(null);
	const [authWorkflow, setAuthWorkflow] = useState<AuthTerminalWorkflow | null>(null);
	const authWorkflowRef = useRef<AuthTerminalWorkflow | null>(null);
	const authStartPendingRef = useRef(false);
	const mountedRef = useRef(true);
	authWorkflowRef.current = authWorkflow;
	const activeInstallJobs = useRef(new Set<AgentId>());
	const pendingActions = useRef(new Set<AgentId>());
	const authChecksInFlight = useRef(new Map<AgentId, Promise<AgentAuthProbeResult | undefined>>());
	const [pendingAgentIds, setPendingAgentIds] = useState<Set<AgentId>>(new Set());
	const rowsRef = useRef<HTMLDivElement>(null);
	const focusHandledRef = useRef(false);
	const highlightTimerRef = useRef<number | null>(null);
	const [highlightedAgentId, setHighlightedAgentId] = useState<AgentId | null>(null);

	const plans = useMemo(() => new Map(installers.data?.map((plan) => [plan.agentId, plan]) ?? []), [installers.data]);
	const jobMap = useMemo(() => new Map(jobs.data?.map((job) => [job.target, job]) ?? []), [jobs.data]);
	const agentAuthPlans = useMemo(() => new Map(authPlans.data?.map((plan) => [plan.agentId, plan]) ?? []), [authPlans.data]);
	const readinessAgents = useMemo(() => new Map(agents.data?.agents.map((agent) => [agent.id, agent]) ?? []), [agents.data]);
	const installed = useMemo(
		() => new Set<AgentId>(agents.data?.agents.filter((agent) => agent.installation.state === "installed").map((agent) => agent.id as AgentId) ?? []),
		[agents.data],
	);
	const normalizedSearch = search.trim().toLowerCase();
	const targetAgentId = AGENT_OPTIONS.find((agentId) => agentId === focusAgentId) ?? null;
	const rows = AGENT_OPTIONS
		.filter((agentId) => agentId === targetAgentId || agentId === authWorkflow?.agentId || agentLabel(agentId).toLowerCase().includes(normalizedSearch))
		.sort((left, right) => {
			const leftAgent = readinessAgents.get(left);
			const rightAgent = readinessAgents.get(right);
			const authOrder = AUTH_STATE_RANK[leftAgent?.authentication.state ?? "unknown"]
				- AUTH_STATE_RANK[rightAgent?.authentication.state ?? "unknown"];
			if (authOrder !== 0) return authOrder;
			return INSTALL_STATE_RANK[leftAgent?.installation.state ?? "unknown"]
				- INSTALL_STATE_RANK[rightAgent?.installation.state ?? "unknown"];
		});
	const updateAuthState = useCallback((agentId: AgentId, patch: Partial<AgentAuthState>) => {
		setAuthStates((current) => ({
			...current,
			[agentId]: { pending: false, error: null, ...current[agentId], ...patch },
		}));
	}, []);
	const activeKey = useMemo(
		() => (jobs.data ?? []).filter((job) => isActive(job)).map((job) => job.target).sort().join(","),
		[jobs.data],
	);
	const refreshInstalledAgent = useCallback((agentId: AgentId) => {
		setActionErrors((current) => ({ ...current, [agentId]: undefined }));
		void client.POST("/api/v1/agents/{agent}/probe", {
			params: { path: { agent: agentId } },
		}).finally(async () => {
			try {
				const readiness = await ensureAgentReadiness([agentId], "display", hostId);
				cacheAgentReadiness(queryClient, readiness, hostId);
			} catch {
				await queryClient.invalidateQueries({ queryKey: readinessKey });
			} finally {
				await Promise.all([
					queryClient.invalidateQueries({ queryKey: installerKey }),
					queryClient.invalidateQueries({ queryKey: authPlansKey }),
					queryClient.invalidateQueries({ queryKey: hostId ? ["agent-models", hostId, agentId] : agentModelsQueryPrefix(agentId) }),
				]);
			}
		});
	}, [authPlansKey, client, hostId, installerKey, queryClient, readinessKey]);

	useEffect(() => {
		let active = true;
		const invalidateHarnessQueries = () => Promise.all([
			queryClient.invalidateQueries({ queryKey: readinessKey }),
			queryClient.invalidateQueries({ queryKey: installerKey }),
			queryClient.invalidateQueries({ queryKey: jobsKey }),
			queryClient.invalidateQueries({ queryKey: authPlansKey }),
		]);
		// Page-open refresh stays silent, but a failed refresh must not leave
		// stale or unknown readiness in place: fall back to ensure, and re-fetch
		// the readiness snapshot if that fails too.
		const recoverReadiness = async () => {
			try {
				const readiness = await ensureAgentReadiness([], "display", hostId);
				if (active) cacheAgentReadiness(queryClient, readiness, hostId);
			} catch {
				if (active) await queryClient.invalidateQueries({ queryKey: readinessKey });
			}
		};
		void client.POST("/api/v1/agents/refresh").then(async ({ error }) => {
			if (!active) return;
			if (error) {
				await recoverReadiness();
				return;
			}
			await invalidateHarnessQueries();
		}).catch(() => {
			if (active) void recoverReadiness();
		});
		return () => { active = false; };
	}, [authPlansKey, client, hostId, installerKey, jobsKey, queryClient, readinessKey]);
	useEffect(() => {
		if (focusHandledRef.current || !targetAgentId) return;
		if (agents.isPending || installers.isPending || jobs.isPending || authPlans.isPending) return;
		const row = Array.from(rowsRef.current?.querySelectorAll<HTMLElement>("[data-agent]") ?? [])
			.find((candidate) => candidate.dataset.agent === targetAgentId);
		if (!row) return;

		focusHandledRef.current = true;
		row.scrollIntoView({ behavior: "smooth", block: "center" });
		const primaryAction = row.querySelector<HTMLElement>("[data-harness-primary-action]:not(:disabled)");
		(primaryAction ?? row).focus({ preventScroll: true });
		setHighlightedAgentId(targetAgentId);
		highlightTimerRef.current = window.setTimeout(() => setHighlightedAgentId(null), FOCUS_HIGHLIGHT_MS);
	}, [agents.isPending, authPlans.isPending, installers.isPending, jobs.isPending, targetAgentId]);

	useEffect(() => () => {
		if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
	}, []);

	useEffect(() => {
		if (!activeKey) return;
		const timer = window.setInterval(() => void jobs.refetch(), POLL_INTERVAL_MS);
		return () => window.clearInterval(timer);
	}, [activeKey, jobs.refetch]);

	useEffect(() => {
		for (const job of jobs.data ?? []) {
			const agentId = job.target as AgentId;
			if (isActive(job)) {
				activeInstallJobs.current.add(agentId);
				continue;
			}
			const completedWhileMounted = activeInstallJobs.current.delete(agentId);
			if (job.status !== "succeeded" || !completedWhileMounted) continue;
			refreshInstalledAgent(agentId);
		}
	}, [jobs.data, refreshInstalledAgent]);

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
		queryClient.setQueryData<InstallJob[]>(jobsKey, (current) => upsertJob(current, job));
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
			const { data, error } = await client.POST("/api/v1/agents/{agent}/install", {
				params: { path: { agent: agentId } },
				body: { method, operation: "install" },
			});
			if (error || !data) {
				setActionErrors((current) => ({ ...current, [agentId]: apiErrorMessage(error, t("settings.harness.startFailed")) }));
				return;
			}
			updateJob(data);
			if (data.status === "succeeded") refreshInstalledAgent(agentId);
		} finally {
			endAction(agentId);
		}
	};

	const verifyInstall = async (agentId: AgentId) => {
		if (!beginAction(agentId)) return;
		setActionErrors((current) => ({ ...current, [agentId]: undefined }));
		try {
			const { data, error } = await client.POST("/api/v1/agents/{agent}/verify", {
				params: { path: { agent: agentId } },
			});
			if (error || !data) {
				setActionErrors((current) => ({ ...current, [agentId]: apiErrorMessage(error, t("settings.harness.verifyFailed", { agent: agentLabel(agentId) })) }));
				return;
			}
			updateJob(data);
			if (data.status === "succeeded") refreshInstalledAgent(agentId);
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
			if (!mountedRef.current) {
				await closeAuthTerminal(result.terminal.handleId, hostId);
				queryClient.setQueryData<ShellTerminal[]>(shellKey, (current) => current?.filter((terminal) => terminal.handleId !== result.terminal.handleId));
				void queryClient.invalidateQueries({ queryKey: shellKey });
				return;
			}
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
			void queryClient.invalidateQueries({ queryKey: shellKey });
		} catch (error) {
			if (mountedRef.current) updateAuthState(agentId, { error: error instanceof Error ? error.message : t("settings.harness.authFailed") });
		} finally {
			authStartPendingRef.current = false;
			if (mountedRef.current) updateAuthState(agentId, { pending: false });
		}
	};

	const checkAuth = useCallback(async (
		agentId: AgentId,
		{ fresh = false }: { fresh?: boolean } = {},
	): Promise<AgentAuthProbeResult | undefined> => {
		const existing = authChecksInFlight.current.get(agentId);
		if (existing && !fresh) return existing;
		const check = (async () => {
			if (existing) await existing;
			try {
				const result = await probeAgentAuth(agentId, hostId);
				const readiness = await ensureAgentReadiness([agentId], "display", hostId);
				cacheAgentReadiness(queryClient, readiness, hostId);
				return result;
			} catch {
				return undefined;
			}
		})();
		authChecksInFlight.current.set(agentId, check);
		const finishCheck = () => {
			if (authChecksInFlight.current.get(agentId) === check) authChecksInFlight.current.delete(agentId);
		};
		void check.then(finishCheck, finishCheck);
		return check;
	}, [hostId, queryClient]);

	const finishAuth = useCallback(async (workflow: AuthTerminalWorkflow) => {
		if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return;
		setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "verifying", reason: undefined } : current);
		// MiMo can confirm a stored provider key locally without validating it upstream.
		const loggedIn = (candidate: AgentAuthProbeResult | undefined) =>
			candidate?.agent.authStatus === "authorized" || (workflow.agentId === "mimo-code" && candidate?.agent.authStatus === "configured");
		let result = await checkAuth(workflow.agentId, { fresh: true });
		for (let attempt = 1; attempt < AUTH_VERIFY_ATTEMPTS && !loggedIn(result); attempt++) {
			await new Promise((resolve) => window.setTimeout(resolve, AUTH_VERIFY_RETRY_MS));
			if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return;
			result = await checkAuth(workflow.agentId, { fresh: true });
		}
		if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return;
		if (loggedIn(result)) {
			try {
				await closeAuthTerminal(workflow.terminal.handleId, hostId);
			} catch (error) {
				setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "cleanup_failed", reason: error instanceof Error ? error.message : t("settings.harness.authFailed") } : current);
				return;
			}
			authWorkflowRef.current = null;
			setAuthWorkflow(null);
			void queryClient.invalidateQueries({ queryKey: shellKey });
			return;
		}
		setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? {
			...current,
			phase: result?.agent.authStatus === "unauthorized" ? "unauthorized" : "unverified",
			reason: result?.agent.authStatus === "unauthorized" ? t("settings.harness.notLoggedIn") : t("settings.harness.loginUnknown"),
		} : current);
	}, [checkAuth, hostId, queryClient, shellKey, t]);

	const closeAuth = useCallback(async (workflow: AuthTerminalWorkflow): Promise<boolean> => {
		if (authWorkflowRef.current?.terminal.handleId !== workflow.terminal.handleId) return false;
		setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "closing", reason: undefined } : current);
		try {
			await closeAuthTerminal(workflow.terminal.handleId, hostId);
			authWorkflowRef.current = null;
			setAuthWorkflow(null);
			void queryClient.invalidateQueries({ queryKey: shellKey });
			await checkAuth(workflow.agentId, { fresh: true });
			return true;
		} catch (error) {
			setAuthWorkflow((current) => current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "cleanup_failed", reason: error instanceof Error ? error.message : t("settings.harness.authFailed") } : current);
			return false;
		}
	}, [checkAuth, hostId, queryClient, shellKey, t]);

	// A login the panel could not confirm may still have taken: the daemon keeps
	// re-checking readiness. Once the harness reads as logged in, close the panel
	// instead of leaving a stale "signed out" terminal on screen.
	useEffect(() => {
		if (!authWorkflow || (authWorkflow.phase !== "unauthorized" && authWorkflow.phase !== "unverified")) return;
		if (readinessAgents.get(authWorkflow.agentId)?.authentication.state === "authorized") void closeAuth(authWorkflow);
	}, [authWorkflow, readinessAgents, closeAuth]);

	useEffect(() => {
		if (!authWorkflow || authWorkflow.phase !== "running") return;
		const handleId = authWorkflow.terminal.handleId;
		const remaining = Math.max(0, AUTH_TERMINAL_LIFETIME_MS - (Date.now() - authWorkflow.startedAt));
		const timeout = window.setTimeout(async () => {
			if (authWorkflowRef.current?.terminal.handleId !== handleId) return;
			setAuthWorkflow((current) => current?.terminal.handleId === handleId ? { ...current, phase: "closing", reason: undefined } : current);
			try {
				await closeAuthTerminal(handleId, hostId);
				setAuthWorkflow((current) => current?.terminal.handleId === handleId ? { ...current, phase: "timed_out", reason: t("settings.harness.authTimedOut") } : current);
				void queryClient.invalidateQueries({ queryKey: shellKey });
				await checkAuth(authWorkflow.agentId, { fresh: true });
			} catch (error) {
				setAuthWorkflow((current) => current?.terminal.handleId === handleId ? { ...current, phase: "cleanup_failed", reason: error instanceof Error ? error.message : t("settings.harness.authFailed") } : current);
			}
		}, remaining);
		return () => window.clearTimeout(timeout);
	}, [authWorkflow, checkAuth, hostId, queryClient, shellKey, t]);

	useEffect(() => {
		mountedRef.current = true;
		return () => {
			mountedRef.current = false;
			const workflow = authWorkflowRef.current;
			if (workflow) void closeAuthTerminal(workflow.terminal.handleId, hostId).catch(() => undefined);
		};
	}, [hostId]);

	return (
		<>
			{installers.error || authPlans.error || agents.error || jobs.error ? (
				<div className="flex items-center gap-2 rounded-md border border-error/30 bg-error/10 px-3 py-2 text-xs text-error">
					<TriangleAlert className="size-4" aria-hidden="true" />
					{jobs.error instanceof Error ? jobs.error.message : t("settings.harness.loadFailed")}
				</div>
			) : null}

			<div className="settings-grouped-rows flex w-full flex-col" ref={rowsRef}>
			{rows.map((agentId) => {
					const plan = plans.get(agentId);
					const job = jobMap.get(agentId);
					const isInstalled = installed.has(agentId);
					const availableMethods = plan?.methods.filter((method) => method.available) ?? [];
					const recommendedMethod = availableMethods.find((method) => method.recommended) ?? availableMethods[0];
					const selectedMethodId = selectedMethods[agentId] ?? (availableMethods.some((method) => method.id === job?.method) ? job?.method : recommendedMethod?.id) ?? "";
					const selectedMethod = availableMethods.find((method) => method.id === selectedMethodId);
					const pending = pendingAgentIds.has(agentId);
					const actionError = actionErrors[agentId];
					const failed = job?.status === "failed" || job?.status === "unsupported" || job?.status === "interrupted" || Boolean(actionError);
					const active = isActive(job);
						const readinessAgent = readinessAgents.get(agentId);
						const incompatibleVersionReason = readinessAgent?.installation.reasonCode === "install_incompatible_version"
							? readinessAgent.installation.reason
							: undefined;
						// Hold back install actions only while readiness is still loading or
						// the daemon reports the installation as not yet observed. A failed
						// readiness fetch or an agent missing from the snapshot falls back to
						// the installer plan so install controls stay usable.
						const installationPending = !agents.error
							&& (agents.isPending || readinessAgent?.installation.state === "unknown");
						const authPlan = agentAuthPlans.get(agentId);
						const isSetupAction = authPlan?.action === "setup";
						const authState = authStates[agentId];
						const authStatus = readinessAgent?.authentication.state;
						const mimoConfigured = agentId === "mimo-code" && authStatus === "configured";
						const installationStatusLabel = t("settings.harness.installed");
						const showInstallationStatus = authStatus === "authorized"
							|| authStatus === "not_applicable"
							|| mimoConfigured
							|| (!authPlans.isPending && (!authPlan || authPlan.action === "instructions"));
						const rowHasError = failed || Boolean(authState?.error);
						const rowAuthWorkflow = authWorkflow?.agentId === agentId ? authWorkflow : null;
						const hasDiagnostics = Boolean(
							job &&
							(job.status === "failed" || job.status === "unsupported" || job.status === "interrupted") &&
							(job.error || job.output || job.method || job.expectedDestination),
						);

						const authSummary = authState?.error
							? authState.error
							: authStatus === "configured"
								? t("settings.harness.configured")
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
						const authControls = authPlan && authPlan.action !== "instructions" ? (
							<>
								{authStatus !== "authorized" && !mimoConfigured ? (
									<Button data-harness-primary-action="" data-terminal-focus-handoff="true" disabled={!authPlan.available || authState?.pending || Boolean(authWorkflow)} size="sm" onClick={() => void startAuth(agentId)}>
										{authState?.pending ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
										{authState?.pending ? t("settings.harness.loggingIn") : isSetupAction ? t("settings.harness.setup") : t("settings.harness.login")}
									</Button>
								) : null}
							</>
						) : null;
						// A logged-in harness's only action is to re-run its login.
						const refreshLocal = authPlan?.action === "login" && authStatus === "authorized" ? (
							<Button type="button" size="sm" variant="outline" disabled={!authPlan.available || authState?.pending || Boolean(authWorkflow)} onClick={() => void startAuth(agentId)}>
								{t("settings.harness.refreshLogin")}
							</Button>
						) : null;
					const localControls = active ? (
				<span className="inline-flex items-center gap-1.5 text-xs text-settings-muted" role="status"><LoaderCircle className="size-4 animate-spin" aria-hidden="true" />{job?.status === "installing" ? t("settings.harness.installing") : t("settings.harness.verifying")}</span>
							) : isInstalled ? (
								<div className="flex shrink-0 items-center gap-2">
								{/* The subtitle already states a login ("Connected", "Configured"); the
								    chip is only for installed harnesses whose subtitle doesn't say so. */}
								{showInstallationStatus && authStatus !== "authorized" && !mimoConfigured ? (
									<Button
										type="button"
										size="none"
										variant="ghost"
										className={cn(MENU_TRIGGER_CHROME, "h-8! min-h-8! shrink-0 rounded-md! border-0! bg-[var(--color-bg-settings-trigger)] px-3! text-xs leading-4")}
										aria-label={installationStatusLabel}
										disabled
									>
										{installationStatusLabel}
									</Button>
								) : null}
								{authControls}
								{refreshLocal}
								</div>
							) : failed ? (
								<div className="flex items-center gap-1.5">
									{methodSelect}
									<Button size="sm" variant="outline" disabled={pending} onClick={() => void verifyInstall(agentId)}>{t("settings.harness.verifyAgain")}</Button>
									{selectedMethodId ? <Button className={MENU_TRIGGER_CHROME} size="sm" variant="ghost" onClick={() => void startInstall(agentId, selectedMethodId)} disabled={pending}>{t("settings.harness.retry")}</Button> : null}
								</div>
							) : installationPending ? null : availableMethods.length > 0 ? (
				<div className="flex items-stretch overflow-hidden rounded-md bg-[var(--color-bg-settings-trigger)]">
									<Button
										data-harness-primary-action=""
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
							) : null;
					return (
						<div
							aria-labelledby={`harness-agent-${agentId}`}
							className={cn(
								"settings-row-bar min-h-14 flex-wrap gap-3 transition-[background-color,box-shadow] duration-200",
								highlightedAgentId === agentId && "bg-accent-weak ring-2 ring-inset ring-accent",
							)}
							data-agent={agentId}
							data-focus-highlighted={highlightedAgentId === agentId ? "" : undefined}
							key={agentId}
							tabIndex={-1}
						>
							<AgentAvatar className="size-7 shrink-0" decorative provider={agentId} />
							<div className="min-w-0 flex-1">
								<div className="flex items-center gap-1.5">
									<p className="truncate text-sm font-medium text-settings-label" id={`harness-agent-${agentId}`}>{agentLabel(agentId)}</p>
								</div>
								<p className={cn("truncate text-xs text-settings-muted", rowHasError && "text-error")} title={authState?.error ?? actionError ?? job?.error ?? incompatibleVersionReason ?? authPlan?.reason ?? plan?.reason}>
									{isInstalled ? authSummary : installationPending ? t("settings.harness.installationUnknown") : actionError ?? (job?.status === "interrupted" ? t("settings.harness.interrupted") : failed ? (job?.error ?? t("settings.harness.installFailed")) : incompatibleVersionReason ?? (plan?.available ? t("settings.harness.availableWith", { method: availableMethodsLabel }) : (plan?.reason ?? t("settings.harness.manualRequired"))))}
								</p>
							</div>

			{localControls}

				{authPlan?.action === "instructions" && authPlan.documentationUrl ? (
					<Button size="icon-sm" variant="ghost" aria-label={t("settings.harness.instructions")} title={t("settings.harness.instructions")} onClick={() => void aoBridge.app.openExternal(authPlan.documentationUrl)}>
						<BookOpen aria-hidden="true" />
					</Button>
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
													hostId={hostId}
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
		</>
	);
}

/**
 * GitHub personal access token for cloud workers to clone private repositories.
 * Lives on the Harness page's cloud view (the only place cloud credentials are
 * managed) so GitHub connectivity stays reachable once a user is signed into a
 * cloud org. The PAT is personal: stored encrypted and never echoed back.
 */
function CloudGitHubPatRow() {
	const { t } = useTranslation();
	const { client } = useCloudCp();
	const queryClient = useQueryClient();
	const userConnections = useProviderConnections();
	const [githubPAT, setGitHubPAT] = useState("");
	const [githubPATBusy, setGitHubPATBusy] = useState(false);
	const [githubPATError, setGitHubPATError] = useState<string | null>(null);

	const githubPATConnected = (userConnections.data ?? []).some(
		(connection) => connection.provider === "github" && connection.label === "default" && connection.validationState === "valid",
	);
	const saveGitHubPAT = async () => {
		if (githubPAT.trim() === "") return;
		setGitHubPATBusy(true);
		setGitHubPATError(null);
		try {
			await client.putGitHubPAT({ secret: githubPAT.trim() });
			setGitHubPAT("");
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey });
		} catch (error) {
			setGitHubPATError(error instanceof Error ? error.message : t("settings.cloudAgents.github.errorSave"));
		} finally {
			setGitHubPATBusy(false);
		}
	};
	const removeGitHubPAT = async () => {
		setGitHubPATBusy(true);
		setGitHubPATError(null);
		try {
			await client.deleteGitHubPAT();
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey });
		} catch (error) {
			setGitHubPATError(error instanceof Error ? error.message : t("settings.cloudAgents.github.errorRemove"));
		} finally {
			setGitHubPATBusy(false);
		}
	};
	return (
		<div className="flex w-full flex-col gap-1.5">
			<SettingsRow key="github-pat" icon={KeyRound} label={t("settings.cloudAgents.github.title")}>
				<span className="text-sm leading-5 text-settings-muted">{githubPATConnected ? t("settings.cloudAgents.github.connected") : t("settings.cloudAgents.github.notConnected")}</span>
			</SettingsRow>
			<GitHubTokenField
				id="settings-github-pat"
				bare
				className="mt-2"
				label={t("settings.cloudAgents.github.tokenLabel")}
				hint={t("settings.cloudAgents.github.tokenHint")}
				value={githubPAT}
				disabled={githubPATBusy}
				error={githubPATError}
				submitLabel={githubPATBusy ? t("settings.cloudAgents.github.saving") : t("settings.cloudAgents.github.save")}
				submitVariant="outline"
				submitDisabled={githubPATBusy}
				onChange={setGitHubPAT}
				onSubmit={() => void saveGitHubPAT()}
			/>
			{githubPATConnected ? (
				<div className="mt-2 flex justify-end">
					<Button type="button" variant="footer" disabled={githubPATBusy} onClick={() => void removeGitHubPAT()}>
						{t("settings.cloudAgents.github.remove")}
					</Button>
				</div>
			) : null}
		</div>
	);
}

function HarnessAuthTerminalPanel({ workflow, hostId, onClose, onRetry, onTerminalState }: {
	workflow: AuthTerminalWorkflow;
	hostId?: string;
	onClose: () => void;
	onRetry: () => void;
	onTerminalState: (state: TerminalSessionState) => void;
}) {
	const { t } = useTranslation();
	const theme = useResolvedTheme();
	const shell = useShellMaybe();
	const createMux = useCallback(() => {
		const base = hostId && baseUrlForHost(hostId);
		if (!base) throw new Error("Remote host disconnected");
		return createTerminalMux(muxUrlFromApiBase(base));
	}, [hostId]);
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
	// While the login runs, the terminal speaks for itself. Guidance is shown only
	// when it tells the user to act outside the terminal (the Open login button).
	const status = workflow.phase === "running"
		? workflow.terminalInput ? workflow.guidance : ""
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
				<div className="min-w-0"><p className="truncate text-xs font-medium text-settings-label">{workflow.terminal.title}</p>{status ? <p className="truncate text-[11px] text-settings-muted" aria-live="polite" role="status">{status}</p> : null}</div>
				<div className="flex shrink-0 items-center gap-2">
					{workflow.terminalInput && workflow.phase === "running" ? <Button type="button" size="sm" variant="outline" disabled={terminalState !== "attached" || commandPending || commandSent} onClick={openAuthAction}>{commandSent ? <Check aria-hidden="true" /> : <LogIn aria-hidden="true" />}{workflow.action === "setup" ? commandSent ? t("settings.harness.setupOpened") : t("settings.harness.openSetup") : commandSent ? t("settings.harness.loginOpened") : t("settings.harness.openLogin")}</Button> : null}
					<button type="button" aria-label={t("settings.close")} className="grid size-7 place-items-center rounded text-settings-muted hover:bg-interactive-hover" disabled={workflow.phase === "closing" || workflow.phase === "verifying"} onClick={onClose}><X className="size-4" aria-hidden="true" /></button>
				</div>
			</div>
			<div className="h-[300px] min-h-0"><TerminalPane createMux={hostId ? createMux : undefined} daemonReady={hostId ? true : shell ? shell.daemonStatus.state === "ready" : true} focusRequested={workflow.phase === "running" && terminalState === "attached"} fontSize={12} inputRequest={inputRequest} onInputRequestResult={handleInputRequestResult} onTerminalStateChange={handleTerminalState} terminalTarget={{ kind: "shell", handleId: workflow.terminal.handleId, generation: workflow.terminal.createdAt, title: workflow.terminal.title }} theme={theme} /></div>
			{retryable ? <div className="flex items-center justify-end border-t border-(--color-border-settings-input) bg-surface/90 px-3 py-2"><Button type="button" size="sm" variant="outline" onClick={workflow.phase === "cleanup_failed" ? onClose : onRetry}>{workflow.phase === "cleanup_failed" ? t("settings.harness.retry") : workflow.action === "setup" ? t("settings.harness.setup") : t("settings.harness.login")}</Button></div> : null}
		</div>
	);
}
