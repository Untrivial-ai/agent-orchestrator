// Predictive local echo for cloud session terminals (issue #4763).
//
// A cloud session's PTY lives in a control-plane sandbox, so every keystroke
// rides a full network round trip before its echo comes back — typing feels
// like the RTT. This module renders conservative predictions locally the
// instant a key is pressed and reconciles them against the authoritative
// server echo when it arrives, modeled on VS Code's terminal typeahead and
// mosh:
//
//   - Predict ONLY on xterm's normal buffer. Agent TUIs (claude, and any
//     alt-screen app) repaint aggressively and echo nothing byte-for-byte;
//     the alternate-buffer gate disables prediction there entirely.
//   - Predict ONLY single-column printable characters (ASCII + Latin) and
//     backspace, and backspace only as a local cancel of a still-pending
//     prediction. Everything else (control sequences, paste bursts, wide or
//     combining codepoints) passes through unpredicted.
//   - Reconcile with a FIFO of predicted characters: server data strips the
//     longest matching predicted prefix (that echo is already on screen), and
//     the FIRST mismatch rolls back every remaining prediction — cursor left
//     by the pending count, clear to end of line — then writes the server
//     chunk verbatim and enters a cooldown so a non-echoing program (a
//     password prompt, an app switching modes) stops being predicted into.
//   - A prediction unconfirmed after LOCAL_ECHO_PREDICTION_TIMEOUT_MS rolls
//     back the same way (a dropped echo must not stay on screen forever).
//
// TerminalLocalEchoController is pure TypeScript — clock and buffer type are
// injected — so the reconciliation logic is unit-testable without xterm or a
// socket. withPredictiveLocalEcho wraps a cloud TerminalMux with it, keeping
// useTerminalSession's attach/replay/reconnect machinery unchanged: predicted
// bytes flow to the terminal through the same onData path as server output,
// so ordering with the initial replay is preserved for free.

import type { TerminalMux } from "./terminal-mux";

/**
 * v1 kill switch for predictive local echo on cloud terminals. There is no
 * user-facing terminal-options surface to hang a setting off yet; flip this
 * to false to disable predictions entirely (server echo still renders).
 */
export const LOCAL_ECHO_ENABLED = true;

export type TerminalBufferType = "normal" | "alternate";

/** Roll back a prediction the server has not echoed back within this window. */
export const LOCAL_ECHO_PREDICTION_TIMEOUT_MS = 2_000;
/** After a mismatch or timeout rollback, stop predicting for this long. */
export const LOCAL_ECHO_COOLDOWN_MS = 1_000;
// Ceiling on unconfirmed predictions. Typing runs far below this; only a
// stuck echo path would reach it, and the timeout rollback already owns that.
const MAX_PENDING_PREDICTIONS = 80;

const DEL = "\x7f";

export interface TerminalLocalEchoOptions {
	/** Which xterm buffer is active right now; predictions run only on "normal". */
	bufferType: () => TerminalBufferType;
	/** Clock, injectable for tests. Defaults to Date.now. */
	now?: () => number;
	predictionTimeoutMs?: number;
	cooldownMs?: number;
}

export interface LocalEchoKeystrokeResult {
	/** Predicted bytes to render locally right away; absent when nothing is predicted. */
	localWrite?: string;
	/** Bytes to forward to the remote PTY — always the raw keystroke in v1. */
	sendUpstream: string;
}

export interface LocalEchoWriteResult {
	/** Bytes to render: any rollback first, then the unmatched server remainder. */
	write: string;
}

// One column, one UTF-16 unit: printable ASCII, or the Latin-1 supplement
// through Latin Extended-B (¡–ɏ). The mismatch/timeout rollback moves the
// cursor left by the PENDING COUNT in columns, so a prediction must occupy
// exactly one column — wide (CJK/emoji) and combining codepoints stay
// unpredicted, as do all C0/C1 controls.
function isPredictableCharacter(data: string): boolean {
	if (data.length !== 1) return false;
	const codePoint = data.codePointAt(0) ?? 0;
	return (codePoint >= 0x20 && codePoint <= 0x7e) || (codePoint >= 0xa0 && codePoint <= 0x024f);
}

export class TerminalLocalEchoController {
	private readonly bufferType: () => TerminalBufferType;
	private readonly now: () => number;
	private readonly predictionTimeoutMs: number;
	private readonly cooldownMs: number;
	/** FIFO of predicted characters still awaiting their server echo. */
	private pending: Array<{ char: string; at: number }> = [];
	private cooldownUntil = Number.NEGATIVE_INFINITY;

	constructor(options: TerminalLocalEchoOptions) {
		this.bufferType = options.bufferType;
		this.now = options.now ?? Date.now;
		this.predictionTimeoutMs = options.predictionTimeoutMs ?? LOCAL_ECHO_PREDICTION_TIMEOUT_MS;
		this.cooldownMs = options.cooldownMs ?? LOCAL_ECHO_COOLDOWN_MS;
	}

	get pendingCount(): number {
		return this.pending.length;
	}

