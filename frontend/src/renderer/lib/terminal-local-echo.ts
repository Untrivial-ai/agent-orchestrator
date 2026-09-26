// Line-buffered local input for cloud session terminals (issue #4763).
//
// A cloud session's PTY lives in a control-plane sandbox, so every keystroke
// rides a full network round trip before its echo comes back — typing feels
// like the RTT. This module renders a conservative, append-only draft directly
// in xterm, holds those printable keystrokes locally, and sends the complete
// line to the PTY when Enter is pressed:
//
//   - Buffer ONLY single-column printable characters (ASCII + Latin) and local
//     backspace. Commands and agent prompts therefore use the exact same
//     terminal input surface. Paste bursts and control sequences remain atomic
//     and are forwarded immediately; if a draft exists they flush after it in
//     the same frame, preserving byte order.
//   - On Enter, move the visible draft into an awaiting-echo FIFO, then send the
//     draft and Enter as two ordered terminal events. Keeping Enter distinct is
//     important: agent TUIs may interpret `draft + Enter` in one event as a paste
//     that inserts text without submitting it. Server data strips a matching echo
//     (already visible) or rolls the draft back before an authoritative TUI redraw.
//   - Slash-command lines are the exception to line buffering: each printable
//     character is rendered optimistically and streamed to the PTY so its live
//     completion menu still updates. Matching echoes are suppressed, while a TUI
//     repaint replaces the predictions without disabling prediction for the next key.
//   - A submitted draft unconfirmed after LOCAL_ECHO_PREDICTION_TIMEOUT_MS rolls
//     back the same way (a dropped echo must not stay on screen forever).
//
// TerminalLocalEchoController is pure TypeScript — its clock is injected — so
// the reconciliation logic is unit-testable without xterm or a
// socket. withLineBufferedLocalInput wraps a cloud TerminalMux with it, keeping
// useTerminalSession's attach/replay/reconnect machinery unchanged: local
// bytes flow to the terminal through the same onData path as server output,
// so ordering with the initial replay is preserved for free.

import type { TerminalMux } from "./terminal-mux";

/**
 * Kill switch for line-buffered local input on cloud terminals. There is no
 * user-facing terminal-options surface to hang a setting off yet; flip this
 * to false to disable local buffering entirely (server echo still renders).
 */
export const LOCAL_ECHO_ENABLED = true;

/** Roll back a prediction the server has not echoed back within this window. */
export const LOCAL_ECHO_PREDICTION_TIMEOUT_MS = 2_000;
// Bound local memory without prematurely flushing normal long prompts.
const MAX_PENDING_PREDICTIONS = 4_096;
const SYNCHRONIZED_OUTPUT_START = "\x1b[?2026h";
const SYNCHRONIZED_OUTPUT_END = "\x1b[?2026l";
const MAX_SYNCHRONIZED_OUTPUT_BYTES = 1_000_000;

const DEL = "\x7f";

function suffixPrefixLength(value: string, marker: string): number {
	const limit = Math.min(value.length, marker.length - 1);
	for (let length = limit; length > 0; length -= 1) {
		if (value.endsWith(marker.slice(0, length))) return length;
	}
	return 0;
}

/**
 * Codex brackets a TUI repaint with DEC synchronized-output markers, but the
 * transport may split that repaint across several WebSocket frames. Feeding
 * those fragments to xterm independently allows a local prediction to render
 * between the clear and redraw. Reassemble only the explicitly marked region
 * so xterm applies each repaint atomically; ordinary output is unchanged.
 */
class SynchronizedOutputBuffer {
	private buffer = "";
	private synchronized = false;

	push(chunk: string): string[] {
		this.buffer += chunk;
		const output: string[] = [];

		while (this.buffer.length > 0) {
			if (this.synchronized) {
				const endIndex = this.buffer.indexOf(SYNCHRONIZED_OUTPUT_END);
				if (endIndex < 0) {
					if (this.buffer.length > MAX_SYNCHRONIZED_OUTPUT_BYTES) {
						output.push(this.buffer);
						this.clear();
					}
					break;
				}
				const frameEnd = endIndex + SYNCHRONIZED_OUTPUT_END.length;
				output.push(this.buffer.slice(0, frameEnd));
				this.buffer = this.buffer.slice(frameEnd);
				this.synchronized = false;
				continue;
			}

			const startIndex = this.buffer.indexOf(SYNCHRONIZED_OUTPUT_START);
			if (startIndex >= 0) {
				if (startIndex > 0) output.push(this.buffer.slice(0, startIndex));
				this.buffer = this.buffer.slice(startIndex);
				this.synchronized = true;
				continue;
			}

			const retainedLength = suffixPrefixLength(this.buffer, SYNCHRONIZED_OUTPUT_START);
			const emittedLength = this.buffer.length - retainedLength;
			if (emittedLength > 0) output.push(this.buffer.slice(0, emittedLength));
			this.buffer = this.buffer.slice(emittedLength);
			break;
		}

		return output;
	}

