// WebSocket client for the daemon's terminal multiplexer (`/mux`).
//
// The wire protocol mirrors backend/internal/terminal/protocol.go: a single
// JSON-framed socket tagged by channel ("ch"). Terminal payloads carry the PTY
// bytes base64-encoded in `data` because PTY output is arbitrary bytes that a
// raw JSON string cannot represent.
//
//   ch "terminal" — per-pane byte stream keyed by an opaque runtime handle id
//     client → open{id,cols,rows} | data{id,data} | resize{id,cols,rows,force?} | close{id}
//     server → opened{id} | data{id,data} | exited{id} | error{id?,error}
//   ch "system"   — ping/pong liveness
//
// The renderer connects directly to the loopback daemon (same host/port as the
// REST API, path `/mux`); it is not proxied through the Electron main process.

type ServerFrame = {
	ch: string;
	id?: string;
	type: string;
	data?: string;
	error?: string;
};

// ---- pure framing helpers (unit-tested in terminal-mux.test.ts) ----

export function bytesToBase64(bytes: Uint8Array): string {
	let binary = "";
	const chunk = 0x8000;
	for (let i = 0; i < bytes.length; i += chunk) {
		binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
	}
	return btoa(binary);
}

export function base64ToBytes(b64: string): Uint8Array {
	const binary = atob(b64);
	const bytes = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i += 1) {
		bytes[i] = binary.charCodeAt(i);
	}
	return bytes;
}

export function openFrame(id: string, cols: number, rows: number): string {
	return JSON.stringify({ ch: "terminal", type: "open", id, cols, rows });
}

export function dataFrame(id: string, bytes: Uint8Array): string {
	return JSON.stringify({ ch: "terminal", type: "data", id, data: bytesToBase64(bytes) });
}

export function resizeFrame(id: string, cols: number, rows: number, force = false): string {
	return JSON.stringify({
		ch: "terminal",
		type: "resize",
		id,
		cols,
		rows,
		...(force ? { force: true } : {}),
	});
}

export function closeFrame(id: string): string {
	return JSON.stringify({ ch: "terminal", type: "close", id });
}

function pingFrame(): string {
	return JSON.stringify({ ch: "system", type: "ping" });
}

// Derive the ws(s)://.../mux URL from the REST API base. The mux is mounted at
// the router root (backend router.go), not under /api/v1.
export function muxUrlFromApiBase(apiBaseUrl: string): string {
	if (apiBaseUrl === "" && typeof window !== "undefined") {
		const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
		return `${protocol}//${window.location.host}/mux`;
	}

	// http://host → ws://host and https://host → wss://host (the trailing "s" is left
	// in place by the anchored replace). apiBaseUrl is the host root (e.g.
	// http://127.0.0.1:4317); strip any trailing slash before appending /mux.
	const ws = apiBaseUrl.replace(/^http/i, "ws");
	return `${ws.replace(/\/+$/, "")}/mux`;
}

type DataListener = (bytes: Uint8Array) => void;
type ExitListener = () => void;
type OpenedListener = () => void;
type ErrorListener = (message: string) => void;

// "waiting" is a cloud-only state: the control plane answered the terminal
// ticket mint with 409 WORKER_UNAVAILABLE because the sandbox worker has not
// checked in yet (normal during a cold start). It reconnects like "closed" but
// must NOT count against the connect-failure circuit breaker, because nothing
// actually failed to connect. The local daemon mux never emits it.
export type MuxConnectionState = "open" | "closed" | "waiting";
type ConnectionListener = (state: MuxConnectionState) => void;

export type TerminalMux = {
	/** Open a PTY pane for the given runtime/session id at an initial size. */
	open: (id: string, cols: number, rows: number) => void;
	/** Forward user-originated keyboard/paste data to the pane. */
	sendInput: (id: string, input: string) => void;
	/** Resize normally, or explicitly re-signal an unchanged grid for recovery. */
	resize: (id: string, cols: number, rows: number, force?: boolean) => void;
	close: (id: string) => void;
	onData: (id: string, listener: DataListener) => () => void;
	onExit: (id: string, listener: ExitListener) => () => void;
	/** Server ack that the pane is attached; the output replay follows it. */
	onOpened: (id: string, listener: OpenedListener) => () => void;
	/**
	 * Server `error` frames. A frame carrying a pane id reaches that pane's
	 * listeners; an id-less frame is connection-scoped and reaches every error
	 * listener.
	 */
	onError: (id: string, listener: ErrorListener) => () => void;
	/** Socket-level state: "open" on connect, "closed" on close or socket error. */
	onConnectionChange: (listener: ConnectionListener) => () => void;
	/** Close the socket and drop all listeners. */
	dispose: () => void;
};

