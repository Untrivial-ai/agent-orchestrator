import type { MobileBrowserCommandResult } from "./mobileBrowserRuntime";

export type PendingMobileBrowserNavigation = {
	requestId: string;
	started: boolean;
	resolve: (result: MobileBrowserCommandResult) => void;
	timer: ReturnType<typeof setTimeout>;
	finishTimer?: ReturnType<typeof setTimeout>;
};

export function clearPendingMobileBrowserNavigationTimers(pending: PendingMobileBrowserNavigation): void {
	clearTimeout(pending.timer);
	if (pending.finishTimer) clearTimeout(pending.finishTimer);
	pending.finishTimer = undefined;
}

export function failPendingMobileBrowserNavigation(
	pending: PendingMobileBrowserNavigation | null,
	error: { code: string; message: string },
): boolean {
	if (!pending?.started) return false;
	clearPendingMobileBrowserNavigationTimers(pending);
	pending.resolve({ ok: false, error });
	return true;
}

export function schedulePendingMobileBrowserNavigationSuccess(
	pending: PendingMobileBrowserNavigation,
	settle: () => void,
	defer: boolean,
): boolean {
	if (!pending.started) return false;
	if (pending.finishTimer) clearTimeout(pending.finishTimer);
	if (defer) {
		pending.finishTimer = setTimeout(() => {
			pending.finishTimer = undefined;
			settle();
		}, 0);
	} else {
		settle();
	}
	return true;
}