	clear(): void {
		this.buffer = "";
		this.synchronized = false;
	}
}

export interface TerminalLocalEchoOptions {
	/** Clock, injectable for tests. Defaults to Date.now. */
	now?: () => number;
	predictionTimeoutMs?: number;
}

export interface LocalEchoKeystrokeResult {
	/** Draft bytes to render locally right away; absent when nothing is buffered. */
	localWrite?: string;
	/** Bytes to forward to the remote PTY; absent while ordinary text stays local. */
	sendUpstream?: string;
	/** Submit key sent as its own terminal event after a buffered line. */
	submitUpstream?: string;
}

export interface LocalEchoWriteResult {
	/** Bytes to render: any rollback first, then the unmatched server remainder. */
	write: string;
}

// One column, one UTF-16 unit: printable ASCII, or the Latin-1 supplement
// through Latin Extended-B (¡–ɏ). The mismatch/timeout rollback moves the
// cursor left by the pending count in columns, so a buffered character must
// occupy exactly one column. Wide and combining codepoints pass through
// atomically instead.
function isPredictableCharacter(data: string): boolean {
	if (data.length !== 1) return false;
	const codePoint = data.codePointAt(0) ?? 0;
	return (codePoint >= 0x20 && codePoint <= 0x7e) || (codePoint >= 0xa0 && codePoint <= 0x024f);
}

export class TerminalLocalEchoController {
	private readonly now: () => number;
	private readonly predictionTimeoutMs: number;
	/** Characters visible locally but not sent to the PTY yet. */
	private draft: Array<{ char: string; at: number }> = [];
	/** Submitted characters still awaiting their authoritative server echo. */
	private pending: Array<{ char: string; at: number }> = [];
	/** Slash predictions are isolated so their repaints cannot affect ordinary prompts. */
	private slashPending: Array<{ char: string; at: number }> = [];
	/**
	 * Local shape of the slash line. This is deliberately independent of
	 * `slashPending`: the latter drains as echoes arrive, while this remains the
	 * source of truth for whether subsequent keys still belong to a slash command.
	 */
	private slashLine: string | null = null;
	/**
	 * Whether the remote terminal currently has bracketed paste mode (DECSET 2004)
	 * enabled, tracked from its output stream. Gates the Enter paste-wrap below: it
	 * is only safe to send ESC[200~..ESC[201~ to a TUI that asked for bracketed
	 * paste; a plain shell would otherwise receive the literal marker bytes.
	 */
	private bracketedPasteActive = false;
	/** Tail of the previous server chunk, so a DECSET 2004 marker split across two chunks is still seen. */
	private pasteMarkerCarry = "";

	constructor(options: TerminalLocalEchoOptions) {
		this.now = options.now ?? Date.now;
		this.predictionTimeoutMs = options.predictionTimeoutMs ?? LOCAL_ECHO_PREDICTION_TIMEOUT_MS;
	}

	get pendingCount(): number {
		return this.draft.length + this.pending.length + this.slashPending.length;
	}

	/**
	 * Track the remote's bracketed paste mode (DECSET 2004) from its output so the
	 * Enter flush can decide whether wrapping the draft is safe. Fed the raw server
	 * chunk before reconciliation; when both markers appear the later one wins.
	 */
	observeServerOutput(text: string): void {
		// Prepend a small carry from the previous chunk so a DECSET 2004 marker
		// split across two WebSocket frames is still detected (the marker is 8
		// bytes; carrying the last 7 covers any split point).
		const scan = this.pasteMarkerCarry + text;
		this.pasteMarkerCarry = scan.slice(-7);
		const enabled = scan.lastIndexOf("\x1b[?2004h");
		const disabled = scan.lastIndexOf("\x1b[?2004l");
		if (enabled === -1 && disabled === -1) return;
		this.bracketedPasteActive = enabled > disabled;
	}

