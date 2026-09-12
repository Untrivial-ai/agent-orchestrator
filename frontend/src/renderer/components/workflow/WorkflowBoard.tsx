import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { Plus, Search } from "lucide-react";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Skeleton } from "../ui/skeleton";
import { useWorkflowPlans } from "../../hooks/useWorkflowPlans";
import { PlanCard } from "./PlanCard";
import { NewPlanDialog } from "./NewPlanDialog";

type WorkflowBoardProps = {
	projectId: string;
};

const planFilterOptions = ["all", "draft", "confirmed", "in_progress", "completed", "cancelled"] as const;

export function WorkflowBoard({ projectId }: WorkflowBoardProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const plansQuery = useWorkflowPlans(projectId);
	const [filter, setFilter] = useState<string>("all");
	const [search, setSearch] = useState("");
	const [isNewPlanOpen, setIsNewPlanOpen] = useState(false);

	const plans = plansQuery.data ?? [];
	const filtered = plans.filter((plan) => {
		if (filter !== "all" && plan.status !== filter) return false;
		if (search && !plan.title.toLowerCase().includes(search.toLowerCase())) return false;
		return true;
	});

	return (
		<div className="flex flex-col gap-4 p-4">
			<div className="flex items-center justify-between">
				<h1 className="text-lg font-semibold">{t("workflow.plans")}</h1>
				<Button onClick={() => setIsNewPlanOpen(true)} size="sm" data-testid="new-plan-button">
					<Plus className="mr-1 h-4 w-4" />
					{t("workflow.createPlan")}
				</Button>
			</div>

			<div className="flex items-center gap-2">
				<div className="relative flex-1">
					<Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
					<Input
						placeholder={t("workflow.searchPlans")}
						value={search}
						onChange={(e) => setSearch(e.target.value)}
						className="pl-8"
					/>
				</div>
				<Select value={filter} onValueChange={setFilter}>
					<SelectTrigger className="w-[160px]">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{planFilterOptions.map((opt) => (
							<SelectItem key={opt} value={opt}>
								{t(`workflow.filter.${opt}`)}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>

			{plansQuery.isLoading ? (
				<div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
					{Array.from({ length: 3 }).map((_, i) => (
						<Skeleton key={i} className="h-36 rounded-lg" />
					))}
				</div>
			) : plansQuery.isError ? (
				<div className="flex flex-col items-center gap-2 py-12 text-muted-foreground">
					<p>{t("workflow.error.loadFailed")}</p>
					<Button variant="outline" size="sm" onClick={() => void plansQuery.refetch()}>
						{t("common.retry")}
					</Button>
				</div>
			) : filtered.length === 0 ? (
				<div className="flex flex-col items-center gap-2 py-12 text-muted-foreground">
					<p className="text-sm">{plans.length === 0 ? t("workflow.empty.noPlans") : t("workflow.empty.noMatch")}</p>
					{plans.length === 0 && (
						<Button variant="outline" size="sm" onClick={() => setIsNewPlanOpen(true)}>
							<Plus className="mr-1 h-4 w-4" />
							{t("workflow.createPlan")}
						</Button>
					)}
				</div>
			) : (
				<div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
					{filtered.map((plan) => (
						<PlanCard
							key={plan.id}
							plan={plan}
							onClick={() =>
								void navigate({
									to: "/projects/$projectId/workflow/$planId",
									params: { projectId, planId: plan.id },
								})
							}
						/>
					))}
				</div>
			)}

			<NewPlanDialog
				projectId={projectId}
				open={isNewPlanOpen}
				onOpenChange={setIsNewPlanOpen}
			/>
		</div>
	);
}
