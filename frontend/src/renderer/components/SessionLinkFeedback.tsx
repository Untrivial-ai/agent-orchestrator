import { useEffect } from "react";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { type SessionLinkNotice, useUiStore } from "../stores/ui-store";

const NOTICE_DISMISS_MS = 5_000;

export function SessionLinkFeedback() {
	const { t } = useTranslation();
	const error = useUiStore((state) => state.sessionLinkError);
	const notices = useUiStore((state) => state.sessionLinkNotices);
	const dismiss = useUiStore((state) => state.setSessionLinkError);
	if (!error && notices.length === 0) return null;
	return (
		<>
			{notices.length > 0 ? (
				<div
					className="pointer-events-auto fixed left-1/2 top-12 z-overlay flex max-h-[184px] w-[min(360px,calc(100vw-24px))] -translate-x-1/2 flex-col gap-2 overflow-y-auto overscroll-contain"
					data-browser-native-overlay="true"
					data-testid="session-link-notifications"
				>
					{notices.slice().reverse().map((notice) => <SessionLinkNoticeItem key={notice.nonce} dismissLabel={t("sessionLink.dismissError")} notice={notice} />)}
				</div>
			) : null}
			{error ? (
				<div className="pointer-events-auto fixed bottom-4 left-1/2 z-overlay flex max-w-md -translate-x-1/2 items-center gap-3 rounded-lg border border-destructive/40 bg-background px-3 py-2 text-xs text-destructive shadow-xl" role="alert">
					<span>{error}</span>
					<button type="button" aria-label={t("sessionLink.dismissError")} onClick={() => dismiss(null)} className="rounded p-1 hover:bg-interactive-hover">
						<X aria-hidden="true" className="size-3" />
					</button>
				</div>
			) : null}
		</>
	);
}

function SessionLinkNoticeItem({ dismissLabel, notice }: { dismissLabel: string; notice: SessionLinkNotice }) {
	const dismiss = useUiStore((state) => state.dismissSessionLinkNotice);
	useEffect(() => {
		const timer = window.setTimeout(() => dismiss(notice.nonce), NOTICE_DISMISS_MS);
		return () => window.clearTimeout(timer);
	}, [dismiss, notice.nonce]);
	return (
		<section aria-live="polite" className="flex min-h-10 shrink-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2 text-xs text-foreground shadow-xl" role="status">
			<span className="min-w-0 flex-1 wrap-break-word">{notice.message}</span>
			<button type="button" aria-label={dismissLabel} onClick={() => dismiss(notice.nonce)} className="shrink-0 rounded p-1 hover:bg-interactive-hover">
				<X aria-hidden="true" className="size-3" />
			</button>
		</section>
	);
}
