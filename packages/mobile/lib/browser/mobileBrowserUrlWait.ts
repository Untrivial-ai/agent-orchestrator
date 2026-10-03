import type { MobileBrowserCommandResult } from "./mobileBrowserRuntime";

type BrowserCommandError = { code: string; message: string };

type PendingUrlWait = {
	url: string;
	resolve: (result: MobileBrowserCommandResult) => void;
	timer: ReturnType<typeof setTimeout>;
};

const cancelled: BrowserCommandError = {
	code: "BROWSER_COMMAND_CANCELLED",
	message: "Browser command was cancelled.",
};

/** Native-side URL waits survive replacement of the WebView document. */
export class MobileBrowserUrlWaits {
	private readonly pending = new Map<string, PendingUrlWait>();

	wait(requestId: string, url: string, currentUrl: string, timeoutValue?: unknown): Promise<MobileBrowserCommandResult> {
		if (currentUrl.includes(url)) return Promise.resolve({ ok: true, result: { matched: true, url: currentUrl } });
		this.cancel(requestId, {
			code: "BROWSER_COMMAND_CANCELLED",
			message: "Browser URL wait was replaced.",
		});
		const requestedTimeout = Number(timeoutValue ?? 10_000);
		const timeoutMs = Number.isFinite(requestedTimeout) ? Math.min(Math.max(requestedTimeout, 0), 55_000) : 10_000;
		return new Promise((resolve) => {
			const timer = setTimeout(() => {
				this.pending.delete(requestId);
				resolve({ ok: false, error: { code: "WAIT_TIMEOUT", message: "Timed out waiting for page condition." } });
			}, timeoutMs);
			this.pending.set(requestId, { url, resolve, timer });
		});
	}

	update(currentUrl: string): void {
		for (const [requestId, pending] of this.pending) {
			if (!currentUrl.includes(pending.url)) continue;
			clearTimeout(pending.timer);
			this.pending.delete(requestId);
			pending.resolve({ ok: true, result: { matched: true, url: currentUrl } });
		}
	}

	cancel(requestId: string, error: BrowserCommandError = cancelled): boolean {
		const pending = this.pending.get(requestId);
		if (!pending) return false;
		clearTimeout(pending.timer);
		this.pending.delete(requestId);
		pending.resolve({ ok: false, error });
		return true;
	}

	close(): void {
		for (const requestId of [...this.pending.keys()]) {
			this.cancel(requestId, {
				code: "BROWSER_TARGET_UNAVAILABLE",
				message: "The mobile browser closed.",
			});
		}
	}
}