export type TerminalMuxPool = {
	/**
	 * Acquire an independently disposable attachment lease over the shared
	 * browser-to-daemon mux socket.
	 */
	acquire: () => TerminalMux;
	/** Release the current shared socket and every listener (the pool stays reusable). */
	dispose: () => void;
};

const PING_INTERVAL_MS = 20_000;

function subscribeById<T>(map: Map<string, Set<T>>, id: string, listener: T): () => void {
	const set = map.get(id) ?? new Set<T>();
	set.add(listener);
	map.set(id, set);
	return () => set.delete(listener);
}

/**
 * Create a mux client over a single WebSocket. Frames sent before the socket is
 * OPEN are queued and flushed on connect. There is no auto-reconnect at this
 * layer: a dropped socket is reported through onConnectionChange("closed") and
 * the owner (useTerminalSession) decides whether to build a fresh client.
 */
export function createTerminalMux(url: string, WebSocketImpl: typeof WebSocket = WebSocket): TerminalMux {
	const socket = new WebSocketImpl(url);
	const encoder = new TextEncoder();
	const queue: string[] = [];
	const dataListeners = new Map<string, Set<DataListener>>();
	const exitListeners = new Map<string, Set<ExitListener>>();
	const openedListeners = new Map<string, Set<OpenedListener>>();
	const errorListeners = new Map<string, Set<ErrorListener>>();
	const connectionListeners = new Set<ConnectionListener>();
	let connectionState: MuxConnectionState | undefined;
	let pingTimer: ReturnType<typeof setInterval> | undefined;
	let disposed = false;

	// Dedupes transitions: a socket "error" event is typically followed by
	// "close", and only the first should notify.
	const setConnectionState = (next: MuxConnectionState) => {
		if (disposed || connectionState === next) return;
		connectionState = next;
		connectionListeners.forEach((listener) => listener(next));
	};

	const flush = () => {
		while (queue.length > 0) {
			const frame = queue.shift();
			if (frame !== undefined) socket.send(frame);
		}
	};

	const send = (frame: string) => {
		if (disposed) return;
		if (socket.readyState === WebSocketImpl.OPEN) {
			socket.send(frame);
		} else {
			queue.push(frame);
		}
	};

	socket.addEventListener("open", () => {
		if (disposed) return;
		flush();
		pingTimer = setInterval(() => send(pingFrame()), PING_INTERVAL_MS);
		setConnectionState("open");
	});

	socket.addEventListener("close", () => {
		setConnectionState("closed");
	});
	socket.addEventListener("error", () => {
		setConnectionState("closed");
	});

	socket.addEventListener("message", (event: MessageEvent) => {
		if (typeof event.data !== "string") return;
		let frame: ServerFrame;
		try {
			frame = JSON.parse(event.data) as ServerFrame;
		} catch {
			return;
		}
		if (frame.ch !== "terminal") return;
		if (frame.type === "error") {
			const message = frame.error ?? "unknown terminal error";
			if (frame.id !== undefined) {
				errorListeners.get(frame.id)?.forEach((listener) => listener(message));
			} else {
				errorListeners.forEach((set) => set.forEach((listener) => listener(message)));
			}
			return;
		}
		if (frame.id === undefined) return;
		if (frame.type === "data" && frame.data) {
			dataListeners.get(frame.id)?.forEach((listener) => listener(base64ToBytes(frame.data as string)));
		} else if (frame.type === "exited") {
			exitListeners.get(frame.id)?.forEach((listener) => listener());
		} else if (frame.type === "opened") {
			openedListeners.get(frame.id)?.forEach((listener) => listener());
		}
	});

	const dispose = () => {
		if (disposed) return;
		disposed = true;
		if (pingTimer) clearInterval(pingTimer);
		dataListeners.clear();
		exitListeners.clear();
		openedListeners.clear();
		errorListeners.clear();
		connectionListeners.clear();
		try {
			socket.close();
		} catch {
			// socket may already be closing; ignore.
		}
	};

	return {
		open: (id, cols, rows) => {
			send(openFrame(id, cols, rows));
		},
		sendInput: (id, input) => {
			const bytes = encoder.encode(input);
			send(dataFrame(id, bytes));
		},
		resize: (id, cols, rows, force) => {
			send(resizeFrame(id, cols, rows, force));
		},
		close: (id) => {
			send(closeFrame(id));
		},
		onData: (id, listener) => subscribeById(dataListeners, id, listener),
		onExit: (id, listener) => subscribeById(exitListeners, id, listener),
		onOpened: (id, listener) => subscribeById(openedListeners, id, listener),
		onError: (id, listener) => subscribeById(errorListeners, id, listener),
		onConnectionChange: (listener) => {
			connectionListeners.add(listener);
			return () => connectionListeners.delete(listener);
		},
		dispose,
	};
}

