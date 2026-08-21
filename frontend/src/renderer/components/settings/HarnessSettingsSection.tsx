import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Download, ExternalLink, LoaderCircle, RefreshCw, Search, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { useAgentsQuery, agentsQueryKey, refreshAgents, refreshAgentsIfStale, type AgentCatalog } from "../../hooks/useAgentsQuery";
import { AGENT_OPTIONS, agentLabel, type AgentId } from "../../lib/agent-options";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { AgentAvatar } from "../AgentAvatar";
import { Button } from "../ui/button";
import { SettingsSection } from "./SettingsSection";

type AgentInstallPlan = components["schemas"]["AgentInstallPlan"];
type InstallJob = components["schemas"]["InstallJob"];

const installerQueryKey = ["agent-installers"] as const;
const POLL_INTERVAL_MS = 1_000;

async function fetchInstallers(): Promise<AgentInstallPlan[]> {
	const { data, error } = await apiClient.GET("/api/v1/agents/installers");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load harness installers."));
	return data.agents;
}

function addInstalledAgent(catalog: AgentCatalog | undefined, agentId: AgentId): AgentCatalog | undefined {
	if (!catalog || catalog.installed.some((agent) => agent.id === agentId)) return catalog;
	const supported = catalog.supported.find((agent) => agent.id === agentId);
	if (!supported) return catalog;
	return { ...catalog, installed: [...catalog.installed, supported] };
}

