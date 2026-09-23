import { ChevronLeft, ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { ConversationBranchPoint } from "../../types/conversation";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

export function ConversationBranchNavigator({
	point,
	pending,
	error,
	onActivate,
}: {
	point: ConversationBranchPoint;
	pending: boolean;
	error?: string;
	onActivate: (branchId: string) => Promise<unknown> | void;
}) {
	const { t } = useTranslation();
	if (point.total <= 1) return null;
	return (
		<div className="flex min-w-0 items-center gap-0.5 text-[10.5px] text-muted-foreground">
		{point.previousBranchId ? (
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">
						<button
							type="button"
							disabled={pending}
							onClick={() => {
								void Promise.resolve(onActivate(point.previousBranchId as string)).catch(() => {});
							}}
							aria-label={t("chat.branch.previous")}
							className="flex size-7 items-center justify-center rounded-md transition-[background-color,color,transform] hover:bg-interactive-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-logo-accent/40 disabled:opacity-45"
						>
							<ChevronLeft aria-hidden="true" className="size-3.5" />
						</button>
					</span>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("chat.branch.previous")}</TooltipContent>
			</Tooltip>
		) : null}
		<span
			className="px-0.5 tabular-nums"
			aria-label={t("chat.branch.position", { position: point.position, total: point.total })}
		>
			{point.position} / {point.total}
		</span>
		{point.nextBranchId ? (
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">
						<button
							type="button"
							disabled={pending}
							onClick={() => {
								void Promise.resolve(onActivate(point.nextBranchId as string)).catch(() => {});
							}}
							aria-label={t("chat.branch.next")}
							className="flex size-7 items-center justify-center rounded-md transition-[background-color,color,transform] hover:bg-interactive-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-logo-accent/40 disabled:opacity-45"
						>
							<ChevronRight aria-hidden="true" className="size-3.5" />
						</button>
					</span>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("chat.branch.next")}</TooltipContent>
			</Tooltip>
		) : null}
		{error ? <span role="alert" className="ml-1 truncate text-destructive">{error}</span> : null}
	</div>
	);
}
