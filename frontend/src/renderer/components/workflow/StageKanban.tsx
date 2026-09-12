import { useTranslation } from "react-i18next";
import { Skeleton } from "../ui/skeleton";
import { useWorkflowStages } from "../../hooks/useWorkflowStages";
import { StageColumn } from "./StageColumn";

type StageKanbanProps = {
	planId: string;
	planStatus: string;
};

export function StageKanban({ planId, planStatus }: StageKanbanProps) {
	const { t } = useTranslation();
	const stagesQuery = useWorkflowStages(planId);
	const stages = (stagesQuery.data ?? []).sort((a, b) => a.sequence - b.sequence);

	if (stagesQuery.isLoading) {
		return (
			<div className="flex gap-3 overflow-x-auto pb-2">
				{Array.from({ length: 3 }).map((_, i) => (
					<Skeleton key={i} className="h-64 w-72 shrink-0 rounded-lg" />
				))}
			</div>
		);
	}

	if (stagesQuery.isError) {
		return (
			<div className="flex flex-col items-center gap-2 py-8 text-muted-foreground">
				<p>{t("workflow.error.stagesLoadFailed")}</p>
			</div>
		);
	}

	if (stages.length === 0) {
		return (
			<div className="flex flex-col items-center gap-2 py-8 text-muted-foreground">
				<p className="text-sm">{t("workflow.empty.noStages")}</p>
			</div>
		);
	}

	return (
		<div className="flex gap-3 overflow-x-auto pb-2" data-testid="stage-kanban">
			{stages.map((stage) => (
				<StageColumn key={stage.id} stage={stage} planId={planId} planStatus={planStatus} />
			))}
		</div>
	);
}
