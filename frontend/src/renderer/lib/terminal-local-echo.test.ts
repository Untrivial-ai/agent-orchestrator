import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MuxConnectionState, TerminalMux } from "./terminal-mux";
import {
	LOCAL_ECHO_PREDICTION_TIMEOUT_MS,
	TerminalLocalEchoController,
	withLineBufferedLocalInput,
} from "./terminal-local-echo";

const DEL = "\x7f";

function createController() {
	let now = 0;
	const controller = new TerminalLocalEchoController({
		now: () => now,
	});
	return {
		controller,
		advance: (ms: number) => {
			now += ms;
		},
	};
}

describe("TerminalLocalEchoController", () => {
	it("renders a printable keystroke locally without sending it upstream", () => {
		const { controller } = createController();
		expect(controller.handleKeystroke("a")).toEqual({ localWrite: "\x1b[Ka" });
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "a", submitUpstream: "\r" });
		// The echo of what we already rendered must not render again.
		expect(controller.handleServerData("a")).toEqual({ write: "" });
		expect(controller.pendingCount).toBe(0);
		// Later output with nothing pending passes through verbatim.
		expect(controller.handleServerData("$ ")).toEqual({ write: "$ " });
	});

	it("sends a complete typed line followed by a distinct Enter event", () => {
		const { controller } = createController();
		for (const [index, char] of [..."hello"].entries()) {
			expect(controller.handleKeystroke(char)).toEqual({ localWrite: index === 0 ? `\x1b[K${char}` : char });
		}
		expect(controller.pendingCount).toBe(5);
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "hello", submitUpstream: "\r" });
		expect(controller.handleServerData("hello\r\n")).toEqual({ write: "\r\n" });
		expect(controller.pendingCount).toBe(0);
	});

	it("wraps the flushed line in bracketed paste on Enter once the remote enables DECSET 2004", () => {
		// An agent TUI (Codex) enables bracketed paste and reads a coalesced
		// "line\r" as a paste that inserts without submitting, forcing a second
		// Enter. Wrapping the line so the trailing Enter lands outside ESC[201~
		// makes it submit on the first Enter even when the writes coalesce.
		const { controller } = createController();
		controller.observeServerOutput("\x1b[?2004h");
		for (const char of [..."ls"]) controller.handleKeystroke(char);
		expect(controller.handleKeystroke("\r")).toEqual({
			sendUpstream: "\x1b[200~ls\x1b[201~",
			submitUpstream: "\r",
		});
	});

	it("never wraps a plain shell that has not enabled bracketed paste", () => {
		const { controller } = createController();
		controller.handleKeystroke("l");
		controller.handleKeystroke("s");
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "ls", submitUpstream: "\r" });
	});

	it("stops wrapping once the remote disables bracketed paste (last marker wins)", () => {
		const { controller } = createController();
		// A single chunk carrying enable then disable: the later marker wins.
		controller.observeServerOutput("\x1b[?2004h...\x1b[?2004l");
		controller.handleKeystroke("l");
		controller.handleKeystroke("s");
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "ls", submitUpstream: "\r" });
	});

	it("detects a DECSET 2004 marker split across two server chunks", () => {
		const { controller } = createController();
		// The 8-byte enable marker arrives split across two frames; the carry must
		// still detect it so the Enter wrap is applied.
		controller.observeServerOutput("prompt\x1b[?20");
		controller.observeServerOutput("04h> ");
		controller.handleKeystroke("l");
		controller.handleKeystroke("s");
		expect(controller.handleKeystroke("\r")).toEqual({
			sendUpstream: "\x1b[200~ls\x1b[201~",
			submitUpstream: "\r",
		});
	});

	it("keeps unmatched predictions pending across partially matching server chunks", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		controller.handleKeystroke("c");
		controller.handleKeystroke("\r");
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
		controller.handleKeystroke("\r");
		// Non-echoing program (password prompt): first output mismatches.
		expect(controller.handleServerData("X")).toEqual({ write: "\x1b[2D\x1b[KX" });
		expect(controller.pendingCount).toBe(0);
	});

	it("counts only still-pending predictions in a mid-chunk mismatch rollback", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		controller.handleKeystroke("\r");
		// "a" echoes back, then the server diverges: only "b" is rolled back.
		expect(controller.handleServerData("az")).toEqual({ write: "\x1b[1D\x1b[Kz" });
		expect(controller.pendingCount).toBe(0);
	});

	it("continues buffering immediately after an authoritative mismatch", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("\r");
		controller.handleServerData("X");
		expect(controller.handleKeystroke("b")).toEqual({ localWrite: "\x1b[Kb" });
	});

	it("rolls back unconfirmed predictions after the timeout without disabling buffering", () => {
		const { controller, advance } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		controller.handleKeystroke("\r");
		expect(controller.msUntilTimeout()).toBe(LOCAL_ECHO_PREDICTION_TIMEOUT_MS);
		advance(LOCAL_ECHO_PREDICTION_TIMEOUT_MS - 1);
		expect(controller.expireStalePredictions()).toEqual({ write: "" });
		expect(controller.pendingCount).toBe(2);
		advance(1);
		expect(controller.expireStalePredictions()).toEqual({ write: "\x1b[2D\x1b[K" });
		expect(controller.pendingCount).toBe(0);
		expect(controller.msUntilTimeout()).toBeNull();
		expect(controller.handleKeystroke("c")).toEqual({ localWrite: "\x1b[Kc" });
	});

	it("edits the unsent local draft with backspace", () => {
		const { controller } = createController();
		controller.handleKeystroke("a");
		controller.handleKeystroke("b");
		expect(controller.handleKeystroke(DEL)).toEqual({ localWrite: "\b \b" });
		expect(controller.pendingCount).toBe(1);
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "a", submitUpstream: "\r" });
		expect(controller.handleServerData("a")).toEqual({ write: "" });
		expect(controller.pendingCount).toBe(0);
	});

	it("passes backspace through untouched when nothing is pending", () => {
		const { controller } = createController();
		expect(controller.handleKeystroke(DEL)).toEqual({ sendUpstream: DEL });
		expect(controller.handleServerData("\b\x1b[K")).toEqual({ write: "\b\x1b[K" });
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
		expect(controller.handleKeystroke("é")).toEqual({ localWrite: "\x1b[Ké" });
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "é", submitUpstream: "\r" });
		expect(controller.handleServerData("é")).toEqual({ write: "" });
	});

	it("flushes a local draft before a control sequence in the same frame", () => {
		const { controller } = createController();
		controller.handleKeystroke("l");
		controller.handleKeystroke("s");
		expect(controller.handleKeystroke("\x1b[A")).toEqual({ sendUpstream: "ls\x1b[A" });
		expect(controller.pendingCount).toBe(2);
		expect(controller.handleServerData("ls")).toEqual({ write: "" });
	});

	it("keeps an unsent draft visible across asynchronous server output", () => {
		const { controller } = createController();
		controller.handleKeystroke("h");
		controller.handleKeystroke("i");
		expect(controller.handleServerData("status")).toEqual({
			write: "\x1b[2D\x1b[Kstatus\x1b[Khi",
		});
		expect(controller.handleKeystroke("\r")).toEqual({ sendUpstream: "hi", submitUpstream: "\r" });
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

describe("withLineBufferedLocalInput", () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	function createWrapped() {
		const inner = createFakeInnerMux();
		const wrapped = withLineBufferedLocalInput(inner.mux, {});
		const writes: string[] = [];
		wrapped.onData("h", (bytes) => writes.push(new TextDecoder().decode(bytes)));
		return { inner, wrapped, writes };
	}

	it("renders typing locally and sends one complete line on Enter", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		expect(writes).toEqual(["\x1b[Ka"]);
		expect(inner.inputs).toEqual([]);
		wrapped.sendInput("h", "b");
		expect(writes).toEqual(["\x1b[Ka", "b"]);
		expect(inner.inputs).toEqual([]);
		wrapped.sendInput("h", "\r");
		expect(inner.inputs).toEqual(["ab", "\r"]);
		// The matching echo is stripped — nothing renders twice.
		inner.emitData("ab");
		expect(writes).toEqual(["\x1b[Ka", "b"]);
	});

	it("does not predict before the socket is open", () => {
		const { inner, wrapped, writes } = createWrapped();
		wrapped.sendInput("h", "a");
		expect(writes).toEqual([]);
		expect(inner.inputs).toEqual(["a"]);
	});

	it("predicts and streams slash-command lines so the remote completion menu stays live", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		wrapped.sendInput("h", "m");
		wrapped.sendInput("h", "o");
		expect(writes).toEqual(["\x1b[K/", "m", "o"]);
		inner.emitData("/mo");
		// The matching authoritative echo is already visible locally.
		expect(writes).toEqual(["\x1b[K/", "m", "o"]);
		wrapped.sendInput("h", "\r");
		expect(inner.inputs).toEqual(["/", "m", "o", "\r"]);
	});

	it("keeps predicting slash characters after a remote TUI repaint", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		inner.emitData("\x1b[2K\r/");
		// The repaint replaces the optimistic slash without changing how the next
		// ordinary prompt is buffered.
		wrapped.sendInput("h", "m");
		expect(writes).toEqual(["\x1b[K/", "\x1b[2K\r/", "m"]);
		expect(inner.inputs).toEqual(["/", "m"]);
	});

	it("delivers a synchronized TUI repaint atomically when WebSocket chunks split it", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		const firstHalf = "\x1b[?2026h\x1b[24;1H\x1b[J";
		const secondHalf = "\x1b[26;1H›\x1b[26;3H/\x1b[?2026l";

		inner.emitData(firstHalf);
		// A clear without its matching redraw must never reach xterm alone.
		expect(writes).toEqual(["\x1b[K/"]);
		inner.emitData(secondHalf);
		expect(writes).toEqual(["\x1b[K/", firstHalf + secondHalf]);
	});

	it("recognizes a synchronized-output marker split across WebSocket chunks", () => {
		const { inner, writes } = createWrapped();
		inner.emitConnection("open");
		inner.emitData("plain\x1b[?20");
		expect(writes).toEqual(["plain"]);
		inner.emitData("26hframe\x1b[?2026l");
		expect(writes).toEqual(["plain", "\x1b[?2026hframe\x1b[?2026l"]);
	});

	it("resets predictive slash mode when Escape cancels the command menu", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		wrapped.sendInput("h", "m");
		wrapped.sendInput("h", "\x1b");
		expect(writes).toEqual(["\x1b[K/", "m", "\x1b[2D\x1b[K"]);
		expect(inner.inputs).toEqual(["/", "m", "\x1b"]);

		// The next ordinary prompt is line-buffered again rather than remaining
		// stuck in the slash command's per-key streaming mode.
		wrapped.sendInput("h", "a");
		expect(writes).toEqual(["\x1b[K/", "m", "\x1b[2D\x1b[K", "\x1b[Ka"]);
		expect(inner.inputs).toEqual(["/", "m", "\x1b"]);
	});

	it("leaves slash mode when an acknowledged slash is deleted", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		inner.emitData("/");
		wrapped.sendInput("h", DEL);

		// `slashPending` is already empty because the slash was acknowledged. The
		// logical line state must still notice that backspace removed the slash.
		wrapped.sendInput("h", "n");
		expect(writes.at(-1)).toBe("\x1b[Kn");
		expect(inner.inputs).toEqual(["/", DEL]);
	});

	it("leaves slash mode when Ctrl-U clears an acknowledged command line", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		wrapped.sendInput("h", "m");
		inner.emitData("/m");
		wrapped.sendInput("h", "\x15");

		wrapped.sendInput("h", "n");
		expect(writes.at(-1)).toBe("\x1b[Kn");
		expect(inner.inputs).toEqual(["/", "m", "\x15"]);
	});

	it("leaves slash mode when Ctrl-W deletes the slash command word", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		for (const char of "/model") wrapped.sendInput("h", char);
		inner.emitData("/model");
		wrapped.sendInput("h", "\x17");

		wrapped.sendInput("h", "n");
		expect(writes.at(-1)).toBe("\x1b[Kn");
		expect(inner.inputs).toEqual(["/", "m", "o", "d", "e", "l", "\x17"]);
	});

	it("recognizes Enter inside a multi-byte terminal input as the end of slash mode", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		wrapped.sendInput("h", "\x1b\r");

		wrapped.sendInput("h", "n");
		expect(writes.at(-1)).toBe("\x1b[Kn");
		expect(inner.inputs).toEqual(["/", "\x1b\r"]);
	});

	it("never lets a completed slash command disable normal prompt buffering", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "/");
		wrapped.sendInput("h", "m");
		wrapped.sendInput("h", "\r");

		// A late command-selector repaint must not affect ordinary prompt state.
		inner.emitData("\x1b[?2026h\x1b[2Jselector\x1b[?2026l");
		wrapped.sendInput("h", "a");
		expect(writes.at(-1)).toBe("\x1b[Ka");
		expect(inner.inputs).toEqual(["/", "m", "\r"]);
	});

	it("buffers a normal prompt after a slash command opens and closes a selector", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		for (const char of "/model") wrapped.sendInput("h", char);
		inner.emitData("\x1b[?2026hcommand menu\x1b[?2026l");
		wrapped.sendInput("h", "\r"); // open the model selector
		inner.emitData("\x1b[?2026hmodel selector\x1b[?2026l");
		wrapped.sendInput("h", "\x1b[B");
		wrapped.sendInput("h", "\r"); // choose a model
		inner.emitData("\x1b[?2026hprompt\x1b[?2026l");

		for (const char of "hello") wrapped.sendInput("h", char);
		expect(writes.slice(-5)).toEqual(["\x1b[Kh", "e", "l", "l", "o"]);
		// Only slash/selector input reached the PTY. The normal prompt is local
		// until its own Enter is pressed.
		expect(inner.inputs).toEqual(["/", "m", "o", "d", "e", "l", "\r", "\x1b[B", "\r"]);
	});

	it("keeps normal prompting buffered immediately after its own TUI repaint", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		wrapped.sendInput("h", "\r");
		inner.emitData("\x1b[?2026h\x1b[2Jprompt\x1b[?2026l");

		wrapped.sendInput("h", "b");
		expect(writes.at(-1)).toBe("\x1b[Kb");
		expect(inner.inputs).toEqual(["a", "\r"]);
	});

	it("rolls back a prediction whose echo never arrives, on a real timer", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		expect(writes).toEqual(["\x1b[Ka"]);
		wrapped.sendInput("h", "\r");
		vi.advanceTimersByTime(LOCAL_ECHO_PREDICTION_TIMEOUT_MS);
		expect(writes).toEqual(["\x1b[Ka", "\x1b[1D\x1b[K"]);
	});

	it("rolls back and passes server output verbatim on a mismatch", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		wrapped.sendInput("h", "b");
		wrapped.sendInput("h", "\r");
		inner.emitData("Password: ");
		expect(writes).toEqual(["\x1b[Ka", "b", "\x1b[2D\x1b[KPassword: "]);
	});

	it("un-draws pending predictions when the connection drops", () => {
		const { inner, wrapped, writes } = createWrapped();
		inner.emitConnection("open");
		wrapped.sendInput("h", "a");
		inner.emitConnection("closed");
		expect(writes).toEqual(["\x1b[Ka", "\x1b[1D\x1b[K"]);
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
		wrapped.sendInput("h", "\r");
		wrapped.dispose();
		expect(inner.disposed).toBe(true);
		vi.advanceTimersByTime(LOCAL_ECHO_PREDICTION_TIMEOUT_MS);
		// No rollback after dispose: the listener set is gone and the timer cleared.
		expect(writes).toEqual(["\x1b[Ka"]);
	});
});