	/**
	 * A user keystroke on its way to the PTY. Printable, single-column input is
	 * held in `draft`; Enter flushes the entire draft, then itself as a distinct
	 * terminal event so remote TUIs treat it as a submit key rather than paste.
	 */
	handleKeystroke(data: string): LocalEchoKeystrokeResult {
		const now = this.now();
		if (this.slashLine !== null) {
			if (data.includes("\r") || data.includes("\n")) {
				this.slashLine = null;
				this.slashPending = [];
				return { sendUpstream: data };
			}
			if (data === "\x1b" || data === "\x03") {
				// Escape/Ctrl-C cancels the command UI. Remove only predictions the
				// server has not confirmed yet; its redraw owns the authoritative line.
				this.slashLine = null;
				const localWrite = this.rollbackSequence(this.slashPending.length);
				this.slashPending = [];
				return localWrite.length > 0
					? { localWrite, sendUpstream: data }
					: { sendUpstream: data };
			}
			if (data === "\x15") {
				// Ctrl-U erases the whole remote line. Once the leading slash is gone,
				// the next printable key must start an ordinary locally buffered prompt.
				this.slashLine = null;
				const localWrite = this.rollbackSequence(this.slashPending.length);
				this.slashPending = [];
				return localWrite.length > 0
					? { localWrite, sendUpstream: data }
					: { sendUpstream: data };
			}
			if (data === "\x17") {
				// Ctrl-W removes the previous word. Track whether that deletion also
				// removed the leading slash so ordinary input cannot inherit this mode.
				this.slashLine = this.slashLine.replace(/\s*\S+\s*$/, "");
				if (this.slashLine.length === 0) this.slashLine = null;
				const localWrite = this.rollbackSequence(this.slashPending.length);
				this.slashPending = [];
				return localWrite.length > 0
					? { localWrite, sendUpstream: data }
					: { sendUpstream: data };
			}
			if (data === DEL) {
				this.slashLine = this.slashLine.slice(0, -1);
				if (this.slashLine.length === 0) this.slashLine = null;
				if (this.slashPending.length > 0) {
					this.slashPending.pop();
					return { localWrite: "\b \b", sendUpstream: data };
				}
				return { sendUpstream: data };
			}
			if (this.slashPending.length < MAX_PENDING_PREDICTIONS && isPredictableCharacter(data)) {
				this.slashLine += data;
				this.slashPending.push({ char: data, at: now });
				return { localWrite: data, sendUpstream: data };
			}
			// Paste bursts and other atomic text still belong to this command line,
			// even though only safe single-column characters are predicted locally.
			if (![...data].some((char) => char < " ")) this.slashLine += data;
			return { sendUpstream: data };
		}
		if (data === "/" && this.draft.length === 0 && this.pending.length === 0) {
			this.slashLine = "/";
			this.slashPending = [{ char: data, at: now }];
			return { localWrite: `\x1b[K${data}`, sendUpstream: data };
		}
		const canBuffer = this.pending.length === 0;
		if (data === DEL) {
			if (canBuffer && this.draft.length > 0) {
				this.draft.pop();
				return { localWrite: "\b \b" };
			}
			return { sendUpstream: data };
		}
		if (canBuffer && this.draft.length < MAX_PENDING_PREDICTIONS && isPredictableCharacter(data)) {
			const firstCharacter = this.draft.length === 0;
			this.draft.push({ char: data, at: now });
			// Agent TUIs often paint placeholder text after the cursor. Clear that
			// decoration when the local draft starts, just as the remote TUI does on
			// its first authoritative input byte.
			return { localWrite: firstCharacter ? `\x1b[K${data}` : data };
		}
		if (this.draft.length > 0) {
			const draft = this.draft;
			this.draft = [];
			this.pending = draft.map(({ char }) => ({ char, at: now }));
			const text = draft.map(({ char }) => char).join("");
			if (data === "\r" || data === "\n") {
				// The flushed line and its Enter reach the worker PTY back-to-back and
				// are usually coalesced into a single read(); an agent TUI (Codex) then
				// reads "text\r" as a paste that inserts the text WITHOUT submitting,
				// so the user has to press Enter a second time. When the remote enabled
				// bracketed paste, wrap the line in paste markers: the TUI ends the
				// paste at ESC[201~ and reads the trailing Enter as a real submit even
				// when the two writes coalesce. Sending Enter as a distinct event still
				// matters for terminals without bracketed paste, so keep it separate.
				const sendUpstream = this.bracketedPasteActive ? `\x1b[200~${text}\x1b[201~` : text;
				return { sendUpstream, submitUpstream: data };
			}
			return { sendUpstream: text + data };
		}
		return { sendUpstream: data };
	}

