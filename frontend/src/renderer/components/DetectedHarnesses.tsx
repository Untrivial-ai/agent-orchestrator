import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import {
	buildRankedAgentOptions,
	DEFAULT_AGENT_PRIORITY_RANK,
	type AgentInfo,
	unknownAgentReadiness,
} from "../lib/agent-select-options";
import { AGENT_OPTIONS } from "../lib/agent-options";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./AgentAvatar";

/**
 * First-launch / Add project strip: surfaces daemon harness detection so the
 * user can see which agent CLIs are ready before picking a folder.
 */
export function DetectedHarnesses({ className }: { className?: string }) {
	const { t } = useTranslation();
	const agentsQuery = useAgentReadinessQuery(true);
	useEnsureAgentReadiness({ enabled: true });
	const fallbackAgents: AgentInfo[] = useMemo(
		() => AGENT_OPTIONS.map((agent) => unknownAgentReadiness(agent, agent)),
		[],
	);
	const options = useMemo(
		() =>
			buildRankedAgentOptions({
				agents: agentsQuery.data?.agents,
				priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
				fallbackAgents,
			}).slice(0, 5),
		[agentsQuery.data?.agents, fallbackAgents],
	);
	const readyCount = options.filter((agent) => !agent.disabled && agent.installation.state === "installed").length;
	const loading = agentsQuery.data === undefined && agentsQuery.isFetching;

	if (loading) {
		return (
			<div className={cn("w-full max-w-preview-content px-1", className)} data-testid="detected-harnesses">
				<p className="text-[12px] text-muted-foreground">{t("onboarding.detectingAgents")}</p>
			</div>
		);
	}

	if (options.length === 0) return null;

	return (
		<div className={cn("w-full max-w-preview-content space-y-2 px-1", className)} data-testid="detected-harnesses">
			<p className="text-[13px] font-medium text-foreground">
				{readyCount > 0
					? t("onboarding.detectedAgents", { count: readyCount })
					: t("onboarding.noAgentsDetected")}
			</p>
			<ul className="flex flex-col gap-1.5">
				{options.map((agent) => (
					<li
						key={agent.id}
						className={cn(
							"flex items-center gap-3 rounded-lg px-3 py-2 text-sm",
							agent.disabled ? "text-muted-foreground" : "bg-[var(--color-bg-import-card)] text-foreground",
						)}
					>
						<AgentAvatar provider={agent.id} className="size-icon-lg" decorative />
						<span className="min-w-0 flex-1 truncate font-medium">{agent.label}</span>
						<span
							className={cn(
								"shrink-0 text-caption",
								agent.statusTone === "success" && "text-success",
								agent.statusTone === "warning" && "text-warning",
								agent.statusTone === "muted" && "text-muted-foreground",
								!agent.status && "text-success",
							)}
						>
							{agent.status || t("onboarding.agentReady")}
						</span>
					</li>
				))}
			</ul>
		</div>
	);
}