export function HarnessSettingsSection({ titleHidden = false }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const agents = useAgentsQuery();
	const installers = useQuery({ queryKey: installerQueryKey, queryFn: fetchInstallers, staleTime: 60_000 });
	const [search, setSearch] = useState("");
	const [activeAgent, setActiveAgent] = useState<AgentId | null>(null);
	const [job, setJob] = useState<InstallJob | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);
	const [verifying, setVerifying] = useState(false);
	const [copiedAgent, setCopiedAgent] = useState<AgentId | null>(null);
	const verificationRef = useRef<string | null>(null);

	useEffect(() => {
		void refreshAgentsIfStale().then((fresh) => {
			if (fresh) queryClient.setQueryData(agentsQueryKey, fresh);
		});
	}, [queryClient]);

	const plans = useMemo(() => new Map(installers.data?.map((plan) => [plan.agentId, plan]) ?? []), [installers.data]);
	const installed = useMemo(() => new Set(agents.data?.installed.map((agent) => agent.id) ?? []), [agents.data]);
	const normalizedSearch = search.trim().toLowerCase();
	const rows = AGENT_OPTIONS.filter((agentId) => agentLabel(agentId).toLowerCase().includes(normalizedSearch));
	const isBusy = job?.status === "running" || verifying;

	useEffect(() => {
		if (!activeAgent || job?.status !== "running") return;
		const timer = window.setInterval(() => {
			void (async () => {
				const { data, error } = await apiClient.GET("/api/v1/agents/{agent}/install", {
					params: { path: { agent: activeAgent } },
				});
				if (!error && data) setJob(data);
			})();
		}, POLL_INTERVAL_MS);
		return () => window.clearInterval(timer);
	}, [activeAgent, job?.status]);

	useEffect(() => {
		if (!activeAgent || job?.status !== "succeeded" || verificationRef.current === activeAgent) return;
		verificationRef.current = activeAgent;
		setVerifying(true);
		void (async () => {
			const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/probe", {
				params: { path: { agent: activeAgent } },
			});
			if (error || !data?.installed) {
				setActionError(
					apiErrorMessage(error, t("settings.harness.verifyFailed", { agent: agentLabel(activeAgent) })),
				);
				return;
			}
			queryClient.setQueryData<AgentCatalog | undefined>(agentsQueryKey, (current) => addInstalledAgent(current, activeAgent));
			await queryClient.invalidateQueries({ queryKey: agentsQueryKey });
		})().finally(() => setVerifying(false));
	}, [activeAgent, job?.status, queryClient, t]);

	const startInstall = async (agentId: AgentId) => {
		setActiveAgent(agentId);
		setJob(null);
		setActionError(null);
		setVerifying(false);
		verificationRef.current = null;
		const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/install", {
			params: { path: { agent: agentId } },
		});
		if (error || !data) {
			setActionError(apiErrorMessage(error, t("settings.harness.startFailed")));
			return;
		}
		setJob(data);
	};

	const copyCommand = async (agentId: AgentId, command: string) => {
		await aoBridge.clipboard.writeText(command);
		setCopiedAgent(agentId);
		window.setTimeout(() => setCopiedAgent((current) => (current === agentId ? null : current)), 1_500);
	};

	const refresh = async () => {
		setActionError(null);
		try {
			const [fresh] = await Promise.all([
				refreshAgents(),
				queryClient.invalidateQueries({ queryKey: installerQueryKey }),
			]);
			queryClient.setQueryData(agentsQueryKey, fresh);
		} catch (error) {
			setActionError(error instanceof Error ? error.message : t("settings.harness.loadFailed"));
		}
	};

	return (
		<SettingsSection title={t("settings.harness")} titleHidden={titleHidden} sectionId="harness">
			<div className="flex items-center gap-2 px-1">
				<label className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3">
					<Search aria-hidden="true" className="size-4 shrink-0 text-settings-muted" />
					<span className="sr-only">{t("settings.harness.search")}</span>
					<input
						aria-label={t("settings.harness.search")}
						className="min-w-0 flex-1 bg-transparent text-sm text-settings-label outline-none placeholder:text-settings-muted"
						placeholder={t("settings.harness.searchPlaceholder")}
						value={search}
						onChange={(event) => setSearch(event.target.value)}
					/>
				</label>
				<Button aria-label={t("settings.harness.refresh")} size="icon-sm" variant="outline" onClick={() => void refresh()}>
					<RefreshCw className={cn((agents.isFetching || installers.isFetching) && "animate-spin")} />
				</Button>
			</div>

			<p className="px-1 text-xs text-settings-muted">
				{t("settings.harness.summary", { installed: installed.size, total: AGENT_OPTIONS.length })}
			</p>

			{installers.isError || agents.isError || (actionError && !activeAgent) ? (
				<div className="flex items-center gap-2 rounded-md border border-error/30 bg-error/10 px-3 py-2 text-xs text-error">
					<TriangleAlert className="size-4" aria-hidden="true" />
					{actionError && !activeAgent ? actionError : t("settings.harness.loadFailed")}
				</div>
			) : null}

			<div className="settings-grouped-rows flex w-full flex-col">
				{rows.map((agentId) => {
					const plan = plans.get(agentId);
					const isInstalled = installed.has(agentId);
					const isActive = activeAgent === agentId;
					const activeStatus = isActive ? job?.status : undefined;
					const failed = isActive && (activeStatus === "failed" || activeStatus === "unsupported" || actionError);
					const running = isActive && activeStatus === "running";
					const rowVerifying = isActive && verifying;
					return (
						<div className="settings-row-bar min-h-14 gap-3" data-agent={agentId} key={agentId}>
							<AgentAvatar className="size-7 shrink-0" decorative provider={agentId} />
							<div className="min-w-0 flex-1">
								<p className="truncate text-sm font-medium text-settings-label">{agentLabel(agentId)}</p>
								<p className={cn("truncate text-xs text-settings-muted", failed && "text-error")} title={failed ? (actionError ?? job?.error) : plan?.reason}>
									{isInstalled
										? t("settings.harness.installed")
										: running
											? t("settings.harness.installing")
											: rowVerifying
												? t("settings.harness.verifying")
												: failed
													? (actionError ?? job?.error ?? t("settings.harness.installFailed"))
													: plan?.available
														? t("settings.harness.availableWith", { method: plan.method })
														: (plan?.reason ?? t("settings.harness.manualRequired"))}
								</p>
							</div>
							{isInstalled ? (
								<span className="inline-flex items-center gap-1 text-xs font-medium text-success">
									<Check className="size-4" aria-hidden="true" />
									{t("settings.harness.installed")}
								</span>
							) : running || rowVerifying ? (
								<span className="inline-flex items-center gap-1.5 text-xs text-settings-muted" role="status">
									<LoaderCircle className="size-4 animate-spin" aria-hidden="true" />
									{running ? t("settings.harness.installing") : t("settings.harness.verifying")}
								</span>
							) : !plan && installers.isPending ? (
								<span className="inline-flex items-center gap-1.5 text-xs text-settings-muted" role="status">
									<LoaderCircle className="size-4 animate-spin" aria-hidden="true" />
								</span>
							) : plan?.available && plan.automatic ? (
								<Button disabled={isBusy && !isActive} size="sm" onClick={() => void startInstall(agentId)}>
									<Download aria-hidden="true" />
									{failed ? t("settings.harness.retry") : t("settings.harness.install")}
								</Button>
							) : plan?.command ? (
								<Button size="sm" variant="outline" onClick={() => void copyCommand(agentId, plan.command!)}>
									{copiedAgent === agentId ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
									{copiedAgent === agentId ? t("settings.harness.copied") : t("settings.harness.copyCommand")}
								</Button>
							) : (
								<Button size="sm" variant="outline" onClick={() => void aoBridge.app.openExternal(plan?.documentationUrl ?? "https://aoagents.dev/docs/installation")}>
									<ExternalLink aria-hidden="true" />
									{t("settings.harness.instructions")}
								</Button>
							)}
						</div>
					);
				})}
				{rows.length === 0 ? <p className="px-3 py-6 text-center text-sm text-settings-muted">{t("settings.harness.noResults")}</p> : null}
			</div>
		</SettingsSection>
	);
}