	/**
	 * A chunk of authoritative server output. Strips the longest prefix that
	 * matches submitted draft characters (already rendered locally — writing it again
	 * would double-render), rolls back everything on the first mismatch, and
	 * returns the bytes the terminal should actually render.
	 */
	handleServerData(chunk: string): LocalEchoWriteResult {
		if (this.slashLine !== null && this.slashPending.length > 0) {
			let index = 0;
			while (this.slashPending.length > 0 && index < chunk.length) {
				if (chunk[index] === this.slashPending[0].char) {
					this.slashPending.shift();
					index += 1;
					continue;
				}
				// Command palettes repaint with absolute cursor movement. Treat that
				// repaint as authoritative without touching normal prompt state.
				this.slashPending = [];
				return { write: chunk.slice(index) };
			}
			return { write: chunk.slice(index) };
		}
		if (this.pending.length === 0) {
			if (this.draft.length === 0) return { write: chunk };
			const draft = this.draft.map(({ char }) => char).join("");
			return { write: this.rollbackSequence(this.draft.length) + chunk + `\x1b[K${draft}` };
		}
		let index = 0;
		while (this.pending.length > 0 && index < chunk.length) {
			// Predictions are single UTF-16 units by construction (see
			// isPredictableCharacter), so a unit-by-unit compare is exact.
			if (chunk[index] === this.pending[0].char) {
				this.pending.shift();
				index += 1;
				continue;
			}
			// First mismatch: the screen is ahead of reality. Un-draw every
			// remaining prediction and let the server chunk repaint verbatim. This
			// is also the path a non-echoing program (password prompt) takes on its
			// first output. The next prompt may begin buffering
			// immediately; an authoritative repaint must not create a remote-only gap.
			return { write: this.rollback() + chunk.slice(index) };
		}
		// Chunk exhausted with predictions left over: they stay pending for the
		// next chunk (or the timeout). Fully matched chunks render nothing new.
		return { write: chunk.slice(index) };
	}

	/**
	 * Roll back a submitted draft once its oldest character outlives the timeout —
	 * covers dropped echo and programs that never echo (password prompts).
	 * Returns "" when nothing has expired yet.
	 */
	expireStalePredictions(): LocalEchoWriteResult {
		const oldestSlash = this.slashPending[0];
		if (
			oldestSlash !== undefined &&
			this.now() - oldestSlash.at >= this.predictionTimeoutMs
		) {
			const count = this.slashPending.length;
			this.slashPending = [];
			return { write: this.rollbackSequence(count) };
		}
		const oldest = this.pending[0];
		if (oldest === undefined || this.now() - oldest.at < this.predictionTimeoutMs) {
			return { write: "" };
		}
		return { write: this.rollback() };
	}

	/** Milliseconds until the oldest prediction expires, or null when none are pending. */
	msUntilTimeout(): number | null {
		const oldest = this.pending[0] ?? this.slashPending[0];
		if (oldest === undefined) return null;
		return Math.max(0, oldest.at + this.predictionTimeoutMs - this.now());
	}

	/**
	 * Un-draw all local input immediately (transport dropped: the next
	 * attachment gets a fresh controller and the server replay would render
	 * the echo of what we already drew).
	 */
	discardPredictions(): LocalEchoWriteResult {
		const count = this.pending.length + this.draft.length + this.slashPending.length;
		this.pending = [];
		this.draft = [];
		this.slashPending = [];
		this.slashLine = null;
		return { write: this.rollbackSequence(count) };
	}

	// The safest un-draw: the cursor sits right after the last predicted cell
	// (predictions are appends), so move left by the pending count and clear
	// to end of line, then the caller appends the server's verbatim repaint.
	private rollback(): string {
		const count = this.pending.length;
		if (count === 0) return "";
		this.pending = [];
		return this.rollbackSequence(count);
	}

	private rollbackSequence(count: number): string {
		return count > 0 ? `\x1b[${count}D\x1b[K` : "";
	}
}

export interface LineBufferedLocalInputMuxOptions {
	/** Clock override for tests. */
	now?: () => number;
}

/**
 * Wrap a cloud TerminalMux with line-buffered local input. Draft bytes and
 * rollbacks are delivered through the wrapper's onData path, so the hook's
 * replay ordering machinery serializes them with real server output; the
 * upstream wire bytes are never altered. Local (loopback) muxes must not be
 * wrapped — their PTY echo is already effectively instant.
 */
