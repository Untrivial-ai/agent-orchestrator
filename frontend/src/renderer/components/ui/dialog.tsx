"use client";

import * as React from "react";
import { XIcon } from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

function Dialog({ ...props }: React.ComponentProps<typeof DialogPrimitive.Root>) {
	return <DialogPrimitive.Root data-slot="dialog" {...props} />;
}

function DialogTrigger({ ...props }: React.ComponentProps<typeof DialogPrimitive.Trigger>) {
	return <DialogPrimitive.Trigger data-slot="dialog-trigger" {...props} />;
}

function DialogPortal({ ...props }: React.ComponentProps<typeof DialogPrimitive.Portal>) {
	return <DialogPrimitive.Portal data-slot="dialog-portal" {...props} />;
}

function DialogClose({ ...props }: React.ComponentProps<typeof DialogPrimitive.Close>) {
	return <DialogPrimitive.Close data-slot="dialog-close" {...props} />;
}

function DialogOverlay({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Overlay>) {
	return (
		<DialogPrimitive.Overlay
			data-slot="dialog-overlay"
			className={cn(
				"dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out",
				className,
			)}
			{...props}
		/>
	);
}

function DialogContent({
	className,
	children,
	overlay,
	portalContainer,
	showCloseButton = true,
	...props
}: React.ComponentProps<typeof DialogPrimitive.Content> & {
	overlay?: React.ReactNode | false;
	portalContainer?: HTMLElement | null;
	showCloseButton?: boolean;
}) {
	const { t } = useTranslation();
	return (
		<DialogPortal container={portalContainer}>
			{overlay === undefined ? <DialogOverlay /> : overlay}
			<DialogPrimitive.Content
				data-slot="dialog-content"
				className={cn(
					"fixed left-[50%] top-[50%] z-50 w-full max-w-lg translate-x-[-50%] translate-y-[-50%] flex flex-col gap-4 bg-background shadow-lg data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none border p-4 sm:rounded-lg",
					className,
				)}
				{...props}
			>
				{children}
				{showCloseButton && (
					<DialogPrimitive.Close className="absolute top-4 right-4 rounded-xs opacity-70 transition-opacity hover:opacity-100 focus:outline-hidden disabled:pointer-events-none data-[state=open]:bg-secondary">
						<XIcon className="size-4" />
						<span className="sr-only">{t("common.close")}</span>
					</DialogPrimitive.Close>
				)}
			</DialogPrimitive.Content>
		</DialogPortal>
	);
}

function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
	return <div data-slot="dialog-header" className={cn("flex flex-col gap-1.5 p-0", className)} {...props} />;
}

function DialogFooter({ className, ...props }: React.ComponentProps<"div">) {
	return <div data-slot="dialog-footer" className={cn("mt-auto flex flex-col gap-2 p-0", className)} {...props} />;
}

function DialogTitle({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Title>) {
	return (
		<DialogPrimitive.Title
			data-slot="dialog-title"
			className={cn("font-semibold text-foreground", className)}
			{...props}
		/>
	);
}

function DialogDescription({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Description>) {
	return (
		<DialogPrimitive.Description
			data-slot="dialog-description"
			className={cn("text-sm text-muted-foreground", className)}
			{...props}
		/>
	);
}

export {
	Dialog,
	DialogTrigger,
	DialogPortal,
	DialogClose,
	DialogOverlay,
	DialogContent,
	DialogHeader,
	DialogFooter,
	DialogTitle,
	DialogDescription,
};

// Settings-style dialog sections — the single source for settings popups and
// confirmation modals (ConfirmDialog) so they all share the same frame,
// spacing, and typography. Every value resolves to a
// design token; no raw pixel values here.
export const settingsDialogContentClass =
	"z-overlay flex max-h-[min(var(--size-settings-dialog-max-h),calc(100svh-var(--space-8)))] w-[min(var(--size-settings-dialog),calc(100vw-var(--space-8)))] max-w-none flex-col gap-0 overflow-hidden rounded-(--radius-settings-dialog-lg) border border-[var(--color-border-settings-dialog)] bg-popover p-0 text-settings-label shadow-[var(--shadow-settings-dialog)]";

export const settingsDialogHeaderClass =
	"flex shrink-0 flex-col gap-1 border-b border-(--color-border-settings-dialog-header) p-(--size-modal-padding)";

export const settingsDialogBodyClass =
	"flex min-h-0 flex-col gap-4 overflow-y-auto p-(--size-modal-padding) settings-thin-scrollbar";

export const settingsDialogFooterClass =
	"flex shrink-0 flex-wrap items-center justify-end gap-3 border-t border-[var(--color-border-settings-dialog-header)] p-(--size-modal-padding)";
