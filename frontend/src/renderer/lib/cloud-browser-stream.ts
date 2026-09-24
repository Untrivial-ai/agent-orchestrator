import type { BrowserTabState } from "../../main/browser-view-host";
import type { CloudCpClient } from "./cloud-cp";
import { captureRendererEvent } from "./telemetry";

const PROTOCOL_VERSION = 1;
const FRAME_HEADER_BYTES = 35;
const FRAME_MAGIC = "AOBR";
const MAX_FRAME_BYTES = 1024 * 1024 + 512;
const RETRY_DELAYS = [250, 500, 1000, 2000, 5000] as const;
const FIRST_FRAME_TIMEOUT_MS = 30_000;

export type CloudBrowserStatus = "idle" | "connecting" | "waiting" | "ready" | "reconnecting" | "fatal";

export type CloudBrowserSnapshot = {
	status: CloudBrowserStatus;
	frameUrl: string;
	frameWidth: number;
	frameHeight: number;
	frameSequence: number;
	streamEpoch: number;
	url: string;
	title: string;
	tabs: BrowserTabState[];
	activeTabId: string;
	owner: "idle" | "agent" | "user";
	canOperate: boolean;
	canGoBack: boolean;
	canGoForward: boolean;
	isLoading: boolean;
	dialogOpen: boolean;
	dialogType: string;
	dialogText: string;
	dialogPrompt: string;
	viewportPending: boolean;
	error: string;
	errorRequestId: string;
};

export type CloudBrowserControl = {
	type: string;
	version?: number;
	streamEpoch?: number;
	inputSeq?: number;
	minFrameSeq?: number;
	kind?: string;
	targetId?: string;
	url?: string;
	title?: string;
	owner?: string;
	code?: string;
	message?: string;
	width?: number;
	height?: number;
	x?: number;
	y?: number;
	deltaX?: number;
	deltaY?: number;
	button?: string;
	buttons?: number;
	clickCount?: number;
	key?: string;
	codeValue?: string;
	text?: string;
	modifiers?: number;
	tabId?: string;
	operation?: string;
	accepted?: boolean;
	running?: boolean;
	activeTabId?: string;
	canGoBack?: boolean;
	canGoForward?: boolean;
	isLoading?: boolean;
	dialogOpen?: boolean;
	dialogType?: string;
	dialogText?: string;
	dialogPrompt?: string;
	tabs?: BrowserTabState[];
};

type BrowserSocket = Pick<
	WebSocket,
	"binaryType" | "readyState" | "send" | "close" | "onopen" | "onmessage" | "onerror" | "onclose"
>;

export type CloudBrowserStreamOptions = {
	baseUrl: string;
	orgId: string;
	sessionId: string;
	client: Pick<CloudCpClient, "createBrowserViewerTicket">;
	createSocket?: (url: string) => BrowserSocket;
	now?: () => number;
	capture?: (event: string, properties: Record<string, unknown>) => void | Promise<void>;
	onIdle?: () => void;
};

type PendingInput = {
	capturedAt: number;
	kind: "input" | "navigate" | "tab" | "dialog";
	minFrameSeq?: number;
};

type PendingRequest = {
	resolve: () => void;
	reject: (error: Error) => void;
	timer: number;
};

type PendingFrame = {
	receivedAt: number;
	connectStartedAt: number;
	bytes: number;
	width: number;
	height: number;
	firstForConnection: boolean;
	reconnect: boolean;
};

const EMPTY_SNAPSHOT: CloudBrowserSnapshot = {
	status: "idle",
	frameUrl: "",
	frameWidth: 0,
	frameHeight: 0,
	frameSequence: 0,
	streamEpoch: 0,
	url: "",
	title: "",
	tabs: [],
	activeTabId: "",
	owner: "idle",
	canOperate: false,
	canGoBack: false,
	canGoForward: false,
	isLoading: false,
	dialogOpen: false,
	dialogType: "",
	dialogText: "",
	dialogPrompt: "",
	viewportPending: true,
	error: "",
	errorRequestId: "",
};

export function cloudBrowserViewerUrl(baseUrl: string, orgId: string, sessionId: string, ticket: string): string {
	const url = new URL(
		`${baseUrl.replace(/\/+$/, "")}/api/cloud/v1/orgs/${encodeURIComponent(orgId)}/sessions/${encodeURIComponent(sessionId)}/browser-view/stream`,
	);
	url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
	url.searchParams.set("ticket", ticket);
	return url.toString();
}

