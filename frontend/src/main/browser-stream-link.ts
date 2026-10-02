import net from "node:net";

const PROTOCOL_VERSION = 1;
const KIND_CONTROL = 1;
const KIND_JPEG = 2;
const MAX_CONTROL_BYTES = 64 << 10;
const MAX_FRAME_BYTES = 2 << 20;
const MAX_PACKET_BYTES = MAX_FRAME_BYTES + 64;
const RETRY_INITIAL_MS = 200;
const RETRY_MAX_MS = 2_000;

export type BrowserStreamControl = {
	type: string;
	version?: number;
	token?: string;
	streamId?: number;
	sessionId?: string;
	payload?: unknown;
	code?: string;
	message?: string;
};

export type BrowserEncodedFrame = {
	streamId: number;
	sequence: bigint;
	capturedAtMs: bigint;
	width: number;
	height: number;
	jpeg: Buffer;
};

export interface BrowserStreamLinkHandle {
	readonly connected: boolean;
	sendControl(control: BrowserStreamControl): boolean;
	sendFrame(frame: BrowserEncodedFrame): boolean;
	dispose(): void;
}

type BrowserStreamLinkOptions = {
	token: string;
	onControl: (control: BrowserStreamControl) => void | Promise<void>;
	log?: (message: string) => void;
};

export function connectBrowserStream(
	address: string | net.TcpNetConnectOpts,
	options: BrowserStreamLinkOptions,
): BrowserStreamLinkHandle {
	const log = options.log ?? (() => undefined);
	let socket: net.Socket | null = null;
	let disposed = false;
	let connected = false;
	let retry: ReturnType<typeof setTimeout> | null = null;
	let backoff = RETRY_INITIAL_MS;
	let buffered: Buffer<ArrayBufferLike> = Buffer.alloc(0);
	const controlChains = new Map<string, Promise<void>>();
	// At most one unsent frame per stream. Socket backpressure replaces stale
	// frames rather than growing an image queue without bound.
	const pendingFrames = new Map<number, Buffer>();
	let draining = false;

	const scheduleRetry = () => {
		if (disposed || retry) return;
		retry = setTimeout(() => {
			retry = null;
			open();
		}, backoff);
		backoff = Math.min(backoff * 2, RETRY_MAX_MS);
	};

	const destroy = () => {
		const target = socket;
		socket = null;
		connected = false;
		buffered = Buffer.alloc(0);
		pendingFrames.clear();
		if (target) {
			target.removeAllListeners();
			target.destroy();
		}
	};

	const writePacket = (packet: Buffer): boolean => {
		const target = socket;
		if (!target || target.destroyed || !connected || packet.byteLength === 0 || packet.byteLength > MAX_PACKET_BYTES) return false;
		const prefix = Buffer.allocUnsafe(4);
		prefix.writeUInt32BE(packet.byteLength);
		return target.write(Buffer.concat([prefix, packet]));
	};

	const sendControl = (control: BrowserStreamControl): boolean => {
		const body = Buffer.from(JSON.stringify(control), "utf8");
		if (body.byteLength > MAX_CONTROL_BYTES) return false;
		return writePacket(Buffer.concat([Buffer.from([KIND_CONTROL]), body]));
	};

	const flushFrames = () => {
		if (draining || !connected) return;
		draining = true;
		try {
			for (const [streamId, packet] of pendingFrames) {
				pendingFrames.delete(streamId);
				if (!writePacket(packet)) break;
			}
		} finally {
			draining = false;
		}
	};

	const sendFrame = (frame: BrowserEncodedFrame): boolean => {
		if (frame.jpeg.byteLength === 0 || frame.jpeg.byteLength > MAX_FRAME_BYTES || frame.width < 1 || frame.height < 1) return false;
		const header = Buffer.allocUnsafe(25);
		header[0] = KIND_JPEG;
		header.writeUInt32BE(frame.streamId, 1);
		header.writeBigUInt64BE(frame.sequence, 5);
		header.writeBigUInt64BE(frame.capturedAtMs, 13);
		header.writeUInt16BE(frame.width, 21);
		header.writeUInt16BE(frame.height, 23);
		pendingFrames.set(frame.streamId, Buffer.concat([header, frame.jpeg]));
		flushFrames();
		return true;
	};

	const dispatchControl = (control: BrowserStreamControl) => {
		if (!control || typeof control.type !== "string") return;
		const key = typeof control.sessionId === "string" ? control.sessionId : "";
		const previous = controlChains.get(key) ?? Promise.resolve();
		const next = previous.then(() => options.onControl(control)).then(() => undefined).catch((error) => {
			log(`browser-stream-link: control failed: ${String(error)}`);
			sendControl({
				type: "error",
				streamId: control.streamId,
				sessionId: control.sessionId,
				code: (error as { code?: string })?.code ?? "BROWSER_HOST_ERROR",
				message: error instanceof Error ? error.message : "Browser host command failed",
			});
		});
		controlChains.set(key, next);
		void next.finally(() => {
			if (controlChains.get(key) === next) controlChains.delete(key);
		});
	};

	const consume = (chunk: Buffer) => {
		buffered = buffered.byteLength === 0 ? chunk : Buffer.concat([buffered, chunk]);
		for (;;) {
			if (buffered.byteLength < 4) return;
			const length = buffered.readUInt32BE(0);
			if (length < 1 || length > MAX_PACKET_BYTES) {
				log(`browser-stream-link: invalid packet length ${length}`);
				destroy();
				scheduleRetry();
				return;
			}
			if (buffered.byteLength < 4 + length) return;
			const packet = buffered.subarray(4, 4 + length);
			buffered = buffered.subarray(4 + length);
			if (packet[0] !== KIND_CONTROL || packet.byteLength - 1 > MAX_CONTROL_BYTES) continue;
			try {
				dispatchControl(JSON.parse(packet.subarray(1).toString("utf8")) as BrowserStreamControl);
			} catch {
				// A malformed private-host command tears down the link rather than
				// allowing the parser to drift onto attacker-controlled boundaries.
				destroy();
				scheduleRetry();
				return;
			}
		}
	};

	const open = () => {
		if (disposed) return;
		const target = typeof address === "string" ? net.createConnection(address) : net.createConnection(address);
		socket = target;
		target.on("connect", () => {
			if (socket !== target || disposed) return;
			connected = true;
			backoff = RETRY_INITIAL_MS;
			sendControl({ type: "hello", version: PROTOCOL_VERSION, token: options.token });
		});
		target.on("data", consume);
		target.on("drain", flushFrames);
		target.on("error", (error) => log(`browser-stream-link: ${String(error)}`));
		target.on("close", () => {
			if (socket === target) destroy();
			scheduleRetry();
		});
	};

	open();
	return {
		get connected() { return connected; },
		sendControl,
		sendFrame,
		dispose() {
			disposed = true;
			if (retry) clearTimeout(retry);
			retry = null;
			destroy();
		},
	};
}
