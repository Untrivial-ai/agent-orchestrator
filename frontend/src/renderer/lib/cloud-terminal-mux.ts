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
	mintTicket: () => Promise<string>;
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
	let after = 0;
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
				if (typeof message.sequence === "number") after = message.sequence;
				openedListeners.forEach((listener) => listener());
				break;
			case "output":
				if (typeof message.sequence === "number") after = message.sequence;
				if (message.data) {
					const bytes = base64ToBytes(message.data);
					dataListeners.forEach((listener) => listener(bytes));
				}
				break;
			// starting / reset / replay_complete / input_ack carry no terminal
			// output the pane must render.
			default:
				break;
		}
	};

	const connect = async () => {
		let ticket: string;
		try {
			ticket = await options.mintTicket();
		} catch {
			if (disposed) return;
			// A freshly created session's worker may not be connected yet while its
			// sandbox provisions; the control plane reports that as 409
			// WORKER_UNAVAILABLE on the ticket request. Treat a mint failure as a
			// transient disconnect so the hook reattaches with backoff and the
			// terminal streams once the worker checks in, instead of surfacing a
			// permanent "worker is not connected" error the user must reload past.
			setConnectionState("closed");
			return;
		}
		if (disposed) return;
		const query = new URLSearchParams({
			ticket,
			kind: options.kind,
			after: String(after),
			protocol: "2",
		});
		const url = `${options.wsBaseUrl.replace(/\/+$/, "")}/terminal?${query.toString()}`;
		const ws = new WS(url);
		socket = ws;
		ws.addEventListener("open", () => {
			if (disposed) return;
			if (pendingResize) sendJSON({ type: "resize", columns: pendingResize.cols, rows: pendingResize.rows });
			for (const input of pendingInput.splice(0)) sendJSON({ type: "input", data: input });
			setConnectionState("open");
		});
		ws.addEventListener("message", handleMessage);
		ws.addEventListener("close", (event: CloseEvent) => {
			// A normal closure (1000) is the CP telling us the terminal process
			// exited or the terminal was closed — the pane is gone, so signal exit
			// and let the hook stop reattaching. Any other close is a transport
			// drop the hook should reconnect through (with a fresh ticket).
			if (event.code === 1000 && !exited) {
				exited = true;
				exitListeners.forEach((listener) => listener());
			}
			setConnectionState("closed");
		});
		ws.addEventListener("error", () => setConnectionState("closed"));
	};

	void connect();

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