/**
 * Share the mux transport without sharing attachment ownership.
 *
 * The daemon protocol already multiplexes frames by terminal id. A lease keeps
 * listener and cleanup ownership local to one useTerminalSession instance while
 * avoiding one WebSocket and ping timer per retained xterm. The last lease
 * closes the underlying client. A socket-level failure retires that client so
 * reconnecting leases converge on one replacement socket.
 */
export function createTerminalMuxPool(createMux: () => TerminalMux): TerminalMuxPool {
	type Connection = {
		closed: boolean;
		disposed: boolean;
		mux: TerminalMux;
		refs: number;
		unsubscribeState: () => void;
	};

	const connections = new Set<Connection>();
	let current: Connection | null = null;

	const disposeConnection = (connection: Connection) => {
		if (connection.disposed) return;
		connection.disposed = true;
		connection.closed = true;
		if (current === connection) current = null;
		connections.delete(connection);
		connection.unsubscribeState();
		connection.mux.dispose();
	};

	const newConnection = (): Connection => {
		const mux = createMux();
		const connection: Connection = {
			closed: false,
			disposed: false,
			mux,
			refs: 0,
			unsubscribeState: () => undefined,
		};
		connection.unsubscribeState = mux.onConnectionChange((state) => {
			if (state !== "closed") return;
			connection.closed = true;
			if (current === connection) current = null;
			if (connection.refs === 0) disposeConnection(connection);
		});
		connections.add(connection);
		current = connection;
		return connection;
	};

	const acquire = (): TerminalMux => {
		const connection = current && !current.closed && !current.disposed ? current : newConnection();
		connection.refs += 1;
		let released = false;
		const subscriptions = new Set<() => void>();

		const subscribe = (register: () => () => void): (() => void) => {
			if (released || connection.closed || connection.disposed) return () => undefined;
			const unsubscribe = register();
			let subscribed = true;
			const dispose = () => {
				if (!subscribed) return;
				subscribed = false;
				subscriptions.delete(dispose);
				unsubscribe();
			};
			subscriptions.add(dispose);
			return dispose;
		};

		const dispose = () => {
			if (released) return;
			released = true;
			for (const unsubscribe of [...subscriptions]) unsubscribe();
			connection.refs -= 1;
			if (connection.refs === 0) disposeConnection(connection);
		};

		return {
			open: (id, cols, rows) => {
				if (!released && !connection.closed && !connection.disposed) connection.mux.open(id, cols, rows);
			},
			sendInput: (id, input) => {
				if (!released && !connection.closed && !connection.disposed) connection.mux.sendInput(id, input);
			},
			resize: (id, cols, rows, force) => {
				if (!released && !connection.closed && !connection.disposed) {
					connection.mux.resize(id, cols, rows, force);
				}
			},
			close: (id) => {
				if (!released && !connection.closed && !connection.disposed) connection.mux.close(id);
			},
			onData: (id, listener) => subscribe(() => connection.mux.onData(id, listener)),
			onExit: (id, listener) => subscribe(() => connection.mux.onExit(id, listener)),
			onOpened: (id, listener) => subscribe(() => connection.mux.onOpened(id, listener)),
			onError: (id, listener) => subscribe(() => connection.mux.onError(id, listener)),
			onConnectionChange: (listener) => subscribe(() => connection.mux.onConnectionChange(listener)),
			dispose,
		};
	};

	return {
		acquire,
		dispose: () => {
			current = null;
			for (const connection of [...connections]) disposeConnection(connection);
		},
	};
}
