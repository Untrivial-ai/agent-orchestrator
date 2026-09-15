// TerminalMux implementation for cloud sessions.
//
// A cloud session's PTY lives inside its control-plane sandbox, reached over a
// single WebSocket at `${cpOrigin}/api/cloud/v1/terminal`. The socket is
// authorized by a single-use ticket (minted via the CP proxy in the Electron
// main process, so the WorkOS token never reaches the renderer), not by an
// Authorization header, so the renderer can dial it directly.
//
// This adapts the CP's structured terminal protocol (protocol=2) onto the same
// TerminalMux interface the local daemon mux implements, so useTerminalSession's
// attach/replay/reconnect lifecycle works unchanged. Each connection mints its
// own ticket, so the hook's reconnect (which builds a fresh mux) transparently
// gets a fresh ticket.
//
// CP wire (see cloud/internal/httpapi/terminal_handlers.go):
//   client -> {type:"input",data} | {type:"resize",columns,rows}
//   server -> {type:"starting"|"ready"|"reset"|"replay_complete"|"input_ack"}
//             {type:"output",data:<base64>,sequence}

import { base64ToBytes, type MuxConnectionState, type TerminalMux } from "./terminal-mux";

export interface CloudTerminalMuxOptions {
	/** WebSocket base including the API mount, e.g. "wss://host/api/cloud/v1". */
	wsBaseUrl: string;
	/** "agent" attaches the running coding agent; "workspace" opens a shell. */
	kind: "agent" | "workspace";
	/** Mints a fresh single-use terminal ticket (goes through the CP proxy). */
	mintTicket: (kind: "agent" | "workspace") => Promise<string>;
	/**
	 * Replay cursor shared across mux rebuilds for the same pane. The hook
	 * discards a mux and builds a fresh one on every reconnect; without a shared
	 * cursor each new mux would send `after=0` and the control plane would
	 * replay the whole scrollback again, so the terminal never settles and just
	 * flickers. Passing a stable ref object lets a rebuilt mux resume from the
	 * last sequence it received. Omit for a fresh pane (starts at 0).
	 */
	cursor?: { value: number };
	/** Subscribes to the worker's explicit agent-ready lifecycle signal. */
	subscribeAgentReady?: (onReady: () => void) => () => void;
	/** Keep the pane in its connecting state until an agent-ready event arrives. */
	waitForAgentReady?: boolean;
	WebSocketImpl?: typeof WebSocket;
}

type DataListener = (bytes: Uint8Array) => void;
type ExitListener = () => void;
type OpenedListener = () => void;
type ErrorListener = (message: string) => void;
type ConnectionListener = (state: MuxConnectionState) => void;

