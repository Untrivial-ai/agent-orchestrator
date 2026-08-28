import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useUiStore } from "../stores/ui-store";

export function SessionLinkFeedback() {
	const { t } = useTranslation();
	const error = useUiStore((state) => state.sessionLinkError);
	const dismiss = useUiStore((state) => state.setSessionLinkError);
	if (!error) return null;
	return (
		<div className="pointer-events-auto fixed bottom-4 left-1/2 z-overlay flex max-w-md -translate-x-1/2 items-center gap-3 rounded-lg border border-destructive/40 bg-background px-3 py-2 text-xs text-destructive shadow-xl" role="alert">
			<span>{error}</span>
			<button
				type="button"
				aria-label={t("sessionLink.dismissError")}
				onClick={() => dismiss(null)}
				className="rounded p-1 hover:bg-interactive-hover"
			>
				<X aria-hidden="true" className="size-3" />
			</button>
		</div>
	);
}
