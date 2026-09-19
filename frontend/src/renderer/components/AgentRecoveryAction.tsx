import { useTranslation } from "react-i18next";
import type { AgentRecoveryActionKind } from "../lib/agent-recovery";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";

const ACTION_KEYS = {
	install: "agentRecovery.installAction",
	login: "agentRecovery.loginAction",
	review: "agentRecovery.reviewAction",
	configure: "agentRecovery.configureAction",
} as const;

const DESCRIPTION_KEYS = {
	install: "agentRecovery.installDescription",
	login: "agentRecovery.loginDescription",
	review: "agentRecovery.reviewDescription",
	configure: "agentRecovery.configureDescription",
} as const;

export type AgentRecoveryActionProps = {
	action: AgentRecoveryActionKind | null;
	agentLabel: string;
	className?: string;
	disabled?: boolean;
	onAction: (action: AgentRecoveryActionKind) => void;
	variant?: "explanatory" | "compact";
};

export function AgentRecoveryAction({
	action,
	agentLabel,
	className,
	disabled = false,
	onAction,
	variant = "explanatory",
}: AgentRecoveryActionProps) {
	const { t } = useTranslation();
	if (!action) return null;

	const label = t(ACTION_KEYS[action]);
	const button = (
		<Button
			type="button"
			size="sm"
			variant="outline"
			disabled={disabled}
			onClick={() => onAction(action)}
			className={variant === "compact" ? className : undefined}
		>
			{label}
		</Button>
	);
	if (variant === "compact") return button;

	return (
		<div
			className={cn(
				"flex items-center justify-between gap-3 rounded-md border border-border bg-muted/30 px-3 py-2",
				className,
			)}
		>
			<p className="min-w-0 text-xs text-muted-foreground">
				{t(DESCRIPTION_KEYS[action], { agent: agentLabel })}
			</p>
			{button}
		</div>
	);
}
