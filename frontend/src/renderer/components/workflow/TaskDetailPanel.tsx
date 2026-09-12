import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "../ui/button";
import { Separator } from "../ui/separator";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { Skeleton } from "../ui/skeleton";
import { useWorkflowTask } from "../../hooks/useWorkflowTasks";
import { StatusBadge } from "./StatusBadge";
import { RunEntry } from "./RunEntry";
import { useWorkflowRuns } from "../../hooks/useWorkflowRuns";

type TaskDetailPanelProps = {
	taskId: string;
	onClose: () => void;
};

export function TaskDetailPanel({ taskId, onClose }: TaskDetailPanelProps) {
	const { t } = useTranslation();
	const taskQuery = useWorkflowTask(taskId);
	const runsQuery = useWorkflowRuns(taskId);
	const task = taskQuery.data;
	const runs = (runsQuery.data ?? []).sort((a, b) => b.attempt - a.attempt);

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
						) : runs.length === 0 ? (
							<p className="py-4 text-center text-xs text-muted-foreground">{t("workflow.empty.noRuns")}</p>
						) : (
							<div className="flex flex-col gap-2">
								{runs.map((run) => (
									<RunEntry key={run.id} run={run} />
								))}
							</div>
						)}
					</TabsContent>
				</Tabs>
			)}
		</div>
	);
}
