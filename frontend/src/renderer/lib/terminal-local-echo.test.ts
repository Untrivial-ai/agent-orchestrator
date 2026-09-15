import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MuxConnectionState, TerminalMux } from "./terminal-mux";
import {
	LOCAL_ECHO_COOLDOWN_MS,
	LOCAL_ECHO_PREDICTION_TIMEOUT_MS,
	TerminalLocalEchoController,
	withPredictiveLocalEcho,
	type TerminalBufferType,
} from "./terminal-local-echo";

const DEL = "\x7f";

function createController() {
	let now = 0;
	let bufferType: TerminalBufferType = "normal";
	const controller = new TerminalLocalEchoController({
		bufferType: () => bufferType,
		now: () => now,
	});
	return {
		controller,
		advance: (ms: number) => {
			now += ms;
		},
		setBufferType: (type: TerminalBufferType) => {
			bufferType = type;
		},
	};
}

describe("TerminalLocalEchoController", () => {
	it("predicts a printable keystroke and strips its matching echo (no double render)", () => {
		const { controller } = createController();
		expect(controller.handleKeystroke("a")).toEqual({ localWrite: "a", sendUpstream: "a" });
		// The echo of what we already rendered must not render again.
		expect(controller.handleServerData("a")).toEqual({ write: "" });
		expect(controller.pendingCount).toBe(0);
		// Later output with nothing pending passes through verbatim.
		expect(controller.handleServerData("$ ")).toEqual({ write: "$ " });
	});

	it("predicts a fast multi-char burst and strips the whole echoed prefix", () => {
		const { controller } = createController();
		for (const char of "hello") {
			expect(controller.handleKeystroke(char)).toEqual({ localWrite: char, sendUpstream: char });
		}
		expect(controller.pendingCount).toBe(5);
		// Echo plus trailing output the burst triggered (e.g. Enter's newline
		// was sent unpredicted in between): prefix stripped, remainder rendered.
		expect(controller.handleServerData("hello\r\n")).toEqual({ write: "\r\n" });
		expect(controller.pendingCount).toBe(0);
	});

	it("keeps unmatched predictions pending across partially matching server chunks", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		controller.handleKeystroke("c");
		// The echo arrives split: first chunk confirms only "a".
		expect(controller.handleServerData("a")).toEqual({ write: "" });
		expect(controller.pendingCount).toBe(2);
		// The rest confirms "bc" and carries new output.
		expect(controller.handleServerData("bc$ ")).toEqual({ write: "$ " });
		expect(controller.pendingCount).toBe(0);
	});

	it("rolls back all remaining predictions on the first mismatch and writes the chunk verbatim", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		// Non-echoing program (password prompt): first output mismatches.
		expect(controller.handleServerData("X")).toEqual({ write: "\x1b[2D\x1b[KX" });
		expect(controller.pendingCount).toBe(0);
	});

	it("counts only still-pending predictions in a mid-chunk mismatch rollback", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		// "a" echoes back, then the server diverges: only "b" is rolled back.
		expect(controller.handleServerData("az")).toEqual({ write: "\x1b[1D\x1b[Kz" });
		expect(controller.pendingCount).toBe(0);
	});

	it("enters a cooldown after a mismatch and resumes predicting once it lapses", () => {
		const { controller, advance } = createController();
		controller.handleKeystroke("a");
		controller.handleServerData("X"); // mismatch → cooldown
		expect(controller.handleKeystroke("b")).toEqual({ sendUpstream: "b" });
		advance(LOCAL_ECHO_COOLDOWN_MS - 1);
		expect(controller.handleKeystroke("c")).toEqual({ sendUpstream: "c" });
		advance(1);
		expect(controller.handleKeystroke("d")).toEqual({ localWrite: "d", sendUpstream: "d" });
	});

	it("rolls back unconfirmed predictions after the timeout and cools down", () => {
		const { controller, advance } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		expect(controller.msUntilTimeout()).toBe(LOCAL_ECHO_PREDICTION_TIMEOUT_MS);
		advance(LOCAL_ECHO_PREDICTION_TIMEOUT_MS - 1);
		expect(controller.expireStalePredictions()).toEqual({ write: "" });
		expect(controller.pendingCount).toBe(2);
		advance(1);
		expect(controller.expireStalePredictions()).toEqual({ write: "\x1b[2D\x1b[K" });
		expect(controller.pendingCount).toBe(0);
		expect(controller.msUntilTimeout()).toBeNull();
		// Timeout implies a non-echoing target: cool down like a mismatch.
		expect(controller.handleKeystroke("c")).toEqual({ sendUpstream: "c" });
		advance(LOCAL_ECHO_COOLDOWN_MS);
		expect(controller.handleKeystroke("d")).toEqual({ localWrite: "d", sendUpstream: "d" });
	});

	it("locally cancels the last pending prediction on backspace and converges with the echo", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		expect(controller.handleKeystroke(DEL)).toEqual({ localWrite: "\b \b", sendUpstream: DEL });
		expect(controller.pendingCount).toBe(1);
		// Server echoes the typed chars then its own erase for the DEL. "a" is
		// stripped; "b" plus the erase render verbatim, converging on screen.
		expect(controller.handleServerData("ab\b\x1b[K")).toEqual({ write: "b\b\x1b[K" });
		expect(controller.pendingCount).toBe(0);
	});

	it("passes backspace through untouched when nothing is pending", () => {
		const { controller } = createController();
		expect(controller.handleKeystroke(DEL)).toEqual({ sendUpstream: DEL });
		expect(controller.handleServerData("\b\x1b[K")).toEqual({ write: "\b\x1b[K" });
	});

	it("never predicts on the alternate buffer", () => {
		const { controller, setBufferType } = createController();
		setBufferType("alternate");
		expect(controller.handleKeystroke("a")).toEqual({ sendUpstream: "a" });
		expect(controller.pendingCount).toBe(0);
		// Alt-screen repaints flow through verbatim.
		expect(controller.handleServerData("\x1b[2J\x1b[Hredraw")).toEqual({ write: "\x1b[2J\x1b[Hredraw" });
	});

	it("stops cancelling on backspace once the buffer flips to alternate", () => {
		const { controller, setBufferType } = createController();
		controller.handleKeystroke("a");
		setBufferType("alternate");
		expect(controller.handleKeystroke(DEL)).toEqual({ sendUpstream: DEL });
		expect(controller.pendingCount).toBe(1);
	});

	it("passes through everything that is not a single one-column printable character", () => {
		const { controller } = createController();
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "\r" }); // Enter
		expect(controller.handleKeystroke("\x1b[A")).toEqual({ sendUpstream: "\x1b[A" }); // arrow key
		expect(controller.handleKeystroke("ab")).toEqual({ sendUpstream: "ab" }); // multi-char burst/paste
		expect(controller.handleKeystroke("\x03")).toEqual({ sendUpstream: "\x03" }); // Ctrl-C
		expect(controller.handleKeystroke("🚀")).toEqual({ sendUpstream: "🚀" }); // wide emoji
		expect(controller.handleKeystroke("漢")).toEqual({ sendUpstream: "漢" }); // wide CJK
		expect(controller.pendingCount).toBe(0);
	});

	it("predicts single-column latin codepoints beyond ASCII", () => {
		const { controller } = createController();
		expect(controller.handleKeystroke("é")).toEqual({ localWrite: "é", sendUpstream: "é" });
		expect(controller.handleServerData("é")).toEqual({ write: "" });
	});

	it("does not let unpredicted keystrokes disturb the pending FIFO", () => {
		const { controller } = createController();
		controller.handleKeystroke("l");
		controller.handleKeystroke("s");
		controller.handleKeystroke("\r"); // Enter: unpredicted, but sent upstream
		expect(controller.pendingCount).toBe(2);
		expect(controller.handleServerData("ls\r\nfile\r\n")).toEqual({ write: "\r\nfile\r\n" });
	});
});

