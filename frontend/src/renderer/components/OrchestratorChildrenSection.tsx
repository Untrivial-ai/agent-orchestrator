import { InspectorSection, inspectorEmptyClass } from "@aoagents/product-ui";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { useOrchestratorChildren, type OrchestratorChildView } from "../hooks/useOrchestratorChildren";
import { cn } from "../lib/utils";
import { getSessionStatusDotView, getSessionStatusView } from "../lib/session-presentation";
import { captureRendererEvent } from "../lib/telemetry";
import type { PullRequestFacts, WorkspaceSession } from "../types/workspace";
import { AgentAvatar } from "./AgentAvatar";
import { ProductExternalLink } from "./ProductExternalLink";

/**
 * The Workers section of a cloud orchestrator's Summary tab: every session the
 * orchestrator spawned, with live status and its pull requests. Rows navigate
 * to the worker session; PR chips open the PR externally.
 */
export function OrchestratorChildrenSection({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const query = useOrchestratorChildren(session);
	const children = query.data ?? [];
	const viewedRef = useRef(false);
	useEffect(() => {
		if (viewedRef.current || query.data === undefined) return;
		viewedRef.current = true;
		void captureRendererEvent("ao.renderer.cloud_workers_viewed", {
			worker_count: query.data.length,
		});
	}, [query.data]);

	const title =
		children.length > 0 ? t("inspector.workersCount", { count: children.length }) : t("inspector.workers");
	return (
		<InspectorSection surface={false} title={title}>
			<div className="flex flex-col gap-1" data-testid="orchestrator-children">
				{query.isLoading ? (
					<p className={inspectorEmptyClass}>{t("inspector.workersLoading")}</p>
				) : query.isError ? (
					<p className={inspectorEmptyClass} role="status">
						{t("inspector.workersUnavailable")}
					</p>
				) : children.length === 0 ? (
					<p className={inspectorEmptyClass}>{t("inspector.workersEmpty")}</p>
				) : (
					children.map((child) => (
						<ChildRow
							key={child.id}
							child={child}
							onOpen={() => {
								void captureRendererEvent("ao.renderer.cloud_worker_opened", {
									has_pr: child.prs.length > 0,
								});
								void navigate({
									to: "/projects/$projectId/sessions/$sessionId",
									params: { projectId: session.workspaceId, sessionId: child.id },
								});
							}}
						/>
					))
				)}
			</div>
		</InspectorSection>
	);
}

function ChildRow({ child, onOpen }: { child: OrchestratorChildView; onOpen: () => void }) {
	const { t } = useTranslation();
	const dot = getSessionStatusDotView(child);
	const statusLabel = getSessionStatusView(child.status, t).label;
	return (
		<div
			className={cn(
				"overflow-hidden rounded-settings-row bg-settings-row px-3 py-1.5",
				child.isTerminated && "opacity-60",
			)}
			data-testid="orchestrator-child-row"
		>
			<button
				className="flex w-full min-w-0 cursor-pointer items-center gap-2 text-left"
				onClick={onOpen}
				type="button"
			>
				<span
					aria-hidden="true"
					className={cn("size-2 shrink-0 rounded-full", dot.className, dot.breathe && "animate-status-pulse")}
					data-session-status={child.status}
				/>
				<AgentAvatar className="size-4 shrink-0" decorative provider={child.provider} />
				<span className="min-w-0 flex-1 truncate text-xs">{child.title}</span>
				<span className="shrink-0 text-2xs text-settings-muted">{statusLabel}</span>
			</button>
			{child.prs.length > 0 ? (
				<div className="mt-1 flex flex-wrap items-center gap-1.5 pl-4">
					{child.prs.map((pr) => (
						<PullRequestChip key={pr.url || pr.number} pr={pr} />
					))}
				</div>
			) : null}
		</div>
	);
}

function PullRequestChip({ pr }: { pr: PullRequestFacts }) {
	const { t } = useTranslation();
	const stateLabel = t(`pr.state.${pr.state}`, { defaultValue: pr.state });
	const ciSuffix = pr.ci && pr.ci !== "unknown" ? ` · CI ${pr.ci}` : "";
	return (
		<ProductExternalLink
			ariaLabel={`${t("pr.short")} #${pr.number}`}
			className="truncate text-2xs text-settings-muted underline-offset-2 hover:underline"
			href={pr.url}
			stopPropagation
		>
			{t("pr.short")} #{pr.number} · {stateLabel}
			{ciSuffix}
		</ProductExternalLink>
	);
}
