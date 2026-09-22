import type { ReactElement } from "react";
import { useTranslation } from "react-i18next";
import type { WorkspaceSession } from "../types/workspace";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";

export function SessionTerminationPopover({
	onConfirm,
	onOpenChange,
	open,
	session,
	trigger,
}: {
	onConfirm: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	session?: WorkspaceSession;
	trigger: ReactElement;
}) {
	const { t } = useTranslation();
	const title = session?.title;
	// A cloud session's teardown is restorable — the control plane keeps its
	// conversation and work so it can be re-provisioned later.
	const isCloud = session?.cloud !== undefined;
	const body = isCloud
		? title
			? t("termination.bodyCloudNamed", { title })
			: t("termination.bodyCloud")
		: title
			? t("termination.bodyNamed", { title })
			: t("termination.body");
	return (
		<Popover onOpenChange={onOpenChange} open={open}>
			<PopoverTrigger asChild>{trigger}</PopoverTrigger>
			<PopoverContent
				align="end"
				aria-label={title ? t("termination.dialogNamed", { title }) : t("termination.dialog")}
				className="w-64 max-w-[calc(100vw-1rem)] p-3 shadow-lg"
				collisionPadding={8}
				onClick={(event) => event.stopPropagation()}
				role="dialog"
				side="bottom"
				sideOffset={6}
			>
				<p className="text-control font-semibold text-foreground">{t("termination.dialog")}</p>
				<p className="mt-1 text-caption leading-4 text-muted-foreground">{body}</p>
				<div className="mt-3 flex justify-end gap-1.5">
					<button
						className="h-control-md rounded-md px-2.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground"
						onClick={() => onOpenChange(false)}
						type="button"
					>
						{t("confirm.cancel")}
					</button>
					<button
						className="h-control-md rounded-md bg-destructive-strong px-2.5 text-xs font-semibold text-white transition-[filter] hover:brightness-110"
						onClick={onConfirm}
						type="button"
					>
						{t("shell.terminateSession")}
					</button>
				</div>
			</PopoverContent>
		</Popover>
	);
}