	/**
	 * A user keystroke on its way to the PTY. Returns what to render locally
	 * (if the keystroke was predicted) and what to send upstream (always the
	 * raw keystroke — prediction never changes the wire bytes).
	 */
	handleKeystroke(data: string): LocalEchoKeystrokeResult {
		const now = this.now();
		const canPredict = this.bufferType() === "normal" && now >= this.cooldownUntil;
		if (data === DEL) {
			// v1 backspace: only a local cancel of the most recent still-pending
			// prediction. The popped character was already sent upstream, so its
			// echo arrives with no prediction left to consume it — that is fine:
			// the FIFO drains to empty first, and the echoed char plus the
			// server's own erase then render verbatim, converging to the same
			// cell. A backspace with nothing pending stays a plain round trip.
			if (canPredict && this.pending.length > 0) {
				this.pending.pop();
				return { localWrite: "\b \b", sendUpstream: data };
			}
			return { sendUpstream: data };
		}
		if (canPredict && this.pending.length < MAX_PENDING_PREDICTIONS && isPredictableCharacter(data)) {
			this.pending.push({ char: data, at: now });
			return { localWrite: data, sendUpstream: data };
		}
		return { sendUpstream: data };
	}

	/**
	 * A chunk of authoritative server output. Strips the longest prefix that
	 * matches pending predictions (already rendered locally — writing it again
	 * would double-render), rolls back everything on the first mismatch, and
	 * returns the bytes the terminal should actually render.
	 */
	handleServerData(chunk: string): LocalEchoWriteResult {
		if (this.pending.length === 0) return { write: chunk };
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
			// remaining prediction, let the server chunk repaint verbatim, and
			// cool down — this is also the path a non-echoing program (password
			// prompt) takes on its first output.
			return { write: this.rollback() + chunk.slice(index) };
		}
		// Chunk exhausted with predictions left over: they stay pending for the
		// next chunk (or the timeout). Fully matched chunks render nothing new.
		return { write: chunk.slice(index) };
	}

	/**
	 * Roll back all predictions once the oldest has outlived the timeout —
	 * covers dropped echo and programs that never echo (password prompts).
	 * Returns "" when nothing has expired yet.
	 */
	expireStalePredictions(): LocalEchoWriteResult {
		const oldest = this.pending[0];
		if (oldest === undefined || this.now() - oldest.at < this.predictionTimeoutMs) {
			return { write: "" };
		}
		return { write: this.rollback() };
	}

	/** Milliseconds until the oldest prediction expires, or null when none are pending. */
	msUntilTimeout(): number | null {
		const oldest = this.pending[0];
		if (oldest === undefined) return null;
		return Math.max(0, oldest.at + this.predictionTimeoutMs - this.now());
	}

	/**
	 * Un-draw all pending predictions immediately (transport dropped: the next
	 * attachment gets a fresh controller and the server replay would render
	 * the echo of what we already drew).
	 */
	discardPredictions(): LocalEchoWriteResult {
		return { write: this.rollback() };
	}

	// The safest un-draw: the cursor sits right after the last predicted cell
	// (predictions are appends), so move left by the pending count and clear
	// to end of line, then the caller appends the server's verbatim repaint.
	private rollback(): string {
		const count = this.pending.length;
		if (count === 0) return "";
		this.pending = [];
		this.cooldownUntil = this.now() + this.cooldownMs;
		return `\x1b[${count}D\x1b[K`;
	}
}

export interface PredictiveLocalEchoMuxOptions {
	/** Which xterm buffer is active; sampled at each keystroke. */
	bufferType: () => TerminalBufferType;
	/** Clock override for tests. */
	now?: () => number;
}

/**
 * Wrap a cloud TerminalMux with predictive local echo. Predicted bytes and
 * rollbacks are delivered through the wrapper's onData path, so the hook's
 * replay ordering machinery serializes them with real server output; the
 * upstream wire bytes are never altered. Local (loopback) muxes must not be
 * wrapped — their PTY echo is already effectively instant.
 */
export function withPredictiveLocalEcho(
	inner: TerminalMux,
	options: PredictiveLocalEchoMuxOptions,
): TerminalMux {
	const controller = new TerminalLocalEchoController({
		bufferType: options.bufferType,
		now: options.now,
	});
	const encoder = new TextEncoder();
	// Streaming decoder: a UTF-8 codepoint split across WebSocket frames is
	// held until complete, so the controller always compares whole characters
	// and the re-encoded bytes reaching xterm are byte-equivalent to the wire.
	const decoder = new TextDecoder();
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
			const { localWrite, sendUpstream } = controller.handleKeystroke(input);
			if (localWrite !== undefined) {
				emitLocal(localWrite);
				scheduleExpiry();
			}
			inner.sendInput(id, sendUpstream);
		},
		onData: (id, listener) => {
			dataListeners.add(listener);
			// A single inner subscription runs the reconciler exactly once per
			// server chunk no matter how many listeners attach.
			if (innerDataUnsubscribe === null) {
				innerDataUnsubscribe = inner.onData(id, (bytes) => {
					const text = decoder.decode(bytes, { stream: true });
					if (text.length === 0) return;
					const { write } = controller.handleServerData(text);
					scheduleExpiry();
					emitLocal(write);
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
			connectionUnsubscribe();
			innerDataUnsubscribe?.();
			innerDataUnsubscribe = null;
			dataListeners.clear();
			inner.dispose();
		},
	};
}
