import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { useWorkflowPlan, useConfirmPlan, useStartPlan, useCompletePlan, useCancelPlan } from "../../hooks/useWorkflowPlans";
import { StatusBadge } from "./StatusBadge";
import { StageKanban } from "./StageKanban";
import { TaskDetailPanel } from "./TaskDetailPanel";
import { useWorkflowStore } from "../../stores/workflow-store";

type PlanDetailViewProps = {
	projectId: string;
	planId: string;
};

export function PlanDetailView({ projectId, planId }: PlanDetailViewProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const planQuery = useWorkflowPlan(planId);
	const confirmPlan = useConfirmPlan();
	const startPlan = useStartPlan();
	const completePlan = useCompletePlan();
	const cancelPlan = useCancelPlan();
	const { selectedTaskId, isTaskDetailOpen, closeTaskDetail } = useWorkflowStore();

	const plan = planQuery.data;

	const handleAction = (action: "confirm" | "start" | "complete" | "cancel") => {
		const mutations = { confirm: confirmPlan, start: startPlan, complete: completePlan, cancel: cancelPlan };
		mutations[action].mutate(planId, {
			onError: () => {
				void planQuery.refetch();
			},
		});
	};

	if (planQuery.isLoading) {
		return (
			<div className="flex flex-col gap-4 p-4">
				<Skeleton className="h-8 w-64" />
				<Skeleton className="h-24 w-full" />
				<div className="flex gap-3">
					<Skeleton className="h-64 w-72" />
					<Skeleton className="h-64 w-72" />
				</div>
			</div>
		);
	}

	if (planQuery.isError || !plan) {
		return (
			<div className="flex flex-col items-center gap-2 py-12 text-muted-foreground">
				<p>{t("workflow.error.planNotFound")}</p>
				<Button variant="outline" size="sm" onClick={() => void navigate({ to: "/projects/$projectId/workflow", params: { projectId } })}>
					{t("workflow.backToPlans")}
				</Button>
			</div>
		);
	}

	return (
		<div className="flex h-full">
			<div className="flex flex-1 flex-col gap-4 overflow-auto p-4">
				<div className="flex items-center gap-2">
					<Button
						variant="ghost"
						size="sm"
						onClick={() => void navigate({ to: "/projects/$projectId/workflow", params: { projectId } })}
					>
						<ArrowLeft className="mr-1 h-4 w-4" />
						{t("workflow.backToPlans")}
					</Button>
				</div>

				<div className="flex items-start justify-between gap-4">
					<div className="flex flex-col gap-1">
						<div className="flex items-center gap-2">
							<h1 className="text-lg font-semibold">{plan.title}</h1>
							<StatusBadge status={plan.status} entity="plan" />
						</div>
						{plan.objective && <p className="text-sm text-muted-foreground">{plan.objective}</p>}
					</div>
					<div className="flex items-center gap-2">
						{plan.status === "draft" && (
							<Button size="sm" onClick={() => handleAction("confirm")} disabled={confirmPlan.isPending} data-testid="plan-confirm">
								{t("workflow.plan.confirm")}
							</Button>
						)}
						{plan.status === "confirmed" && (
							<Button size="sm" onClick={() => handleAction("start")} disabled={startPlan.isPending} data-testid="plan-start">
								{t("workflow.plan.start")}
							</Button>
						)}
						{plan.status === "in_progress" && (
							<Button size="sm" onClick={() => handleAction("complete")} disabled={completePlan.isPending} data-testid="plan-complete">
								{t("workflow.plan.complete")}
							</Button>
						)}
						{(plan.status === "draft" || plan.status === "confirmed" || plan.status === "in_progress") && (
							<Button variant="outline" size="sm" onClick={() => handleAction("cancel")} disabled={cancelPlan.isPending} data-testid="plan-cancel">
								{t("workflow.plan.cancel")}
							</Button>
						)}
					</div>
				</div>

				{plan.requirements && (
					<div className="rounded-md border p-3">
						<h3 className="mb-1 text-xs font-medium text-muted-foreground">{t("workflow.plan.requirements")}</h3>
						<p className="text-sm whitespace-pre-wrap">{plan.requirements}</p>
					</div>
				)}

				{plan.implementationSummary && (
					<div className="rounded-md border p-3">
						<h3 className="mb-1 text-xs font-medium text-muted-foreground">{t("workflow.plan.implementationSummary")}</h3>
						<p className="text-sm whitespace-pre-wrap">{plan.implementationSummary}</p>
					</div>
				)}

				<StageKanban planId={planId} planStatus={plan.status} />
			</div>

			{isTaskDetailOpen && selectedTaskId && (
				<TaskDetailPanel taskId={selectedTaskId} onClose={closeTaskDetail} />
			)}
		</div>
	);
}
