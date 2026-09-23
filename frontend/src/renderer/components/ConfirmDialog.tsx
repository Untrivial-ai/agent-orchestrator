import { useEffect, useRef } from "react";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogOverlay,
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
	/** Screen-reader name for the confirm button when "Confirm" alone is vague. */
	confirmAriaLabel?: string;
	/** Defaults to "Cancel"; reversible actions can soften it to "No". */
	cancelLabel?: string;
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
	confirmAriaLabel,
	cancelLabel,
	destructive,
	busy,
	error,
	onConfirm,
	onOpenChange,
}: ConfirmDialogProps) {
	const { t } = useTranslation();
	// Radix aims its close-time focus at a DialogTrigger, but these confirms are
	// opened programmatically, so that ref is null and the keyboard user is
	// dropped on <body> — several tab stops away from the control they came
	// from. Remember whatever had focus when the dialog opened and put it back.
	const returnFocusRef = useRef<HTMLElement | null>(null);
	useEffect(() => {
		if (open) returnFocusRef.current = document.activeElement as HTMLElement | null;
	}, [open]);

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
				// React portals re-dispatch synthetic events up the React tree, not the
				// DOM tree, so a confirm rendered from inside a clickable row or card
				// would otherwise also trigger that ancestor's onClick (opening the very
				// session being confirmed). The modal — dimmed backdrop included — owns
				// its own clicks. Radix still dismisses on the native pointerdown.
				onClick={(event) => event.stopPropagation()}
				onCloseAutoFocus={(event) => {
					const target = returnFocusRef.current;
					// A confirmed action often removes its own trigger (the archived row
					// disappears); fall back to Radix's handling when it is gone.
					if (!target?.isConnected) return;
					event.preventDefault();
					target.focus();
				}}
				overlay={<DialogOverlay onClick={(event) => event.stopPropagation()} />}
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
							{cancelLabel ?? t("confirm.cancel")}
						</Button>
					</DialogClose>
					<Button
						aria-label={confirmAriaLabel}
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
