import { authHeaders, type ServerConfig } from "./config";
import { browserLiveURL, decodeBrowserFrame, type BrowserLiveFrame } from "./browserLiveProtocol";

export { browserLiveURL, decodeBrowserFrame, type BrowserLiveFrame } from "./browserLiveProtocol";

export type BrowserLiveState = {
	url: string;
	title: string;
	loading: boolean;
	canGoBack: boolean;
	canGoForward: boolean;
	activeTabId: string;
	tabs: Array<{ id: string; title: string; url: string; active: boolean }>;
	width: number;
	height: number;
};

export type BrowserLiveCommand =
	| { type: "input"; payload: Record<string, unknown> }
	| { type: "navigate"; payload: { action: "open" | "back" | "forward" | "reload" | "stop"; url?: string } }
	| { type: "tab"; payload: { action: "new" | "select" | "close"; tabId?: string; url?: string } };

type BrowserLiveHandlers = {
	onStatus(status: "connecting" | "open" | "closed" | "error", message?: string): void;
	onState(state: BrowserLiveState): void;
	onFrame(frame: BrowserLiveFrame): void;
};

export class BrowserLiveClient {
	private socket: WebSocket | null = null;
	private closed = false;
	private reportedError = false;

	constructor(private readonly config: ServerConfig, private readonly sessionID: string, private readonly handlers: BrowserLiveHandlers) {}

	connect(): void {
		this.closed = false;
		this.reportedError = false;
		this.handlers.onStatus("connecting");
		try {
			const Socket = WebSocket as unknown as {
				new (url: string, protocols?: string | string[], options?: { headers?: Record<string, string> }): WebSocket;
			};
			const socket = new Socket(browserLiveURL(this.config, this.sessionID), undefined, {
				headers: { Origin: "http://localhost", ...authHeaders(this.config) },
			});
			socket.binaryType = "arraybuffer";
			this.socket = socket;
			socket.onopen = () => this.handlers.onStatus("open");
			socket.onerror = () => {
				this.reportedError = true;
				this.handlers.onStatus("error", "The browser stream could not connect.");
			};
			socket.onclose = () => {
				this.socket = null;
				if (this.closed) this.handlers.onStatus("closed");
				else if (!this.reportedError) this.handlers.onStatus("error", "The browser stream ended. Try reconnecting.");
			};
			socket.onmessage = (event) => { void this.handleMessage(event.data); };
		} catch (error) {
			this.handlers.onStatus("error", error instanceof Error ? error.message : "The browser stream could not connect.");
		}
	}

	send(command: BrowserLiveCommand): void {
		if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(command));
	}

	close(): void {
		this.closed = true;
		this.socket?.close();
		this.socket = null;
	}

	private async handleMessage(data: unknown): Promise<void> {
		if (typeof data === "string") {
			const message = JSON.parse(data) as { type?: string; payload?: BrowserLiveState; code?: string; message?: string };
			if (message.type === "state" && message.payload) this.handlers.onState(message.payload);
			if (message.type === "error") {
				this.reportedError = true;
				this.handlers.onStatus("error", message.message ?? message.code ?? "Browser host error");
			}
			return;
		}
		const buffer = data instanceof ArrayBuffer
			? data
			: typeof Blob !== "undefined" && data instanceof Blob
				? await data.arrayBuffer()
				: null;
		if (buffer) this.handlers.onFrame(decodeBrowserFrame(buffer));
	}
}