export class CloudBrowserStream {
	private snapshot: CloudBrowserSnapshot = { ...EMPTY_SNAPSHOT };
	private readonly listeners = new Set<() => void>();
	private socket: BrowserSocket | null = null;
	private refs = 0;
	private retry = 0;
	private generation = 0;
	private retryTimer: number | undefined;
	private releaseTimer: number | undefined;
	private pingTimer: number | undefined;
	private firstFrameTimer: number | undefined;
	private connectingGeneration = 0;
	private missedPongs = 0;
	private inputSeq = 0;
	private readonly retiredEpochs = new Set<number>();
	private readonly pendingInputs = new Map<number, PendingInput>();
	private readonly pendingRequests = new Map<number, PendingRequest>();
	private pendingFrame: { sequence: number; timing: PendingFrame } | null = null;
	private connectStartedAt = 0;
	private connectionIsReconnect = false;
	private hasOpened = false;
	private receivedFrameForConnection = false;

	constructor(private readonly options: CloudBrowserStreamOptions) {}

	getSnapshot = (): CloudBrowserSnapshot => this.snapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	retain(): void {
		this.refs += 1;
		if (this.releaseTimer !== undefined) {
			window.clearTimeout(this.releaseTimer);
			this.releaseTimer = undefined;
		}
		if (this.socket === null && this.retryTimer === undefined && this.connectingGeneration === 0) {
			void this.connect();
		}
	}

	release(): void {
		this.refs = Math.max(0, this.refs - 1);
		if (this.refs !== 0 || this.releaseTimer !== undefined) return;
		this.releaseTimer = window.setTimeout(() => {
			this.releaseTimer = undefined;
			if (this.refs === 0) {
				this.disconnect();
				this.clearFrame();
				this.options.onIdle?.();
			}
		}, 500);
	}

	dispose(): void {
		this.refs = 0;
		if (this.releaseTimer !== undefined) window.clearTimeout(this.releaseTimer);
		this.releaseTimer = undefined;
		this.disconnect();
		if (this.snapshot.frameUrl) URL.revokeObjectURL(this.snapshot.frameUrl);
		this.snapshot = { ...EMPTY_SNAPSHOT };
		this.emit();
	}

	retryNow(): void {
		if (this.refs === 0) return;
		this.generation += 1;
		this.connectingGeneration = 0;
		if (this.retryTimer !== undefined) window.clearTimeout(this.retryTimer);
		this.retryTimer = undefined;
		this.stopPing();
		this.stopFirstFrameTimer();
		if (this.socket?.readyState === WebSocket.OPEN) {
			this.socket.send(JSON.stringify({ type: "detach", version: PROTOCOL_VERSION }));
		}
		this.socket?.close(1000, "browser viewer retry requested");
		this.socket = null;
		this.retry = 0;
		this.rejectPendingRequests(new Error("The browser viewer is reconnecting."));
		this.pendingInputs.clear();
		this.pendingFrame = null;
		this.update({ status: "connecting", error: "", errorRequestId: "", viewportPending: true });
		void this.connect();
	}

	send(control: Omit<CloudBrowserControl, "version" | "streamEpoch">): boolean {
		return this.sendControl(control) !== false;
	}

	request(control: Omit<CloudBrowserControl, "version" | "streamEpoch">): Promise<void> {
		if (control.type !== "navigate" && control.type !== "tab" && control.type !== "dialog") {
			return Promise.reject(new Error("This browser control cannot be acknowledged."));
		}
		const inputSeq = this.sendControl(control);
		if (inputSeq === false || inputSeq === 0) {
			return Promise.reject(new Error(this.controlUnavailableMessage()));
		}
		return new Promise<void>((resolve, reject) => {
			const timer = window.setTimeout(() => {
				this.pendingRequests.delete(inputSeq);
				reject(new Error("The browser did not acknowledge the action."));
			}, 10_000);
			this.pendingRequests.set(inputSeq, { resolve, reject, timer });
		});
	}