type FakeInnerMux = {
	mux: TerminalMux;
	inputs: string[];
	disposed: boolean;
	emitData(payload: string | Uint8Array): void;
	emitConnection(state: MuxConnectionState): void;
};

function createFakeInnerMux(): FakeInnerMux {
	const dataListeners = new Set<(bytes: Uint8Array) => void>();
	const connectionListeners = new Set<(state: MuxConnectionState) => void>();
	const fake: FakeInnerMux = {
		inputs: [],
		disposed: false,
		mux: {
			open: () => undefined,
			sendInput: (_id, input) => fake.inputs.push(input),
			resize: () => undefined,
			close: () => undefined,
			onData: (_id, listener) => {
				dataListeners.add(listener);
				return () => dataListeners.delete(listener);
			},
			onExit: () => () => undefined,
			onOpened: () => () => undefined,
			onError: () => () => undefined,
			onConnectionChange: (listener) => {
				connectionListeners.add(listener);
				return () => connectionListeners.delete(listener);
			},
			dispose: () => {
				fake.disposed = true;
			},
		},
		emitData: (payload) => {
			const bytes = typeof payload === "string" ? new TextEncoder().encode(payload) : payload;
			dataListeners.forEach((listener) => listener(bytes));
		},
		emitConnection: (state) => connectionListeners.forEach((listener) => listener(state)),
	};
	return fake;
}

