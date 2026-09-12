import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { StatusBadge } from "./StatusBadge";
import { useWorkflowStore } from "../../stores/workflow-store";

type TaskView = components["schemas"]["TaskView"];

type TaskCardProps = {
	task: TaskView;
};

export function TaskCard({ task }: TaskCardProps) {
	const { t } = useTranslation();
	const openTaskDetail = useWorkflowStore((s) => s.openTaskDetail);

	return (
		<Card
			size="sm"
			className="cursor-pointer transition-colors hover:bg-accent/50"
			onClick={() => openTaskDetail(task.id)}
			data-testid={`task-card-${task.id}`}
		>
			<CardHeader>
				<div className="flex items-start justify-between gap-1">
					<CardTitle className="line-clamp-1 text-xs">{task.title}</CardTitle>
					<StatusBadge status={task.status} entity="task" />
				</div>
			</CardHeader>
			<CardContent>
				<div className="flex flex-col gap-1 text-xs text-muted-foreground">
					<span>{t("workflow.task.type")}: {task.taskType || "—"}</span>
					<span>{t("workflow.task.role")}: {task.agentRoleId || "—"}</span>
				</div>
			</CardContent>
		</Card>
	);
}