	private sendControl(control: Omit<CloudBrowserControl, "version" | "streamEpoch">): number | false {
		const socket = this.socket;
		if (socket === null || socket.readyState !== WebSocket.OPEN) return false;
		if (control.type === "input" || control.type === "navigate" || control.type === "tab" || control.type === "dialog") {
			if (!this.snapshot.canOperate || this.snapshot.viewportPending) return false;
		}
		const inputSeq = control.type === "input" || control.type === "navigate" || control.type === "tab" || control.type === "dialog"
			? ++this.inputSeq
			: undefined;
		if (inputSeq !== undefined) {
			if (this.pendingInputs.size >= 128) {
				const oldest = this.pendingInputs.keys().next().value;
				if (oldest !== undefined) this.pendingInputs.delete(oldest);
			}
			this.pendingInputs.set(inputSeq, {
				capturedAt: this.now(),
				kind: control.type as PendingInput["kind"],
			});
		}
		try {
			socket.send(JSON.stringify({
				...control,
				version: PROTOCOL_VERSION,
				streamEpoch: this.snapshot.streamEpoch,
				...(inputSeq === undefined ? {} : { inputSeq }),
			}));
		} catch {
			if (inputSeq !== undefined) this.pendingInputs.delete(inputSeq);
			return false;
		}
		return inputSeq ?? 0;
	}

	reportPaint(frameSequence: number, decodeMs: number, paintMs: number): void {
		const pending = this.pendingFrame;
		if (!pending || pending.sequence !== frameSequence) return;
		this.pendingFrame = null;
		const paintedAt = this.now();
		if (pending.timing.firstForConnection) {
			this.capture("ao.renderer.cloud_browser_first_frame", {
				elapsed_ms: Math.max(0, Math.round(paintedAt - pending.timing.connectStartedAt)),
				relay_to_paint_ms: Math.max(0, Math.round(paintedAt - pending.timing.receivedAt)),
				decode_ms: Math.max(0, Math.round(decodeMs)),
				paint_ms: Math.max(0, Math.round(paintMs)),
				frame_bytes: pending.timing.bytes,
				width: pending.timing.width,
				height: pending.timing.height,
				reconnect: pending.timing.reconnect,
			});
		}
		for (const [sequence, input] of this.pendingInputs) {
			if (input.minFrameSeq === undefined || frameSequence < input.minFrameSeq) continue;
			this.capture("ao.renderer.cloud_browser_input_frame", {
				elapsed_ms: Math.max(0, Math.round(paintedAt - input.capturedAt)),
				input_kind: input.kind,
			});
			this.pendingInputs.delete(sequence);
		}
	}

	setViewport(width: number, height: number): void {
		const boundedWidth = Math.min(1440, Math.max(320, Math.round(width)));
		const boundedHeight = Math.min(900, Math.max(240, Math.round(height)));
		if (boundedWidth === this.snapshot.frameWidth && boundedHeight === this.snapshot.frameHeight && !this.snapshot.viewportPending) return;
		this.update({ viewportPending: true });
		this.send({ type: "viewport", width: boundedWidth, height: boundedHeight });
	}

	private async connect(): Promise<void> {
		if (this.refs === 0 || this.connectingGeneration !== 0) return;
		const generation = ++this.generation;
		this.connectingGeneration = generation;
		this.connectStartedAt = this.now();
		this.connectionIsReconnect = this.hasOpened;
		this.receivedFrameForConnection = false;
		this.update({ status: this.snapshot.frameUrl ? "reconnecting" : "connecting", error: "", errorRequestId: "" });
		try {
			const ticket = await this.options.client.createBrowserViewerTicket(this.options.orgId, this.options.sessionId);
			if (generation !== this.generation || this.refs === 0) return;
			const createSocket = this.options.createSocket ?? ((url: string) => new WebSocket(url));
			const socket = createSocket(cloudBrowserViewerUrl(
				this.options.baseUrl,
				this.options.orgId,
				this.options.sessionId,
				ticket.ticket,
			));
			socket.binaryType = "arraybuffer";
			this.socket = socket;
			this.update({ canOperate: ticket.canOperate });
			socket.onopen = () => {
				if (generation !== this.generation) return;
				this.hasOpened = true;
				this.retry = 0;
				this.update({ status: this.snapshot.frameUrl ? "reconnecting" : "waiting", error: "", errorRequestId: "" });
				this.startPing();
				this.startFirstFrameTimer(generation, socket);
			};
			socket.onmessage = (event) => {
				if (generation !== this.generation) return;
				this.receive(event.data);
			};
			socket.onerror = () => {
				if (generation === this.generation) this.update({ error: "The browser stream encountered a connection error." });
			};
			socket.onclose = () => {
				if (generation !== this.generation) return;
				this.socket = null;
				this.stopPing();
				this.stopFirstFrameTimer();
				this.scheduleReconnect();
			};
		} catch (error) {
			if (generation !== this.generation || this.refs === 0) return;
			const code = typeof error === "object" && error !== null && "code" in error ? String(error.code) : "";
			const requestId = typeof error === "object" && error !== null && "requestId" in error ? String(error.requestId ?? "") : "";
			const message = error instanceof Error ? error.message : "The browser viewer could not connect.";
			if (code === "BROWSER_POLICY_DENIED" || code === "BROWSER_VIEWER_IN_USE" || code === "unauthorized" || code === "not_found") {
				this.update({ status: "fatal", error: message, errorRequestId: requestId });
				return;
			}
			this.update({ error: message });
			this.scheduleReconnect();
		} finally {
			if (this.connectingGeneration === generation) this.connectingGeneration = 0;
		}
	}

