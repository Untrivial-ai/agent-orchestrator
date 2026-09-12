import { useTranslation } from "react-i18next";
import { Badge } from "../ui/badge";

type EntityType = "plan" | "stage" | "task" | "run";

type StatusBadgeProps = {
	status: string;
	entity: EntityType;
};

const statusVariantMap: Record<string, "neutral" | "accent" | "success" | "warning" | "error"> = {
	draft: "neutral",
	confirmed: "accent",
	in_progress: "accent",
	completed: "success",
	cancelled: "error",
	pending: "neutral",
	ready: "accent",
	ready_for_approval: "warning",
	passed: "success",
	blocked: "warning",
	running: "accent",
	review: "warning",
	succeeded: "success",
	failed: "error",
};

export function StatusBadge({ status, entity }: StatusBadgeProps) {
	const { t } = useTranslation();
	const key = `status.${entity}.${status}`;
	const label = t(key, { defaultValue: status.replace(/_/g, " ") });
	const variant = statusVariantMap[status] ?? "neutral";

	return <Badge variant={variant}>{label}</Badge>;
}
