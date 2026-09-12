import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { StatusBadge } from "./StatusBadge";

type PlanView = components["schemas"]["PlanView"];

type PlanCardProps = {
	plan: PlanView;
	onClick: () => void;
};

export function PlanCard({ plan, onClick }: PlanCardProps) {
	const { t } = useTranslation();

	return (
		<Card
			className="cursor-pointer transition-colors hover:bg-accent/50"
			onClick={onClick}
			data-testid={`plan-card-${plan.id}`}
		>
			<CardHeader>
				<div className="flex items-start justify-between gap-2">
					<CardTitle className="line-clamp-1 text-sm">{plan.title}</CardTitle>
					<StatusBadge status={plan.status} entity="plan" />
				</div>
			</CardHeader>
			<CardContent>
				{plan.objective && (
					<p className="line-clamp-2 text-xs text-muted-foreground">{plan.objective}</p>
				)}
				<div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
					<span>{t("workflow.plan.createdAt")}: {new Date(plan.createdAt).toLocaleDateString()}</span>
				</div>
			</CardContent>
		</Card>
	);
}