	private scheduleReconnect(): void {
		if (this.refs === 0 || this.snapshot.status === "fatal" || this.retryTimer !== undefined) return;
		this.rejectPendingRequests(new Error("The browser viewer is reconnecting."));
		this.update({ status: this.snapshot.frameUrl ? "reconnecting" : "connecting", viewportPending: true });
		const delay = RETRY_DELAYS[Math.min(this.retry, RETRY_DELAYS.length - 1)]!;
		this.retry += 1;
		this.retryTimer = window.setTimeout(() => {
			this.retryTimer = undefined;
			if (this.refs > 0) void this.connect();
		}, delay);
	}

	private disconnect(): void {
		this.generation += 1;
		this.connectingGeneration = 0;
		if (this.retryTimer !== undefined) window.clearTimeout(this.retryTimer);
		this.retryTimer = undefined;
		this.stopPing();
		this.stopFirstFrameTimer();
		if (this.socket?.readyState === WebSocket.OPEN) {
			this.socket.send(JSON.stringify({ type: "detach", version: PROTOCOL_VERSION }));
		}
		this.socket?.close(1000, "browser viewer hidden");
		this.socket = null;
		this.retry = 0;
		this.rejectPendingRequests(new Error("The browser viewer disconnected."));
		this.pendingInputs.clear();
		this.pendingFrame = null;
		this.update({ status: "idle", viewportPending: true, owner: "idle" });
	}

	private receive(data: unknown): void {
		if (data instanceof ArrayBuffer) {
			this.receiveFrame(data);
			return;
		}
		if (typeof data !== "string" || data.length > 32 * 1024) return;
		let control: CloudBrowserControl;
		try {
			control = JSON.parse(data) as CloudBrowserControl;
		} catch {
			return;
		}
		if (control.version !== PROTOCOL_VERSION) return;
		if (control.streamEpoch !== undefined && control.streamEpoch !== this.snapshot.streamEpoch) {
			this.changeEpoch(control.streamEpoch);
		}
		switch (control.type) {
			case "hello":
			case "attached":
				this.update({ status: this.snapshot.frameUrl ? "reconnecting" : "waiting" });
				break;
			case "pong":
				this.missedPongs = 0;
				break;
			case "state":
				this.update({
					url: control.url ?? "",
					title: control.title ?? "",
					tabs: control.tabs ?? [],
					activeTabId: control.activeTabId ?? control.targetId ?? "",
					owner: normalizeOwner(control.owner),
					canGoBack: control.canGoBack ?? false,
					canGoForward: control.canGoForward ?? false,
					isLoading: control.isLoading ?? false,
					dialogOpen: control.dialogOpen ?? false,
					dialogType: control.dialogType ?? "",
					dialogText: control.dialogText ?? "",
					dialogPrompt: control.dialogPrompt ?? "",
				});
				break;
			case "viewport_ack":
				this.update({ viewportPending: false });
				break;
			case "control_owner":
				this.update({ owner: normalizeOwner(control.owner) });
				break;
			case "agent_action":
				this.update({ owner: control.running ? "agent" : normalizeOwner(control.owner) });
				break;
			case "input_ack": {
				const inputSeq = control.inputSeq ?? 0;
				const pending = this.pendingInputs.get(inputSeq);
				if (pending) {
					pending.minFrameSeq = control.minFrameSeq;
					this.capture("ao.renderer.cloud_browser_input_ack", {
						elapsed_ms: Math.max(0, Math.round(this.now() - pending.capturedAt)),
						input_kind: pending.kind,
					});
				}
				this.resolvePendingRequest(inputSeq);
				this.update({ error: "" });
				break;
			}
			case "input_rejected": {
				const inputSeq = control.inputSeq ?? 0;
				const message = browserControlErrorMessage(control);
				this.pendingInputs.delete(inputSeq);
				this.rejectPendingRequest(inputSeq, new Error(message));
				this.update({ owner: normalizeOwner(control.owner), error: message });
				break;
			}
			case "error":
				if (control.code === "BROWSER_SESSION_UNAVAILABLE" || control.code === "BROWSER_RESTARTING") {
					const message = control.message ?? "The session browser is reconnecting.";
					this.rejectPendingRequests(new Error(message));
					this.update({ status: this.snapshot.frameUrl ? "reconnecting" : "waiting", error: message });
				} else {
					this.update({ error: control.message ?? "The browser stream reported an error." });
				}
				break;
		}
	}

