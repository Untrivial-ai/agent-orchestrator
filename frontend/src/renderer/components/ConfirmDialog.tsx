import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

type ConfirmDialogProps = {
	open: boolean;
	title: string;
	description: React.ReactNode;
	confirmLabel: string;
	destructive?: boolean;
	busy?: boolean;
	error?: string | null;
	onConfirm: () => void;
	onOpenChange: (open: boolean) => void;
};

// Shared confirmation modal styled exactly like the settings dialogs — same
// frame, header typography, and footer buttons via the shared
// settingsDialog* class constants. Destructive confirms fill
// with the deep danger-strong token instead of the settings accent.
export function ConfirmDialog({
	open,
	title,
	description,
	confirmLabel,
	destructive,
	busy,
	error,
	onConfirm,
	onOpenChange,
}: ConfirmDialogProps) {
	const { t } = useTranslation();
	// Sized for a two-line prompt, not a settings form: the shared settings
	// frame (575px, 38px footer pills) reads oversized around one question, so
	// the confirm narrows the dialog and compacts the buttons while keeping the
	// settings family's colors, borders, and typography.
	const compactButtonClass = "h-8 rounded-[10px] px-4 text-sm";
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent
				showCloseButton={false}
				className={cn(settingsDialogContentClass, "w-[min(420px,calc(100vw-24px))]")}
			>
				<DialogClose asChild>
					<button
						type="button"
						disabled={busy}
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("confirm.close")}
						title={t("confirm.closeEsc")}
					>
						<X className="size-4" aria-hidden="true" />
					</button>
				</DialogClose>

				<div className={cn(settingsDialogHeaderClass, "p-5 pr-12")}>
					<DialogTitle className="settings-dialog-title text-base">{title}</DialogTitle>
					<DialogDescription asChild>
						<div className="text-control leading-5 text-settings-muted">{description}</div>
					</DialogDescription>
				</div>

				{error ? (
					<div className={cn(settingsDialogBodyClass, "p-5 py-3")}>
						<p role="alert" className="text-caption leading-4 text-error">
							{error}
						</p>
					</div>
				) : null}

				<div className={cn(settingsDialogFooterClass, "gap-2 p-4")}>
					<DialogClose asChild>
						<Button type="button" variant="footer" className={compactButtonClass} disabled={busy}>
							{t("confirm.cancel")}
						</Button>
					</DialogClose>
					<Button
						type="button"
						variant="footer-primary"
						className={cn(compactButtonClass, destructive && "bg-danger-strong hover:bg-danger-strong")}
						disabled={busy}
						onClick={onConfirm}
					>
						{confirmLabel}
					</Button>
				</div>
			</DialogContent>
		</Dialog>
	);
}
