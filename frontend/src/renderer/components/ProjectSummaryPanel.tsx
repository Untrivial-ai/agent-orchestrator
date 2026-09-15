import { ArrowUpRight, MessageSquareText, RefreshCw, X } from "lucide-react";
import { useEffect } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { WorkspaceSession } from "../types/workspace";
import { useProjectSummary } from "../hooks/useProjectSummary";
import { writeChatComposerText } from "../lib/chat-drafts";
import { Button } from "./ui/button";
import { AppLink } from "./AppLink";

export function ProjectSummaryPanel({ onClose, orchestrator }: { onClose: () => void; orchestrator: WorkspaceSession }) {
	const navigate = useNavigate();
	const { t } = useTranslation();
	const summary = useProjectSummary(orchestrator.workspaceId, true);
	useEffect(() => { summary.refresh.mutate(); }, [orchestrator.workspaceId]);
	const data = summary.data;
	const discuss = (question: string) => {
		writeChatComposerText({ sessionId: orchestrator.id, incarnation: orchestrator.createdAt ?? orchestrator.id }, question);
		onClose();
		window.setTimeout(() => (document.querySelector('[role="combobox"]') as HTMLElement | null)?.focus(), 0);
	};
	return (
		<aside aria-label={t("projectSummary.title")} className="absolute inset-0 z-overlay flex min-h-0 flex-col border-l border-border bg-background sm:relative sm:inset-auto sm:z-auto sm:w-[380px] sm:shrink-0" data-testid="project-summary-panel">
			<header className="flex h-inspector-tabs shrink-0 items-center gap-2 border-b border-border px-3">
				<div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{t("projectSummary.title")}</p><p className="truncate text-[11px] text-passive">{orchestrator.workspaceName}</p></div>
				<Button aria-label={t("projectSummary.refresh")} disabled={summary.refresh.isPending} onClick={() => summary.refresh.mutate()} size="icon" variant="ghost"><RefreshCw className={summary.refresh.isPending ? "animate-spin" : ""} /></Button>
				<Button aria-label={t("projectSummary.close")} onClick={onClose} size="icon" variant="ghost"><X /></Button>
			</header>
			<div className="min-h-0 flex-1 overflow-y-auto p-4">
				{summary.isLoading ? <p className="text-sm text-passive">{t("projectSummary.loading")}</p> : summary.isError || !data ? <p role="alert" className="text-sm text-destructive">{t("projectSummary.loadFailed")}</p> : <>
					{data.generationError ? <p role="alert" className="mb-3 rounded-md border border-destructive/30 bg-destructive/5 p-2 text-xs text-destructive">{t("projectSummary.updateFailed")} {data.generationError}</p> : null}
					<p className="text-[15px] leading-6 text-foreground">{data.narrative}</p>
					<p className="mt-2 text-[11px] text-passive">{t("projectSummary.updated", { date: new Date(data.generatedAt).toLocaleString() })}</p>
					<div className="mt-5 grid grid-cols-3 gap-2" aria-label={t("projectSummary.counts")}>
						<Count label={t("projectSummary.active")} value={data.activeWorkers} /><Count label={t("projectSummary.complete")} value={data.completedWorkers} /><Count label={t("projectSummary.needsInput")} value={data.needsAttention.length} />
					</div>
					{data.needsAttention.length > 0 ? <section className="mt-6"><h2 className="text-xs font-semibold">{t("projectSummary.attention")}</h2><div className="mt-2 space-y-2">{data.needsAttention.map((item) => <article className="rounded-lg border border-warning/35 bg-warning/5 p-3" key={item.sessionId}><button className="text-left text-xs font-medium hover:underline" onClick={() => void navigate({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: orchestrator.workspaceId, sessionId: item.sessionId } })}>{item.sessionName}</button><p className="mt-1 text-xs leading-5 text-muted-foreground">{item.question}</p><Button className="mt-2" onClick={() => discuss(t("projectSummary.discussPrompt", { name: item.sessionName, question: item.question }))} size="sm" variant="outline"><MessageSquareText />{t("projectSummary.discuss")}</Button></article>)}</div></section> : null}
					{data.outputs.length > 0 ? <section className="mt-6"><h2 className="text-xs font-semibold">{t("projectSummary.outputs")}</h2><div className="mt-2 space-y-1">{data.outputs.map((output) => output.url ? <AppLink className="flex items-center gap-2 rounded-md px-2 py-2 text-xs hover:bg-surface" href={output.url} key={output.url} rel="noreferrer"><span className="min-w-0 flex-1"><span className="block truncate font-medium">{t("projectSummary.pullRequest", { number: output.number })}</span><span className="block truncate text-passive">{output.sessionName}, {output.state}</span></span><ArrowUpRight className="size-3.5 shrink-0" /></AppLink> : <div className="rounded-md px-2 py-2 text-xs" key={`${output.sessionId}:${output.kind}:${output.reference}`}><span className="block truncate font-medium">{output.label || output.kind}</span><span className="block truncate text-passive">{output.reference}</span></div>)}</div></section> : null}
				</>}
			</div>
		</aside>
	);
}

function Count({ label, value }: { label: string; value: number }) { return <div className="rounded-md bg-surface px-2 py-2"><p className="font-mono text-lg font-semibold tabular-nums">{value}</p><p className="text-[10px] text-passive">{label}</p></div>; }