export function createCloudTerminalMux(options: CloudTerminalMuxOptions): TerminalMux {
	const WS = options.WebSocketImpl ?? WebSocket;
	const dataListeners = new Set<DataListener>();
	const exitListeners = new Set<ExitListener>();
	const openedListeners = new Set<OpenedListener>();
	const errorListeners = new Set<ErrorListener>();
	const connectionListeners = new Set<ConnectionListener>();

	let socket: WebSocket | null = null;
	// Resume from the shared cursor so a rebuilt mux does not replay the whole
	// scrollback from sequence 0 (the flicker/never-settle bug). advanceCursor
	// keeps the shared ref in step with our local position.
	let after = options.cursor?.value ?? 0;
	const advanceCursor = (sequence: number) => {
		after = sequence;
		if (options.cursor) options.cursor.value = sequence;
	};
	let activeKind: "agent" | "workspace" | null = options.waitForAgentReady ? null : options.kind;
	let agentUpgradeRequested = false;
	let agentRetryTimer: ReturnType<typeof setTimeout> | null = null;
	let disposed = false;
	let exited = false;
	let connectionState: MuxConnectionState | undefined;
	let pendingResize: { cols: number; rows: number } | null = null;
	const pendingInput: string[] = [];

	const setConnectionState = (next: MuxConnectionState) => {
		if (disposed || connectionState === next) return;
		connectionState = next;
		connectionListeners.forEach((listener) => listener(next));
	};

	const sendJSON = (message: unknown): boolean => {
		if (socket && socket.readyState === WS.OPEN) {
			socket.send(JSON.stringify(message));
			return true;
		}
		return false;
	};

	const handleMessage = (event: MessageEvent) => {
		if (typeof event.data !== "string") return;
		let message: { type?: string; data?: string; sequence?: number };
		try {
			message = JSON.parse(event.data);
		} catch {
			return;
		}
		switch (message.type) {
			case "ready":
				if (typeof message.sequence === "number") advanceCursor(message.sequence);
				openedListeners.forEach((listener) => listener());
				break;
			case "output":
				if (typeof message.sequence === "number") advanceCursor(message.sequence);
				if (message.data) {
					const bytes = base64ToBytes(message.data);
					dataListeners.forEach((listener) => listener(bytes));
				}
				break;
			case "reset":
				// The CP deliberately restarts the stream from sequence 0 (e.g. a
				// shell switch). Drop our resume cursor and wipe the pane's stale
				// content (clear screen + scrollback, home the cursor) so the fresh
				// replay does not stack on top of the old buffer.
				advanceCursor(0);
				{
					const clear = new TextEncoder().encode("\x1b[3J\x1b[H\x1b[2J");
					dataListeners.forEach((listener) => listener(clear));
				}
				break;
			// starting / replay_complete / input_ack carry no terminal output the
			// pane must render.
			default:
				break;
		}
	};

	const openSocket = (kind: "agent" | "workspace", ticket: string) => {
		if (disposed) return;
		const query = new URLSearchParams({
			ticket,
			kind,
			after: String(after),
			protocol: "2",
		});
		const url = `${options.wsBaseUrl.replace(/\/+$/, "")}/terminal?${query.toString()}`;
		const ws = new WS(url);
		socket = ws;
		ws.addEventListener("open", () => {
			if (disposed || socket !== ws) return;
			if (pendingResize) sendJSON({ type: "resize", columns: pendingResize.cols, rows: pendingResize.rows });
			for (const input of pendingInput.splice(0)) sendJSON({ type: "input", data: input });
			setConnectionState("open");
		});
		ws.addEventListener("message", (event) => {
			if (socket === ws) handleMessage(event);
		});
		ws.addEventListener("close", (event: CloseEvent) => {
			if (socket !== ws) return;
			if (event.code === 1000 && !exited) {
				exited = true;
				exitListeners.forEach((listener) => listener());
			}
			setConnectionState("closed");
		});
		ws.addEventListener("error", () => {
			if (socket === ws) setConnectionState("closed");
		});
	};

	const connect = async (kind: "agent" | "workspace") => {
		let ticket: string;
		try {
			ticket = await options.mintTicket(kind);
		} catch {
			if (disposed) return;
			// A freshly created session's worker may not be connected yet while its
			// sandbox provisions; the control plane reports that as 409
			// WORKER_UNAVAILABLE on the ticket request. Report this as "waiting",
			// distinct from a socket-level "closed", so the hook keeps polling for
			// readiness WITHOUT counting it against the connect-failure circuit
			// breaker: nothing failed to connect, the worker is simply not up yet.
			// Only a genuine post-mint socket failure trips the breaker.
			setConnectionState("waiting");
			return;
		}
		if (disposed) return;
		if (kind === "workspace" && agentUpgradeRequested) return;
		activeKind = kind;
		openSocket(kind, ticket);
	};

	const upgradeToAgent = () => {
		if (disposed || activeKind === "agent" || agentUpgradeRequested) return;
		agentUpgradeRequested = true;
		void (async () => {
			try {
				const ticket = await options.mintTicket("agent");
				if (disposed) return;
				after = 0;
				dataListeners.forEach((listener) => listener(new TextEncoder().encode("\u001bc")));
				const previous = socket;
				socket = null;
				try {
					previous?.close(1000, "switching to coding agent");
				} catch {
					// Already closed.
				}
				activeKind = "agent";
				openSocket("agent", ticket);
			} catch {
				if (disposed) return;
				agentUpgradeRequested = false;
				agentRetryTimer = setTimeout(upgradeToAgent, 100);
			}
		})();
	};

	const unsubscribeAgentReady = options.subscribeAgentReady?.(upgradeToAgent);
	if (!options.waitForAgentReady) void connect(options.kind);

	return {
		open: (_id, cols, rows) => {
			pendingResize = { cols, rows };
			sendJSON({ type: "resize", columns: cols, rows });
		},
		sendInput: (_id, input) => {
			if (!sendJSON({ type: "input", data: input })) pendingInput.push(input);
		},
		resize: (_id, cols, rows) => {
			pendingResize = { cols, rows };
			sendJSON({ type: "resize", columns: cols, rows });
		},
		close: () => {
			if (socket) {
				try {
					socket.close(1000, "closed by client");
				} catch {
					// already closing.
				}
			}
		},
		onData: (_id, listener) => {
			dataListeners.add(listener);
			return () => dataListeners.delete(listener);
		},
		onExit: (_id, listener) => {
			exitListeners.add(listener);
			return () => exitListeners.delete(listener);
		},
		onOpened: (_id, listener) => {
			openedListeners.add(listener);
			return () => openedListeners.delete(listener);
		},
		onError: (_id, listener) => {
			errorListeners.add(listener);
			return () => errorListeners.delete(listener);
		},
		onConnectionChange: (listener) => {
			connectionListeners.add(listener);
			return () => connectionListeners.delete(listener);
		},
		dispose: () => {
			if (disposed) return;
			disposed = true;
			if (agentRetryTimer) clearTimeout(agentRetryTimer);
			unsubscribeAgentReady?.();
			dataListeners.clear();
			exitListeners.clear();
			openedListeners.clear();
			errorListeners.clear();
			connectionListeners.clear();
			if (socket) {
				try {
					socket.close();
				} catch {
					// already closing.
				}
			}
			socket = null;
		},
	};
}
