import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { StatusBadge } from "./StatusBadge";
import { TaskCard } from "./TaskCard";
import { useWorkflowTasks } from "../../hooks/useWorkflowTasks";
import {
	useStartStage,
	useReadyForApprovalStage,
	usePassStage,
	useBlockStage,
	useUnblockStage,
	useCancelStage,
} from "../../hooks/useWorkflowStages";

type StageView = components["schemas"]["StageView"];

type StageColumnProps = {
	stage: StageView;
	planId: string;
	planStatus: string;
};

export function StageColumn({ stage, planId, planStatus }: StageColumnProps) {
	const { t } = useTranslation();
	const tasksQuery = useWorkflowTasks(stage.id);
	const tasks = (tasksQuery.data ?? []).sort((a, b) => a.sequence - b.sequence);

	const startStage = useStartStage();
	const readyStage = useReadyForApprovalStage();
	const passStage = usePassStage();
	const blockStage = useBlockStage();
	const unblockStage = useUnblockStage();
	const cancelStage = useCancelStage();

	const isLoading =
		startStage.isPending ||
		readyStage.isPending ||
		passStage.isPending ||
		blockStage.isPending ||
		unblockStage.isPending ||
		cancelStage.isPending;

	const handleAction = (action: "start" | "ready" | "pass" | "block" | "unblock" | "cancel") => {
		const mutations = {
			start: startStage,
			ready: readyStage,
			pass: passStage,
			block: blockStage,
			unblock: unblockStage,
			cancel: cancelStage,
		};
		mutations[action].mutate(
			{ stageId: stage.id, planId },
			{ onError: () => void tasksQuery.refetch() },
		);
	};

	const canOperate = planStatus === "in_progress";

	return (
		<Card className="w-72 shrink-0" data-testid={`stage-column-${stage.id}`}>
			<CardHeader>
				<div className="flex items-start justify-between gap-1">
					<CardTitle className="line-clamp-1 text-xs">{stage.title}</CardTitle>
					<StatusBadge status={stage.status} entity="stage" />
				</div>
				{stage.description && (
					<p className="line-clamp-2 text-xs text-muted-foreground">{stage.description}</p>
				)}
			</CardHeader>
			<CardContent className="flex flex-col gap-2">
				{canOperate && (
					<div className="flex flex-wrap gap-1">
						{stage.status === "pending" && (
							<Button size="sm" variant="outline" onClick={() => handleAction("start")} disabled={isLoading}>
								{t("workflow.stage.start")}
							</Button>
						)}
						{stage.status === "in_progress" && (
							<>
								<Button size="sm" variant="outline" onClick={() => handleAction("ready")} disabled={isLoading}>
									{t("workflow.stage.readyForApproval")}
								</Button>
								<Button size="sm" variant="outline" onClick={() => handleAction("block")} disabled={isLoading}>
									{t("workflow.stage.block")}
								</Button>
							</>
						)}
						{stage.status === "ready_for_approval" && (
							<Button size="sm" onClick={() => handleAction("pass")} disabled={isLoading}>
								{t("workflow.stage.pass")}
							</Button>
						)}
						{stage.status === "blocked" && (
							<Button size="sm" variant="outline" onClick={() => handleAction("unblock")} disabled={isLoading}>
								{t("workflow.stage.unblock")}
							</Button>
						)}
						{(stage.status === "pending" || stage.status === "in_progress" || stage.status === "blocked") && (
							<Button size="sm" variant="ghost" onClick={() => handleAction("cancel")} disabled={isLoading}>
								{t("workflow.stage.cancel")}
							</Button>
						)}
					</div>
				)}

				<div className="flex flex-col gap-2">
					{tasksQuery.isLoading ? (
						<>
							<Skeleton className="h-20 rounded-md" />
							<Skeleton className="h-20 rounded-md" />
						</>
					) : tasksQuery.isError ? (
						<p className="py-4 text-center text-xs text-destructive">{t("workflow.error.tasksLoadFailed")}</p>
					) : tasks.length === 0 ? (
						<p className="py-4 text-center text-xs text-muted-foreground">{t("workflow.empty.noTasks")}</p>
					) : (
						tasks.map((task) => <TaskCard key={task.id} task={task} />)
					)}
				</div>
			</CardContent>
		</Card>
	);
}
