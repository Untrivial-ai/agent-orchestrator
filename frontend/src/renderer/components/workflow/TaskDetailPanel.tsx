import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "../ui/button";
import { Separator } from "../ui/separator";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { Skeleton } from "../ui/skeleton";
import { useWorkflowTask } from "../../hooks/useWorkflowTasks";
import { StatusBadge } from "./StatusBadge";
import { RunEntry } from "./RunEntry";
import { ReviewTab } from "./ReviewTab";
import { useWorkflowRuns, useCreateRun, useStartRun, useCancelRun } from "../../hooks/useWorkflowRuns";

type TaskDetailPanelProps = {
	taskId: string;
	projectId?: string;
	onClose: () => void;
	onNavigateSession?: (sessionId: string) => void;
};

export function TaskDetailPanel({ taskId, projectId, onClose, onNavigateSession }: TaskDetailPanelProps) {
	const { t } = useTranslation();
	const taskQuery = useWorkflowTask(taskId);
	const runsQuery = useWorkflowRuns(taskId);
	const createRun = useCreateRun(taskId);
	const startRun = useStartRun(taskId);
	const cancelRun = useCancelRun(taskId);
	const task = taskQuery.data;
	const runs = (runsQuery.data ?? []).sort((a, b) => b.attempt - a.attempt);
	const latestRun = runs[0] ?? null;
	const hasActiveRun = runs.some((r) => r.status === "pending" || r.status === "running");
	const isTransientRecovery = task?.status === "running" && latestRun?.status === "failed";

	function handleViewSession(sessionId: string) {
		if (projectId && onNavigateSession) {
			onNavigateSession(sessionId);
		}
	}

	return (
		<div
			className="flex w-96 shrink-0 flex-col border-l bg-card"
			data-testid="task-detail-panel"
		>
			<div className="flex items-center justify-between border-b px-4 py-3">
				<h2 className="text-sm font-semibold">{t("workflow.task.detail")}</h2>
				<Button variant="ghost" size="sm" onClick={onClose} data-testid="close-task-detail">
					<X className="h-4 w-4" />
				</Button>
			</div>

			{taskQuery.isLoading ? (
				<div className="flex flex-col gap-3 p-4">
					<Skeleton className="h-5 w-48" />
					<Skeleton className="h-16 w-full" />
				</div>
			) : taskQuery.isError || !task ? (
				<div className="p-4 text-sm text-muted-foreground">{t("workflow.error.taskNotFound")}</div>
			) : (
				<Tabs defaultValue="info" className="flex flex-1 flex-col overflow-hidden">
					<TabsList className="mx-4 mt-2">
						<TabsTrigger value="info">{t("workflow.task.info")}</TabsTrigger>
						<TabsTrigger value="runs">{t("workflow.task.runHistory")}</TabsTrigger>
						<TabsTrigger value="review">{t("workflow.task.review")}</TabsTrigger>
					</TabsList>

					<TabsContent value="info" className="flex-1 overflow-auto p-4">
						<div className="flex flex-col gap-3">
							<div>
								<h3 className="text-sm font-medium">{task.title}</h3>
								<StatusBadge status={task.status} entity="task" />
							</div>
							<Separator />
							{task.description && (
								<div>
									<p className="text-xs font-medium text-muted-foreground">{t("workflow.task.description")}</p>
									<p className="mt-1 text-sm whitespace-pre-wrap">{task.description}</p>
								</div>
							)}
							{task.acceptanceCriteria && (
								<div>
									<p className="text-xs font-medium text-muted-foreground">{t("workflow.task.acceptanceCriteria")}</p>
									<p className="mt-1 text-sm whitespace-pre-wrap">{task.acceptanceCriteria}</p>
								</div>
							)}
							<div className="grid grid-cols-2 gap-2 text-xs">
								<div>
									<p className="text-muted-foreground">{t("workflow.task.type")}</p>
									<p>{task.taskType || "—"}</p>
								</div>
								<div>
									<p className="text-muted-foreground">{t("workflow.task.role")}</p>
									<p>{task.agentRoleId || "—"}</p>
								</div>
								<div>
									<p className="text-muted-foreground">{t("workflow.task.provider")}</p>
									<p>{task.providerId || "—"}</p>
								</div>
								<div>
									<p className="text-muted-foreground">{t("workflow.task.model")}</p>
									<p>{task.providerModelId || "—"}</p>
								</div>
							</div>
						</div>
					</TabsContent>

					<TabsContent value="runs" className="flex-1 overflow-auto p-4">
						{runsQuery.isLoading ? (
							<div className="flex flex-col gap-2">
								<Skeleton className="h-16 w-full" />
								<Skeleton className="h-16 w-full" />
							</div>
						) : (
							<div className="flex flex-col gap-2">
								{task.status === "ready" && !hasActiveRun && (
									<Button
										size="sm"
										onClick={() => createRun.mutate()}
										disabled={createRun.isPending}
										data-testid="create-run"
									>
										{t("workflow.run.create")}
									</Button>
								)}

								{createRun.isError && (
									<p className="text-xs text-destructive">{t("workflow.run.createFailed")}</p>
								)}

								{isTransientRecovery && (
									<p className="text-xs text-muted-foreground">{t("workflow.run.recovering")}</p>
								)}

								{runs.length === 0 && task.status !== "ready" ? (
									<p className="py-4 text-center text-xs text-muted-foreground">{t("workflow.empty.noRuns")}</p>
								) : runs.length === 0 ? null : (
									runs.map((run, i) => (
										<RunEntry
											key={run.id}
											run={run}
											isLatest={i === 0}
											onStart={i === 0 && run.status === "pending" ? () => startRun.mutate(run.id) : undefined}
											onCancel={i === 0 && (run.status === "pending" || run.status === "running") ? () => cancelRun.mutate(run.id) : undefined}
											onViewSession={i === 0 && run.status === "running" && run.sessionId ? handleViewSession : undefined}
											startPending={i === 0 && startRun.isPending}
											cancelPending={i === 0 && cancelRun.isPending}
										/>
									))
								)}

								{task.status === "review" && (
									<p className="text-xs text-muted-foreground">{t("workflow.run.waitingReview")}</p>
								)}
							</div>
						)}
					</TabsContent>

					<TabsContent value="review" className="flex-1 overflow-auto p-4">
						<ReviewTab task={task} latestRun={latestRun} taskId={taskId} />
					</TabsContent>
				</Tabs>
			)}
		</div>
	);
}