export function withLineBufferedLocalInput(
	inner: TerminalMux,
	options: LineBufferedLocalInputMuxOptions,
): TerminalMux {
	const controller = new TerminalLocalEchoController({
		now: options.now,
	});
	const encoder = new TextEncoder();
	// Streaming decoder: a UTF-8 codepoint split across WebSocket frames is
	// held until complete, so the controller always compares whole characters
	// and the re-encoded bytes reaching xterm are byte-equivalent to the wire.
	const decoder = new TextDecoder();
	const synchronizedOutput = new SynchronizedOutputBuffer();
	const dataListeners = new Set<(bytes: Uint8Array) => void>();
	let innerDataUnsubscribe: (() => void) | null = null;
	let expiryTimer: ReturnType<typeof setTimeout> | null = null;
	let connectionOpen = false;
	let disposed = false;

	const emitLocal = (text: string): void => {
		if (text.length === 0 || disposed) return;
		const bytes = encoder.encode(text);
		dataListeners.forEach((listener) => listener(bytes));
	};

	const clearExpiryTimer = (): void => {
		if (expiryTimer !== null) {
			clearTimeout(expiryTimer);
			expiryTimer = null;
		}
	};

	// One timer aimed at the oldest pending prediction; re-aimed whenever the
	// FIFO head can have changed. Fires the timeout rollback even when no
	// further keystrokes or server chunks ever arrive (dropped echo).
	const scheduleExpiry = (): void => {
		clearExpiryTimer();
		const delay = controller.msUntilTimeout();
		if (delay === null) return;
		expiryTimer = setTimeout(() => {
			expiryTimer = null;
			emitLocal(controller.expireStalePredictions().write);
			scheduleExpiry();
		}, delay);
	};

	const connectionUnsubscribe = inner.onConnectionChange((state) => {
		connectionOpen = state === "open";
		if (!connectionOpen) {
			// Registered before the hook's own connection listener, so this
			// un-draw lands ahead of any teardown the drop triggers.
			synchronizedOutput.clear();
			emitLocal(controller.discardPredictions().write);
			scheduleExpiry();
		}
	});

	return {
		open: (id, cols, rows) => inner.open(id, cols, rows),
		resize: (id, cols, rows, force) => inner.resize(id, cols, rows, force),
		close: (id) => inner.close(id),
		onExit: (id, listener) => inner.onExit(id, listener),
		onOpened: (id, listener) => inner.onOpened(id, listener),
		onReplayComplete: inner.onReplayComplete
			? (id, listener) => inner.onReplayComplete!(id, listener)
			: undefined,
		onError: (id, listener) => inner.onError(id, listener),
		onConnectionChange: (listener) => inner.onConnectionChange(listener),
		sendInput: (id, input) => {
			// Before the socket is open the mux only queues input; the echo (and
			// any replay) will arrive much later, so predicting here would just
			// guarantee a rollback. Pass through untouched.
			if (!connectionOpen) {
				inner.sendInput(id, input);
				return;
			}
			const { localWrite, sendUpstream, submitUpstream } = controller.handleKeystroke(input);
			if (localWrite !== undefined) {
				emitLocal(localWrite);
				scheduleExpiry();
			}
			if (sendUpstream !== undefined) {
				inner.sendInput(id, sendUpstream);
				scheduleExpiry();
			}
			if (submitUpstream !== undefined) {
				inner.sendInput(id, submitUpstream);
			}
		},
		onData: (id, listener) => {
			dataListeners.add(listener);
			// A single inner subscription runs the reconciler exactly once per
			// server chunk no matter how many listeners attach.
			if (innerDataUnsubscribe === null) {
				innerDataUnsubscribe = inner.onData(id, (bytes) => {
					const text = decoder.decode(bytes, { stream: true });
					if (text.length === 0) return;
					// Track bracketed paste mode from the raw stream before the
					// synchronized-output buffer can hold part of it back.
					controller.observeServerOutput(text);
					for (const output of synchronizedOutput.push(text)) {
						const { write } = controller.handleServerData(output);
						emitLocal(write);
					}
					scheduleExpiry();
				});
			}
			return () => {
				dataListeners.delete(listener);
			};
		},
		dispose: () => {
			if (disposed) return;
			disposed = true;
			clearExpiryTimer();
			synchronizedOutput.clear();
			connectionUnsubscribe();
			innerDataUnsubscribe?.();
			innerDataUnsubscribe = null;
			dataListeners.clear();
			inner.dispose();
		},
	};
}