describe("withPredictiveLocalEcho", () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	function createWrapped(bufferType: TerminalBufferType = "normal") {
		const inner = createFakeInnerMux();
		const wrapped = withPredictiveLocalEcho(inner.mux, { bufferType: () => bufferType });
		const writes: string[] = [];
		wrapped.onData("h", (bytes) => writes.push(new TextDecoder().decode(bytes)));
		return { inner, wrapped, writes };
	}

	it("renders the prediction locally and forwards the raw keystroke upstream", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		expect(writes).toEqual(["a"]);
		expect(inner.inputs).toEqual(["a"]);
		// The matching echo is stripped — nothing renders twice.
		inner.emitData("a");
		expect(writes).toEqual(["a"]);
	});

	it("does not predict before the socket is open", () => {
		const { inner, wrapped, writes } = createWrapped();
		wrapped.sendInput("h", "a");
		expect(writes).toEqual([]);
		expect(inner.inputs).toEqual(["a"]);
	});

	it("rolls back a prediction whose echo never arrives, on a real timer", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		expect(writes).toEqual(["a"]);
		vi.advanceTimersByTime(LOCAL_ECHO_PREDICTION_TIMEOUT_MS);
		expect(writes).toEqual(["a", "\x1b[1D\x1b[K"]);
	});

	it("rolls back and passes server output verbatim on a mismatch", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		wrapped.sendInput("h", "b");
		inner.emitData("Password: ");
		expect(writes).toEqual(["a", "b", "\x1b[2D\x1b[KPassword: "]);
	});

	it("un-draws pending predictions when the connection drops", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		inner.emitConnection("closed");
		expect(writes).toEqual(["a", "\x1b[1D\x1b[K"]);
	});

	it("never predicts into the alternate buffer", () => {
		const { inner, wrapped, writes } = createWrapped("alternate");
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		expect(writes).toEqual([]);
		expect(inner.inputs).toEqual(["a"]);
		inner.emitData("agent repaint");
		expect(writes).toEqual(["agent repaint"]);
	});

	it("reassembles a UTF-8 codepoint split across server chunks", () => {
		const { inner, writes } = createWrapped();
		inner.emitConnection("open");
		const encoded = new TextEncoder().encode("é"); // two bytes
		inner.emitData(encoded.subarray(0, 1));
		inner.emitData(encoded.subarray(1));
		expect(writes).toEqual(["é"]);
	});

	it("clears its timer and disposes the inner mux on dispose", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		wrapped.dispose();
		expect(inner.disposed).toBe(true);
		vi.advanceTimersByTime(LOCAL_ECHO_PREDICTION_TIMEOUT_MS);
		// No rollback after dispose: the listener set is gone and the timer cleared.
		expect(writes).toEqual(["a"]);
	});
});
