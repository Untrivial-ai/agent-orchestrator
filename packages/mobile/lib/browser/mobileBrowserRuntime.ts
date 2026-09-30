import { authHeaders, type ServerConfig } from "../config";
import { mobileBrowserRuntimeURL } from "./mobileBrowserRuntimeUrl";

export type MobileBrowserCommand = {
	type: "command";
	requestId: string;
	sessionId: string;
	action: string;
	args?: Record<string, unknown>;
};

export type MobileBrowserCommandResult =
	| { ok: true; result: Record<string, unknown> }
	| { ok: false; error: { code: string; message: string } };

type RuntimeHandlers = {
	onStatus?: (connected: boolean) => void;
	onActivity?: (active: boolean) => void;
	onCancel?: (requestId: string) => void;
	execute: (command: MobileBrowserCommand) => Promise<MobileBrowserCommandResult>;
};

/** Foreground-only command transport for the WebView visible on the phone. */
export class MobileBrowserRuntimeClient {
	private ws: WebSocket | null = null;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private reconnectDelay = 750;
	private stopped = true;
	private active = new Set<string>();

	constructor(
		private readonly cfg: ServerConfig,
		private readonly sessionId: string,
		private readonly deviceId: string,
		private readonly handlers: RuntimeHandlers,
	) {}

	start(): void {
		if (!this.stopped) return;
		this.stopped = false;
		this.open();
	}

	stop(): void {
		this.stopped = true;
		if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
		this.reconnectTimer = null;
		this.ws?.close();
		this.ws = null;
		this.active.clear();
		this.handlers.onActivity?.(false);
		this.handlers.onStatus?.(false);
	}

	private open(): void {
		if (this.stopped) return;
		try {
			const WS = WebSocket as unknown as {
				new (url: string, protocols?: string | string[], options?: { headers?: Record<string, string> }): WebSocket;
			};
			const ws = new WS(mobileBrowserRuntimeURL(this.cfg, this.sessionId, this.deviceId), undefined, {
				headers: { Origin: "http://localhost", ...authHeaders(this.cfg) },
			});
			this.ws = ws;
			ws.onopen = () => {
				this.reconnectDelay = 750;
				this.handlers.onStatus?.(true);
			};
			ws.onmessage = (event) => this.receive(typeof event.data === "string" ? event.data : "");
			ws.onerror = () => this.handlers.onStatus?.(false);
			ws.onclose = () => {
				if (this.ws === ws) this.ws = null;
				this.handlers.onStatus?.(false);
				this.scheduleReconnect();
			};
		} catch {
			this.handlers.onStatus?.(false);
			this.scheduleReconnect();
		}
	}

	private receive(raw: string): void {
		let command: MobileBrowserCommand | { type: "cancel"; requestId: string };
		try {
			command = JSON.parse(raw) as MobileBrowserCommand | { type: "cancel"; requestId: string };
		} catch {
			return;
		}
		if (!command.requestId) return;
		if (command.type === "cancel") {
			this.handlers.onCancel?.(command.requestId);
			this.active.delete(command.requestId);
			this.handlers.onActivity?.(this.active.size > 0);
			return;
		}
		if (command.type !== "command" || command.sessionId !== this.sessionId) return;
		this.active.add(command.requestId);
		this.handlers.onActivity?.(true);
		void this.handlers.execute(command).then(
			(result) => this.sendResult(command.requestId, result),
			(error) => this.sendResult(command.requestId, {
				ok: false,
				error: { code: "MOBILE_BROWSER_COMMAND_FAILED", message: error instanceof Error ? error.message : String(error) },
			}),
		).finally(() => {
			this.active.delete(command.requestId);
			this.handlers.onActivity?.(this.active.size > 0);
		});
	}

	private sendResult(requestId: string, result: MobileBrowserCommandResult): void {
		if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
		this.ws.send(JSON.stringify({ type: "result", requestId, ...result }));
	}

	private scheduleReconnect(): void {
		if (this.stopped || this.reconnectTimer) return;
		this.reconnectTimer = setTimeout(() => {
			this.reconnectTimer = null;
			this.open();
		}, this.reconnectDelay);
		this.reconnectDelay = Math.min(this.reconnectDelay * 2, 10_000);
	}
}