	private receiveFrame(buffer: ArrayBuffer): void {
		if (buffer.byteLength < FRAME_HEADER_BYTES + 2 || buffer.byteLength > MAX_FRAME_BYTES) return;
		const bytes = new Uint8Array(buffer);
		if (String.fromCharCode(...bytes.subarray(0, 4)) !== FRAME_MAGIC || bytes[4] !== PROTOCOL_VERSION || bytes[5] !== 1) return;
		const view = new DataView(buffer);
		const epoch = Number(view.getBigUint64(6));
		const sequence = Number(view.getBigUint64(14));
		const width = view.getUint16(22);
		const height = view.getUint16(24);
		const targetBytes = bytes[34] ?? 0;
		const jpegOffset = FRAME_HEADER_BYTES + targetBytes;
		const jpegBytes = buffer.byteLength - jpegOffset;
		if (
			epoch <= 0 || sequence <= 0 || width <= 0 || height <= 0 || targetBytes <= 0 ||
			targetBytes > 128 || jpegBytes <= 0 || jpegBytes > 1024 * 1024
		) return;
		try {
			new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(FRAME_HEADER_BYTES, jpegOffset));
		} catch {
			return;
		}
		if (this.retiredEpochs.has(epoch)) return;
		if (epoch !== this.snapshot.streamEpoch) this.changeEpoch(epoch);
		if (epoch === this.snapshot.streamEpoch && sequence <= this.snapshot.frameSequence) return;
		this.stopFirstFrameTimer();
		const frameUrl = URL.createObjectURL(new Blob([buffer.slice(jpegOffset)], { type: "image/jpeg" }));
		if (this.snapshot.frameUrl) URL.revokeObjectURL(this.snapshot.frameUrl);
		this.update({
			frameUrl,
			frameWidth: width,
			frameHeight: height,
			frameSequence: sequence,
			streamEpoch: epoch,
			status: "ready",
			error: "",
			errorRequestId: "",
		});
		this.pendingFrame = {
			sequence,
			timing: {
				receivedAt: this.now(),
				connectStartedAt: this.connectStartedAt,
				bytes: jpegBytes,
				width,
				height,
				firstForConnection: !this.receivedFrameForConnection,
				reconnect: this.connectionIsReconnect,
			},
		};
		this.receivedFrameForConnection = true;
	}

	private update(next: Partial<CloudBrowserSnapshot>): void {
		this.snapshot = { ...this.snapshot, ...next };
		this.emit();
	}

	private changeEpoch(epoch: number): void {
		if (this.snapshot.streamEpoch > 0) {
			this.retiredEpochs.add(this.snapshot.streamEpoch);
			if (this.retiredEpochs.size > 32) {
				const oldest = this.retiredEpochs.values().next().value;
				if (oldest !== undefined) this.retiredEpochs.delete(oldest);
			}
		}
		this.pendingInputs.clear();
		this.rejectPendingRequests(new Error("The browser viewer restarted."));
		this.pendingFrame = null;
		this.update({ streamEpoch: epoch, frameSequence: 0, viewportPending: true });
	}

	private now(): number {
		return this.options.now?.() ?? performance.now();
	}

	private capture(event: string, properties: Record<string, unknown>): void {
		const capture = this.options.capture ?? captureRendererEvent;
		void Promise.resolve(capture(event, properties)).catch(() => undefined);
	}

	private clearFrame(): void {
		if (!this.snapshot.frameUrl && this.snapshot.frameWidth === 0 && this.snapshot.frameHeight === 0 && this.snapshot.frameSequence === 0) return;
		if (this.snapshot.frameUrl) URL.revokeObjectURL(this.snapshot.frameUrl);
		this.update({ frameUrl: "", frameWidth: 0, frameHeight: 0, frameSequence: 0 });
	}

	private controlUnavailableMessage(): string {
		if (this.socket === null || this.socket.readyState !== WebSocket.OPEN) return "The browser viewer is disconnected.";
		if (this.snapshot.viewportPending) return "The browser is still connecting.";
		if (!this.snapshot.canOperate) return "This browser is read-only.";
		return "The browser viewer is disconnected.";
	}

	private resolvePendingRequest(inputSeq: number): void {
		const pending = this.pendingRequests.get(inputSeq);
		if (!pending) return;
		window.clearTimeout(pending.timer);
		this.pendingRequests.delete(inputSeq);
		pending.resolve();
	}

	private rejectPendingRequest(inputSeq: number, error: Error): void {
		const pending = this.pendingRequests.get(inputSeq);
		if (!pending) return;
		window.clearTimeout(pending.timer);
		this.pendingRequests.delete(inputSeq);
		pending.reject(error);
	}

	private rejectPendingRequests(error: Error): void {
		for (const [inputSeq, pending] of this.pendingRequests) {
			window.clearTimeout(pending.timer);
			this.pendingRequests.delete(inputSeq);
			pending.reject(error);
		}
	}

	private startPing(): void {
		this.stopPing();
		this.missedPongs = 0;
		this.pingTimer = window.setInterval(() => {
			if (this.missedPongs >= 3) {
				this.socket?.close(1011, "browser viewer keepalive timed out");
				return;
			}
			if (this.send({ type: "ping" })) this.missedPongs += 1;
		}, 20_000);
	}

	private stopPing(): void {
		if (this.pingTimer !== undefined) window.clearInterval(this.pingTimer);
		this.pingTimer = undefined;
		this.missedPongs = 0;
	}

	private startFirstFrameTimer(generation: number, socket: BrowserSocket): void {
		this.stopFirstFrameTimer();
		this.firstFrameTimer = window.setTimeout(() => {
			this.firstFrameTimer = undefined;
			if (generation !== this.generation || socket !== this.socket || this.receivedFrameForConnection) return;
			this.generation += 1;
			this.socket = null;
			this.stopPing();
			socket.close(1011, "browser viewer first frame timed out");
			this.update({ error: "The browser stream did not deliver a frame." });
			this.scheduleReconnect();
		}, FIRST_FRAME_TIMEOUT_MS);
	}

	private stopFirstFrameTimer(): void {
		if (this.firstFrameTimer !== undefined) window.clearTimeout(this.firstFrameTimer);
		this.firstFrameTimer = undefined;
	}

	private emit(): void {
		for (const listener of this.listeners) listener();
	}
}

function normalizeOwner(owner: string | undefined): CloudBrowserSnapshot["owner"] {
	return owner === "agent" || owner === "user" ? owner : "idle";
}

function browserControlErrorMessage(control: CloudBrowserControl): string {
	if (control.message) return control.message;
	switch (control.code) {
		case "BROWSER_AGENT_CONTROL_ACTIVE":
			return "The session agent is controlling the browser. Try again in a moment.";
		case "BROWSER_INPUT_RATE_EXCEEDED":
			return "Browser input is arriving too quickly. Try again in a moment.";
		case "BROWSER_POLICY_DENIED":
			return "This browser is read-only.";
		case "BROWSER_SESSION_UNAVAILABLE":
		case "BROWSER_RESTARTING":
			return "The session browser is reconnecting.";
		default:
			return "Browser input was rejected.";
	}
}
