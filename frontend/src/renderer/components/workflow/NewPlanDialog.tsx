import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { useCreatePlan } from "../../hooks/useWorkflowPlans";

type NewPlanDialogProps = {
	projectId: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
};

export function NewPlanDialog({ projectId, open, onOpenChange }: NewPlanDialogProps) {
	const { t } = useTranslation();
	const createPlan = useCreatePlan();
	const [title, setTitle] = useState("");
	const [objective, setObjective] = useState("");
	const [requirements, setRequirements] = useState("");
	const [implementationSummary, setImplementationSummary] = useState("");

	const resetForm = () => {
		setTitle("");
		setObjective("");
		setRequirements("");
		setImplementationSummary("");
	};

	const handleSubmit = () => {
		if (!title.trim()) return;
		createPlan.mutate(
			{
				projectId,
				title: title.trim(),
				objective: objective.trim() || undefined,
				requirements: requirements.trim() || undefined,
				implementationSummary: implementationSummary.trim() || undefined,
			},
			{
				onSuccess: () => {
					resetForm();
					onOpenChange(false);
				},
			},
		);
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) resetForm();
				onOpenChange(nextOpen);
			}}
		>
			<DialogContent data-testid="new-plan-dialog">
				<DialogHeader>
					<DialogTitle>{t("workflow.createPlan")}</DialogTitle>
				</DialogHeader>
				<div className="flex flex-col gap-3">
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="plan-title">{t("workflow.plan.title")} *</Label>
						<Input
							id="plan-title"
							value={title}
							onChange={(e) => setTitle(e.target.value)}
							placeholder={t("workflow.plan.titlePlaceholder")}
							autoFocus
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="plan-objective">{t("workflow.plan.objective")}</Label>
						<Input
							id="plan-objective"
							value={objective}
							onChange={(e) => setObjective(e.target.value)}
							placeholder={t("workflow.plan.objectivePlaceholder")}
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="plan-requirements">{t("workflow.plan.requirements")}</Label>
						<Input
							id="plan-requirements"
							value={requirements}
							onChange={(e) => setRequirements(e.target.value)}
							placeholder={t("workflow.plan.requirementsPlaceholder")}
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="plan-summary">{t("workflow.plan.implementationSummary")}</Label>
						<Input
							id="plan-summary"
							value={implementationSummary}
							onChange={(e) => setImplementationSummary(e.target.value)}
							placeholder={t("workflow.plan.summaryPlaceholder")}
						/>
					</div>
				</div>
				{createPlan.isError && (
					<p className="text-sm text-destructive">{createPlan.error?.message}</p>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						{t("common.close")}
					</Button>
					<Button
						onClick={handleSubmit}
						disabled={!title.trim() || createPlan.isPending}
						data-testid="submit-new-plan"
					>
						{createPlan.isPending ? t("common.creating") : t("workflow.createPlan")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
