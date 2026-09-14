import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { ImportSessionList } from "./ImportSessionList";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
} from "./ui/dialog";

type ImportSessionDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	container?: HTMLElement;
	// projectId scopes the dialog to one project's history.
	projectId: string;
	projectName?: string;
};

// ImportSessionDialog lists agent conversations already on disk (Claude Code,
// Codex, and any future provider) and imports one as a resumable AO session.
export function ImportSessionDialog({
	open,
	onOpenChange,
	container,
	projectId,
	projectName,
}: ImportSessionDialogProps) {
	const { t } = useTranslation();

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent
				portalContainer={container}
				showCloseButton={false}
				className="w-[min(var(--size-dialog-lg,42rem),calc(100%-var(--space-8)))] max-w-none gap-0 overflow-hidden rounded-xl border border-border-strong bg-surface p-0 text-foreground shadow-xl"
			>
				<DialogClose asChild>
					<button
						aria-label={t("importSession.close")}
						className="settings-dialog-close-button settings-close-button"
						type="button"
					>
						<X className="size-icon-base" aria-hidden="true" />
					</button>
				</DialogClose>
				<DialogTitle className="settings-dialog-title px-5 pr-12 pt-4">
					{t("importSession.title")}
					{projectName ? ` — ${projectName}` : ""}
				</DialogTitle>
				<DialogDescription className="px-5 pr-12 pt-1 text-caption leading-4 text-muted-foreground">
					{t("importSession.descriptionProject")}
				</DialogDescription>

				<div className="max-h-[min(60vh,32rem)] overflow-y-auto px-5 pb-5 pt-4">
					<ImportSessionList active={open} projectId={projectId} />
				</div>
			</DialogContent>
		</Dialog>
	);
}
