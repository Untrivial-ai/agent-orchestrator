import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { type GlobalToast as Toast, useUiStore } from "../stores/ui-store";

const TOAST_DISMISS_MS = 3_500;

export function GlobalToast() {
	const toasts = useUiStore((state) => state.globalToasts);

	if (toasts.length === 0) return null;

	return (
		<>
			<GlobalToastStack placement="top-center" toasts={toasts.filter((toast) => toast.placement === "top-center")} />
			<GlobalToastStack placement="bottom-right" toasts={toasts.filter((toast) => toast.placement !== "top-center")} />
		</>
	);
}

function GlobalToastStack({ placement, toasts }: { placement: NonNullable<Toast["placement"]>; toasts: Toast[] }) {
	if (toasts.length === 0) return null;
	return (
		<div
			className={`pointer-events-none fixed z-[calc(var(--z-overlay)+1)] flex w-[min(360px,calc(100vw-24px))] items-stretch gap-2 ${
				placement === "top-center" ? "left-1/2 top-12 max-h-[184px] -translate-x-1/2 flex-col overflow-y-auto overscroll-contain" : "right-3 bottom-3 flex-col-reverse"
			}`}
			data-browser-native-overlay="true"
			data-state="open"
		>
			{toasts.slice().reverse().map((toast) => <GlobalToastItem key={toast.nonce} toast={toast} />)}
		</div>
	);
}

function GlobalToastItem({ toast }: { toast: Toast }) {
	const { t } = useTranslation();
	const [exiting, setExiting] = useState(false);
	const dismissGlobalToast = useUiStore((state) => state.dismissGlobalToast);
	const isError = toast.tone === "error";
	const dismissMs = toast.durationMs ?? TOAST_DISMISS_MS;

	useEffect(() => {
		const exitTimer = window.setTimeout(() => setExiting(true), Math.max(0, dismissMs - 180));
		const dismissTimer = window.setTimeout(() => dismissGlobalToast(toast.nonce), dismissMs);
		return () => {
			window.clearTimeout(exitTimer);
			window.clearTimeout(dismissTimer);
		};
	}, [dismissGlobalToast, dismissMs, toast.nonce]);

	return (
		<section
			aria-live={isError ? "assertive" : "polite"}
			className={`${exiting ? "toast-exit" : "toast-enter"} pointer-events-auto flex items-start gap-3 rounded-welcome-panel px-3.5 py-3 text-xs shadow-[var(--shadow-import-modal)] ${
				isError ? "border border-destructive/40 bg-destructive/10" : "border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)]"
			}`}
			role={isError ? "alert" : "status"}
		>
			<div className="min-w-0 flex-1">
				<p className={`wrap-break-word font-medium ${isError ? "text-destructive" : "text-(--color-text-import-title)"}`}>{toast.title}</p>
				{toast.body ? <p className="mt-0.5 wrap-break-word text-pretty text-[var(--color-text-import-muted)]">{toast.body}</p> : null}
			</div>
			{toast.dismissible ? (
				<button
					type="button"
					aria-label={t("shortcut.dismissNotification")}
					onClick={() => dismissGlobalToast(toast.nonce)}
					className="shrink-0 rounded p-1 text-muted-foreground hover:bg-interactive-hover hover:text-foreground"
				>
					<X aria-hidden="true" className="size-3" />
				</button>
			) : null}
		</section>
	);
}
