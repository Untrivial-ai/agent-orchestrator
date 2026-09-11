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
// Both the agent and workspace kinds open their socket DIRECTLY at construction
// and drive readiness off the CP's structured `starting`/`ready` messages —
// there is no separate agent-ready SSE wait. The CP's OpenTerminal(agent) is
// find-or-create for the current worker epoch (it reuses a running agent's
// terminal), so opening a healthy session's terminal is safe and immediate, and
// a not-yet-started agent is held in `starting` up to the CP's ready deadline.
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
	 *
	 * The mux always sends `after=cursor.value`; the CP decides whether to honor
	 * it or reset to 0. Today the CP sends `{type:"reset"}` for both kinds, so
	 * the effective replay is always from 0 (correct across worker-epoch bumps,
	 * whose per-terminal output sequences restart). The cursor plumbing is kept
	 * so a future epoch-aware CP can resume within an epoch instead.
	 */
	cursor?: { value: number };
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
				// The CP deliberately restarts the stream from sequence 0 (a fresh
				// workspace shell, or an agent open whose replay must start clean).
				// Drop our resume cursor and wipe the pane's stale content (clear
				// screen + scrollback, home the cursor) so the fresh replay does not
				// stack on top of the old buffer.
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
		openSocket(kind, ticket);
	};

	// Open directly for both kinds. The CP's find-or-create OpenTerminal plus its
	// structured starting/ready messages drive readiness, so there is no separate
	// agent-ready SSE to wait for — that wait was the orchestrator "Connecting…"
	// stall (a large event log never redelivered the old, low-sequence agent.ready,
	// so the socket was never even attempted).
	void connect(options.kind);

	// A hidden/parked pane attaches at 0×0 to keep receiving output WITHOUT
	// claiming a size — the shared PTY must never be resized from an off-screen
	// grid. The CP rejects a 0-dimension resize as invalid and closes the socket
	// (which would loop a parked reconnect), so a 0×0 open/resize sends nothing;
	// the real size follows from the first visible fit (open for a visible pane,
	// or resize() when the pane becomes visible).
	const sendResize = (cols: number, rows: number) => {
		if (cols <= 0 || rows <= 0) return;
		pendingResize = { cols, rows };
		sendJSON({ type: "resize", columns: cols, rows });
	};

	return {
		open: (_id, cols, rows) => {
			sendResize(cols, rows);
		},
		sendInput: (_id, input) => {
			if (!sendJSON({ type: "input", data: input })) pendingInput.push(input);
		},
		resize: (_id, cols, rows) => {
			sendResize(cols, rows);
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
