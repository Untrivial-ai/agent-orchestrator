// Terminal Attachment (see CONTEXT.md): the live binding between a terminal
// pane and a PTY over the mux. The hook owns the whole attachment lifecycle —
// open ordering, auto-reattach with backoff, error surfacing, and exit
// handling — so the pane component only renders.
//
// The PTY is either an agent session's pane (the default) or a standalone
// shell terminal the user opened by hand (options.shellTerminalHandleId). The
// mux draws no distinction between them, so only the handle's source and the
// session-specific side effects branch below.
//
// Status rule: the frontend never writes a session's display status. On mux
// `exited`/`error` it invalidates the workspaces query and lets the daemon's
// derived status flow back (docs/architecture.md).

import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { getApiBaseUrl } from "../lib/api-client";
import { captureRendererEvent } from "../lib/telemetry";
import { createTerminalMux, muxUrlFromApiBase, type TerminalMux } from "../lib/terminal-mux";
import { sessionIsActive, type WorkspaceSession } from "../types/workspace";
import { workspaceQueryKey } from "./useWorkspaceQuery";

/**
 * The slice of xterm's Terminal the attachment needs. Structural, so tests can
 * drive the hook with a tiny fake instead of a real xterm + DOM.
 */
export type TerminalUserInputSource = "keyboard" | "paste" | "composition" | "shortcut" | "wheel";

export type AttachableTerminal = {
	cols: number;
	rows: number;
	/**
	 * `done` fires once this exact chunk has been parsed into the buffer (xterm's
	 * own write callback). The attachment uses it to reveal the pane at the
	 * replay's final scroll position instead of guessing with a timer.
	 */
	write: (data: Uint8Array, done?: () => void) => void;
	writeln: (line: string) => void;
	/** Move xterm's logical viewport and DOM scrollbar to the latest output. */
	showLatestOutput: () => void;
	/**
	 * Fit a retained terminal and move it to the latest output while its
	 * container is still non-visible. Resolves only after xterm has rendered the
	 * bottom viewport and crossed a paint boundary, so the owner can reveal
	 * without exposing an intermediate row.
	 */
	prepareForActivation: () => Promise<void>;
	onUserInput: (listener: (data: string, source: TerminalUserInputSource) => void) => { dispose: () => void };
	onResize: (listener: (size: { cols: number; rows: number }) => void) => { dispose: () => void };
};

export type TerminalSessionState =
	| "idle" // nothing attached (no session, or detached)
	| "connecting" // first attach in flight
	| "attached" // server acked the open
	| "reattaching" // socket dropped; waiting on backoff or daemon readiness
	| "exited" // PTY process ended; terminal kept for scrollback
	| "error"; // server reported a pane error; no automatic retry

export type UseTerminalSessionOptions = {
	/** Gates auto-reattach: when false, a dropped socket waits instead of retrying. */
	daemonReady: boolean;
	/** Refuse user bytes without detaching while a controller handoff owns input. */
	inputDisabled?: boolean;
	/** Coalesce and cover the initial replay. Disable for non-retained reviewer panes. */
	coverInitialReplay?: boolean;
	/**
	 * False while a retained terminal is parked off screen. Output and transport
	 * recovery continue, but hidden panes cannot send user input or PTY resizes.
	 */
	isVisible?: boolean;
	/** Test seam: build the mux client. Defaults to a fresh socket against the current API base. */
	createMux?: () => TerminalMux;
	/**
	 * Attach to a standalone shell terminal (POST /api/v1/shell-terminals)
	 * instead of a session's pane. When set it wins over `session`, which
	 * callers pass as undefined for shell panes.
	 *
	 * The mux needs no distinction between the two: it treats the id it is
	 * given as an opaque runtime handle either way. Everything downstream of
	 * `handle` in this hook is therefore shared verbatim; only the handle's
	 * source and the session-specific side effects differ.
	 */
	shellTerminalHandleId?: string;
};

const RETRY_BASE_MS = 500;
const RETRY_MAX_MS = 8_000;
const OPEN_TIMEOUT_MS = 3_000;
// Trailing debounce on grid changes: a pane drag emits a burst of intermediate
// sizes; the attached program should get one SIGWINCH when the drag settles,
// not dozens (yyork's terminal-panel does the same at its socket layer).
const RESIZE_DEBOUNCE_MS = 100;
// Initial-replay gate. On attach the runtime replays the pane's state, and the
// daemon pumps it in 32KB reads (attachment.go copyOut) — so the renderer gets
// N WebSocket frames, N `write()` calls, and N separate event-loop turns. xterm
// parses each write atomically but the browser paints BETWEEN turns, so every
// frame boundary is a painted, further-scrolled state: the terminal visibly
// walks from mid-session down to the tail. Measured on a 1000-line replay: 25
// frames at 16ms spacing paint 25 distinct scroll positions; the same bytes as
// ONE write paint exactly 1, for ~2ms of parse.
//
// The first burst is buffered, joined in wire order, then parsed in bounded
// batches behind one cover. QUIET_MS or CAP_MS ends that coalesced phase; xterm
// then consumes late tail frames in order behind the same cover. The tail
// quiet/cap below decides when to reveal at the bottom.
const REPLAY_QUIET_MS = 60;
const REPLAY_CAP_MS = 750;
// After the coalesced replay batches land, keep the cover up while late replay
// frames stream into xterm. This removes the visible first-open walk without
// retaining another large duplicate replay buffer. A second cap bounds the
// cover for an agent that is already producing live output continuously.
const REPLAY_TAIL_QUIET_MS = 180;
const REPLAY_TAIL_CAP_MS = 750;
// Byte ceiling on the buffered burst. Attaching to a pane that is actively
// streaming (an agent mid-run) means every frame restarts the quiet window, so
// the time bounds alone would hold the entire burst. The byte cap limits the
// duplicate JS buffer; the write-batch cap below separately limits each xterm
// parser task. Real replays are far below this; only a pathological stream trips
// it, and tripping it ends only the coalesced phase: later bytes still render
// behind the cover until the tail settles.
const REPLAY_MAX_BYTES = 1024 * 1024;
// A replay below the byte ceiling can still be large enough to monopolize the
// renderer in one xterm parser task. Feed it in bounded writes behind the same
// cover and yield between them; the user still sees one final reveal while
// fullscreen/window input remains responsive.
const REPLAY_WRITE_BATCH_BYTES = 256 * 1024;
// Cover-only grace on the first replay byte. A pane that has produced NOTHING
// has no walk to hide, so holding the cover to the cap just shows a blank
// overlay — and past the pane's label delay (REPLAY_COVER_LABEL_MS in
// TerminalPane.tsx) a "Loading latest output…" label with nothing to load.
// Uncovering on this timer keeps that window short.
//
// It lifts the cover WITHOUT ending the gate: flushing here instead would clear
// `replayBuffering`, and a replay that then arrives late would go to xterm frame
// by frame — the exact walk this gate removes, with no cover left over it. So
// the burst stays coalesced either way and a late one lands as a single paint.
// Longer than the quiet window on purpose: `opened` fires from setPTY before
// copyOut, so the first byte is imminent but not instant, and a value near
// QUIET_MS would uncover panes that were about to draw.
const REPLAY_FIRST_BYTE_MS = 250;
// Steady-state batching: coalesce bytes that arrive within the same
// animation frame into a single term.write() call. Mirrors the initial-
// replay buffering above, but bounded to one frame instead of a quiet
// window — an actively streaming pane must not wait for a lull. Without
// this, a PTY output burst (several WS frames landing in separate browser
// tasks) triggers a separate term.write() + xterm repaint per frame, and
// the browser can paint BETWEEN those writes — the same frame-boundary
// tearing the replay gate exists to avoid, but during live activity
// instead of only at attach. Bounded so a never-idle stream still flushes
// regularly instead of growing the buffer without limit.
const STEADY_WRITE_MAX_BYTES = 256 * 1024;

function defaultCreateMux(): TerminalMux {
	// Resolved per connect, not per hook: a daemon restart can change the port.
	return createTerminalMux(muxUrlFromApiBase(getApiBaseUrl()));
}

export function useTerminalSession(session: WorkspaceSession | undefined, options: UseTerminalSessionOptions) {
	const queryClient = useQueryClient();
	const [state, setState] = useState<TerminalSessionState>("idle");
	const [error, setError] = useState<string | undefined>(undefined);
	// False only while the initial replay is being buffered — the pane keeps a
	// cover over xterm until the burst has been written and parsed.
	const [replaySettled, setReplaySettled] = useState(true);

	const sessionRef = useRef(session);
	sessionRef.current = session;
	const previousSessionActiveRef = useRef(session ? sessionIsActive(session) : false);
	const previousActivityStateRef = useRef(session?.activity?.state);
	const optionsRef = useRef(options);
	optionsRef.current = options;
	const stateRef = useRef<TerminalSessionState>(state);
	const connectRef = useRef<() => void>(() => undefined);

	const runtime = useRef({
		terminal: null as AttachableTerminal | null,
		mux: null as TerminalMux | null,
		handle: null as string | null,
		disposers: [] as Array<() => void>,
		retryTimer: null as ReturnType<typeof setTimeout> | null,
		openTimer: null as ReturnType<typeof setTimeout> | null,
		resizeTimer: null as ReturnType<typeof setTimeout> | null,
		// Last positive grid claimed by this attachment. This is deliberately
		// separate from xterm's local grid: hidden fits must not resize the PTY, and
		// repeated identical visible fits must not manufacture another SIGWINCH.
		lastPublishedGrid: null as { cols: number; rows: number } | null,
		attempts: 0,
		generation: 0,
		inputReady: false,
		detached: true,
		// True only after this attachment opens parked at 0×0. The next visible
		// activation must promote it back to a positive primary grid.
		needsVisibleSizeSync: false,
		// Initial-replay gate, reset per connect (see REPLAY_QUIET_MS).
		replayBuffering: false,
		replayChunks: [] as Uint8Array[],
		replayBytes: 0,
		replayQuietTimer: null as ReturnType<typeof setTimeout> | null,
		replayCapTimer: null as ReturnType<typeof setTimeout> | null,
		// Uncovers a pane that never produced a first byte; see
		// REPLAY_FIRST_BYTE_MS. Does not end the gate, so it is not a flush timer.
		replayFirstByteTimer: null as ReturnType<typeof setTimeout> | null,
		replayTailQuietTimer: null as ReturnType<typeof setTimeout> | null,
		replayTailCapTimer: null as ReturnType<typeof setTimeout> | null,
		replayTailPending: false,
		// The current attachment's flush, published so teardown can land buffered
		// bytes instead of discarding them (the closure lives inside connect).
		flushReplay: null as ((preserveBeforeTeardown?: boolean) => void) | null,
		// Steady-state write coalescing (see STEADY_WRITE_MAX_BYTES). Runs for
		// the whole life of an attachment, not just the initial replay burst.
		steadyWriteChunks: [] as Uint8Array[],
		steadyWriteBytes: 0,
		steadyWriteRaf: null as number | null,
		flushSteadyWrite: null as (() => void) | null,
	});

	const transition = useCallback((next: TerminalSessionState) => {
		stateRef.current = next;
		setState(next);
	}, []);

	const invalidateWorkspaces = useCallback(() => {
		// A standalone shell has no session row behind it, so its exit carries no
		// news for the session board. Refetching every workspace on `exit` would
		// be pure churn — the shell terminal list owns that pane's fate instead.
		if (optionsRef.current.shellTerminalHandleId) return;
		void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
	}, [queryClient]);

	const clearReplayTimers = useCallback(() => {
		const r = runtime.current;
		if (r.replayQuietTimer) {
			clearTimeout(r.replayQuietTimer);
			r.replayQuietTimer = null;
		}
		if (r.replayCapTimer) {
			clearTimeout(r.replayCapTimer);
			r.replayCapTimer = null;
		}
		if (r.replayFirstByteTimer) {
			clearTimeout(r.replayFirstByteTimer);
			r.replayFirstByteTimer = null;
		}
		if (r.replayTailQuietTimer) {
			clearTimeout(r.replayTailQuietTimer);
			r.replayTailQuietTimer = null;
		}
		if (r.replayTailCapTimer) {
			clearTimeout(r.replayTailCapTimer);
			r.replayTailCapTimer = null;
		}
	}, []);

	const teardownMux = useCallback(() => {
		const r = runtime.current;
		// Land anything still buffered before the attachment goes away. Dropping
		// it would lose output that had already arrived. No-ops when the gate is
		// closed or already superseded.
		r.flushReplay?.(true);
		r.flushReplay = null;
		r.flushSteadyWrite?.();
		r.flushSteadyWrite = null;
		if (r.steadyWriteRaf !== null) {
			cancelAnimationFrame(r.steadyWriteRaf);
			r.steadyWriteRaf = null;
		}
		r.steadyWriteChunks = [];
		r.steadyWriteBytes = 0;
		clearReplayTimers();
		r.replayBuffering = false;
		r.replayChunks = [];
		r.replayBytes = 0;
		r.replayTailPending = false;
		r.lastPublishedGrid = null;
		// Nothing is buffering any more, so nothing should stay covered. connect()
		// re-arms the gate immediately after calling this, in the same tick, so
		// the reveal here never flashes. Without it, a teardown that does not
		// reconnect (open timeout while the daemon is down) strands the pane
		// behind the cover — the cap no longer covers that case now it is armed
		// from `opened` rather than from connect().
		setReplaySettled(true);
		if (r.retryTimer) {
			clearTimeout(r.retryTimer);
			r.retryTimer = null;
		}
		if (r.openTimer) {
			clearTimeout(r.openTimer);
			r.openTimer = null;
		}
		if (r.resizeTimer) {
			clearTimeout(r.resizeTimer);
			r.resizeTimer = null;
		}
		r.inputReady = false;
		if (r.mux && r.handle) {
			r.mux.close(r.handle);
		}
		r.disposers.forEach((dispose) => dispose());
		r.disposers = [];
		r.mux?.dispose();
		r.mux = null;
	}, [clearReplayTimers]);

	const isCurrentAttachment = useCallback((generation: number, handle: string, mux: TerminalMux) => {
		const r = runtime.current;
		return !r.detached && r.generation === generation && r.handle === handle && r.mux === mux;
	}, []);

	const clearOpenTimer = useCallback((generation: number) => {
		const r = runtime.current;
		if (r.generation !== generation || !r.openTimer) return;
		clearTimeout(r.openTimer);
		r.openTimer = null;
	}, []);

	const scheduleReattach = useCallback(() => {
		const r = runtime.current;
		if (r.detached || !r.terminal || !r.handle) {
			return;
		}
		// A socket dropping after the PTY ended (or errored) changes nothing.
		if (stateRef.current === "exited" || stateRef.current === "error") {
			return;
		}
		transition("reattaching");
		// Not ready → no timer; the daemonReady effect reconnects when it flips.
		if (!optionsRef.current.daemonReady) {
			return;
		}
		if (r.retryTimer) {
			return;
		}
		const delay = Math.min(RETRY_BASE_MS * 2 ** r.attempts, RETRY_MAX_MS);
		r.attempts += 1;
		r.retryTimer = setTimeout(() => {
			r.retryTimer = null;
			connectRef.current();
		}, delay);
	}, [transition]);

	const connect = useCallback(() => {
		const r = runtime.current;
		const { terminal, handle } = r;
		if (!terminal || !handle || r.detached) {
			return;
		}
		// Flush the outgoing attachment BEFORE bumping the generation: past that
		// point its own guard rejects the flush and its buffered bytes are lost.
		r.flushReplay?.(true);
		r.flushSteadyWrite?.();
		const generation = r.generation + 1;
		r.generation = generation;
		r.inputReady = false;
		teardownMux();

		const mux = (optionsRef.current.createMux ?? defaultCreateMux)();
		r.mux = mux;

		let pendingReplayWrites = 0;
		let replayRevealDeadlineReached = false;
		const postReplayWriteQueue: Uint8Array[] = [];
		let postReplayWriteActive = false;
		let replayBatchBytes: Uint8Array | null = null;
		let replayBatchOffset = 0;
		let replayBatchTimer: ReturnType<typeof setTimeout> | null = null;
		let replayBatchDone: (() => void) | null = null;
		let replayWritesPreserved = false;

		// Reveal only after xterm has parsed the coalesced replay and any late tail
		// frames have gone quiet. The tail itself streams straight into xterm behind
		// the cover, avoiding an unbounded duplicate replay buffer.
		const revealReplayTail = () => {
			if (!r.replayTailPending || !isCurrentAttachment(generation, handle, mux)) return;
			if (r.replayTailQuietTimer) {
				clearTimeout(r.replayTailQuietTimer);
				r.replayTailQuietTimer = null;
			}
			if (r.replayTailCapTimer) {
				clearTimeout(r.replayTailCapTimer);
				r.replayTailCapTimer = null;
			}
			if (pendingReplayWrites > 0) {
				replayRevealDeadlineReached = true;
				return;
			}
			r.replayTailPending = false;
			replayRevealDeadlineReached = false;
			terminal.showLatestOutput();
			setReplaySettled(true);
		};
		const scheduleReplayTailReveal = () => {
			if (!r.replayTailPending || !isCurrentAttachment(generation, handle, mux)) return;
			if (pendingReplayWrites > 0) return;
			if (replayRevealDeadlineReached) {
				revealReplayTail();
				return;
			}
			if (r.replayTailQuietTimer) clearTimeout(r.replayTailQuietTimer);
			r.replayTailQuietTimer = setTimeout(revealReplayTail, REPLAY_TAIL_QUIET_MS);
		};
		const holdReplayTail = () => {
			r.replayTailPending = true;
			if (!r.replayTailCapTimer) {
				r.replayTailCapTimer = setTimeout(revealReplayTail, REPLAY_TAIL_CAP_MS);
			}
		};
		const writeReplayBatches = (bytes: Uint8Array, done: () => void) => {
			replayBatchBytes = bytes;
			replayBatchOffset = 0;
			replayBatchDone = done;
			const writeNext = () => {
				replayBatchTimer = null;
				if (replayWritesPreserved) return;
				if (!isCurrentAttachment(generation, handle, mux)) return;
				const current = replayBatchBytes;
				if (!current) return;
				const end = Math.min(current.length, replayBatchOffset + REPLAY_WRITE_BATCH_BYTES);
				const batch = current.subarray(replayBatchOffset, end);
				replayBatchOffset = end;
				terminal.write(batch, () => {
					if (replayWritesPreserved) return;
					if (!isCurrentAttachment(generation, handle, mux)) return;
					if (replayBatchOffset >= current.length) {
						const finished = replayBatchDone;
						replayBatchBytes = null;
						replayBatchOffset = 0;
						replayBatchDone = null;
						finished?.();
						return;
					}
					replayBatchTimer = setTimeout(writeNext, 0);
				});
			};
			writeNext();
		};
		const preservePendingReplayWrites = () => {
			if (replayWritesPreserved) return;
			replayWritesPreserved = true;
			if (replayBatchTimer !== null) {
				clearTimeout(replayBatchTimer);
				replayBatchTimer = null;
			}
			if (replayBatchBytes && replayBatchOffset < replayBatchBytes.length) {
				// The current batch is already in xterm's queue. Queue the remainder in
				// one call before dispose so it cannot be overtaken or discarded.
				terminal.write(replayBatchBytes.subarray(replayBatchOffset));
			}
			replayBatchBytes = null;
			replayBatchOffset = 0;
			replayBatchDone = null;
			for (const bytes of postReplayWriteQueue) terminal.write(bytes);
			postReplayWriteQueue.length = 0;
			postReplayWriteActive = false;
			pendingReplayWrites = 0;
			r.replayTailPending = false;
			terminal.showLatestOutput();
			setReplaySettled(true);
		};
		const settleAfterReplayWrites = () => {
			if (pendingReplayWrites > 0 || postReplayWriteActive || postReplayWriteQueue.length > 0) return;
			if (r.replayTailPending) {
				scheduleReplayTailReveal();
				return;
			}
			terminal.showLatestOutput();
			setReplaySettled(true);
		};
		const drainPostReplayWrites = () => {
			if (!isCurrentAttachment(generation, handle, mux)) return;
			if (pendingReplayWrites > 0 || postReplayWriteActive) return;
			const bytes = postReplayWriteQueue.shift();
			if (!bytes) {
				settleAfterReplayWrites();
				return;
			}
			postReplayWriteActive = true;
			pendingReplayWrites += 1;
			terminal.write(bytes, () => {
				if (replayWritesPreserved) return;
				if (!isCurrentAttachment(generation, handle, mux)) return;
				postReplayWriteActive = false;
				pendingReplayWrites = Math.max(0, pendingReplayWrites - 1);
				drainPostReplayWrites();
			});
		};

		// End the buffered part of the initial replay: concatenate what arrived so
		// far and feed it through bounded writes. Bytes arriving during those yields
		// queue behind the replay so ANSI/VT commands remain in exact wire order.
		// Normal quiet/cap flushes keep the cover for the tail. Explicit input settles
		// after the ordered queue drains; teardown synchronously submits any remainder
		// to xterm before releasing the attachment.
		//
		// Safe to call from anywhere — a second call is a no-op, and a call from
		// a superseded attachment is dropped.
		const flushReplay = (holdTail = false, preserveBeforeTeardown = false) => {
			if (!r.replayBuffering) {
				if (preserveBeforeTeardown) preservePendingReplayWrites();
				return;
			}
			if (!isCurrentAttachment(generation, handle, mux)) return;
			r.replayBuffering = false;
			clearReplayTimers();
			if (holdTail) holdReplayTail();

			const chunks = r.replayChunks;
			r.replayChunks = [];
			r.replayBytes = 0;

			if (chunks.length === 0) {
				// Nothing buffered, so there is nothing to reveal at a settled
				// position — just make sure the cover is off. Reaching here means an
				// exit, error, dead socket or the cap; a pane that is merely slow to
				// draw was already uncovered by REPLAY_FIRST_BYTE_MS without ending
				// the gate.
				if (holdTail) scheduleReplayTailReveal();
				else setReplaySettled(true);
				return;
			}

			let total = 0;
			for (const chunk of chunks) total += chunk.length;
			const replay = new Uint8Array(total);
			let offset = 0;
			for (const chunk of chunks) {
				replay.set(chunk, offset);
				offset += chunk.length;
			}
			if (preserveBeforeTeardown) {
				terminal.write(replay);
				preservePendingReplayWrites();
				return;
			}
			pendingReplayWrites += 1;
			writeReplayBatches(replay, () => {
				if (!isCurrentAttachment(generation, handle, mux)) return;
				pendingReplayWrites = Math.max(0, pendingReplayWrites - 1);
				drainPostReplayWrites();
			});
		};
		r.flushReplay = (preserveBeforeTeardown = false) =>
			flushReplay(false, preserveBeforeTeardown);
		// See STEADY_WRITE_MAX_BYTES. mux.onData feeds this instead of writing
		// directly once the replay gate is no longer buffering.
		const flushSteadyWrite = () => {
			if (r.steadyWriteRaf !== null) {
				cancelAnimationFrame(r.steadyWriteRaf);
				r.steadyWriteRaf = null;
			}
			const chunks = r.steadyWriteChunks;
			if (chunks.length === 0) return;
			r.steadyWriteChunks = [];
			r.steadyWriteBytes = 0;
			if (chunks.length === 1) {
				terminal.write(chunks[0]);
				return;
			}
			let total = 0;
			for (const chunk of chunks) total += chunk.length;
			const merged = new Uint8Array(total);
			let offset = 0;
			for (const chunk of chunks) {
				merged.set(chunk, offset);
				offset += chunk.length;
			}
			terminal.write(merged);
		};
		r.flushSteadyWrite = flushSteadyWrite;

		r.disposers.push(
			mux.onData(handle, (bytes) => {
				if (!isCurrentAttachment(generation, handle, mux)) return;
				if (r.replayBuffering) {
					r.replayChunks.push(bytes);
					r.replayBytes += bytes.length;
					// A stream that never idles would restart the quiet window forever
					// and grow the buffer without bound; flush on size instead.
					if (r.replayBytes >= REPLAY_MAX_BYTES) {
						flushReplay(true);
						return;
					}
					// Each frame restarts the quiet window: the burst is over only
					// once the stream actually goes idle.
					if (r.replayQuietTimer) clearTimeout(r.replayQuietTimer);
					r.replayQuietTimer = setTimeout(() => flushReplay(true), REPLAY_QUIET_MS);
					return;
				}
				if (pendingReplayWrites > 0 || postReplayWriteActive || r.replayTailPending) {
					// Preserve wire order while the bounded initial replay is yielding,
					// and do not let a previous quiet deadline reveal before this newer
					// chunk has been parsed by xterm.
					if (r.replayTailQuietTimer) {
						clearTimeout(r.replayTailQuietTimer);
						r.replayTailQuietTimer = null;
					}
					postReplayWriteQueue.push(bytes);
					drainPostReplayWrites();
					return;
				}
				r.steadyWriteChunks.push(bytes);
				r.steadyWriteBytes += bytes.length;
				if (r.steadyWriteBytes >= STEADY_WRITE_MAX_BYTES) {
					flushSteadyWrite();
					return;
				}
				if (r.steadyWriteRaf === null) {
					r.steadyWriteRaf = requestAnimationFrame(flushSteadyWrite);
				}
			}),
			mux.onOpened(handle, () => {
				if (!isCurrentAttachment(generation, handle, mux)) return;
				clearOpenTimer(generation);
				r.inputReady = true;
				r.attempts = 0;
				setError(undefined);
				transition("attached");
				// Bound the gate from here: the daemon fires onOpen from setPTY and
				// starts copyOut immediately after, so the replay is imminent and
				// the cap now measures the burst rather than the connect handshake.
				if (r.replayBuffering && !r.replayCapTimer) {
					r.replayCapTimer = setTimeout(() => flushReplay(true), REPLAY_CAP_MS);
				}
				// Same anchor, different job: uncover a pane that turns out to have
				// nothing to replay (see REPLAY_FIRST_BYTE_MS). Deliberately not a
				// flush — the gate stays armed so a late burst is still coalesced.
				if (r.replayBuffering && !r.replayFirstByteTimer) {
					r.replayFirstByteTimer = setTimeout(() => {
						r.replayFirstByteTimer = null;
						if (!isCurrentAttachment(generation, handle, mux)) return;
						// Bytes arrived, or the burst already flushed: the flush owns
						// the reveal in both cases, at the settled scroll position.
						if (!r.replayBuffering || r.replayChunks.length > 0) return;
						setReplaySettled(true);
					}, REPLAY_FIRST_BYTE_MS);
				}
			}),
			mux.onExit(handle, () => {
				if (!isCurrentAttachment(generation, handle, mux)) return;
				clearOpenTimer(generation);
				r.inputReady = false;
				// Land whatever was buffered before the notice, and lift the cover:
				// a pane that exits mid-replay must never be left behind it.
				flushReplay(false, true);
				flushSteadyWrite();
				terminal.writeln("\r\n\x1b[2m[process exited]\x1b[0m");
				transition("exited");
				// Preserve xterm scrollback, but release the attachment: an exited
				// pane has no reason to keep a WebSocket/client writer alive.
				teardownMux();
				invalidateWorkspaces();
			}),
			mux.onError(handle, (message) => {
				if (!isCurrentAttachment(generation, handle, mux)) return;
				clearOpenTimer(generation);
				r.inputReady = false;
				flushReplay(false, true);
				flushSteadyWrite();
				terminal.writeln(`\r\n\x1b[2m[terminal error] ${message}\x1b[0m`);
				setError(message);
				transition("error");
				// Pane errors are terminal for this attachment. Keep the renderer
				// and its error text inspectable while disposing the failed socket;
				// an explicit session restore can create a fresh attachment later.
				teardownMux();
				void captureRendererEvent("ao.renderer.terminal_attach_failed", { reason: "pane_error" });
				invalidateWorkspaces();
			}),
			mux.onConnectionChange((connectionState) => {
				if (!isCurrentAttachment(generation, handle, mux)) return;
				if (connectionState === "closed") {
					// End the gate: no replay is coming over a dead socket. This is
					// the ONLY settle path when the socket dies before `opened` —
					// clearOpenTimer below drops the open timeout, and
					// scheduleReattach schedules nothing while the daemon is down,
					// so without this the pane is stranded behind the cover with no
					// timer left to lift it (and stays there for a whole reconnect
					// storm). The cap cannot cover this: it is armed from `opened`.
				flushReplay(false, true);
				flushSteadyWrite();
					clearOpenTimer(generation);
					r.inputReady = false;
					scheduleReattach();
				}
			}),
		);
		const input = terminal.onUserInput((data) => {
			if (
				!isCurrentAttachment(generation, handle, mux) ||
				!r.inputReady ||
				optionsRef.current.inputDisabled ||
				optionsRef.current.isVisible === false
			) {
				return;
			}
			// Input is accepted from `opened`, which lands before the replay — so a
			// user can type while the gate still holds the burst, and their echo
			// would sit in the buffer behind an opaque cover. Someone typing has
			// stopped caring about a tidy reveal and needs to see the pane now:
			// end the gate immediately.
			if (r.replayBuffering) flushReplay();
			else revealReplayTail();
			mux.sendInput(handle, data);
		});
		// xterm only fires onResize when the grid actually changed; the debounce
		// additionally collapses a drag/fullscreen/layout burst into one PTY
		// resize. The last published grid is checked again at send time because a
		// retained activation can report the same final grid through several paths.
		const resize = terminal.onResize(({ cols, rows }) => {
			if (!isCurrentAttachment(generation, handle, mux)) return;
			if (optionsRef.current.isVisible === false) return;
			if (r.resizeTimer) clearTimeout(r.resizeTimer);
			r.resizeTimer = setTimeout(() => {
				r.resizeTimer = null;
				if (!isCurrentAttachment(generation, handle, mux)) return;
				if (optionsRef.current.isVisible === false) return;
				const published = r.lastPublishedGrid;
				if (published?.cols === cols && published.rows === rows) return;
				mux.resize(handle, cols, rows);
				r.lastPublishedGrid = { cols, rows };
			}, RESIZE_DEBOUNCE_MS);
		});
		r.disposers.push(
			() => input.dispose(),
			() => resize.dispose(),
		);

		// Open the replay gate before the pane can produce any output. It cannot
		// wait for `opened`: the daemon fires onOpen from setPTY and only then
		// starts copyOut (attachment.go), so `attached` arrives before the first
		// replay byte and would uncover a pane that has not drawn yet.
		const coverInitialReplay = optionsRef.current.coverInitialReplay !== false;
		r.replayBuffering = coverInitialReplay;
		r.replayChunks = [];
		r.replayBytes = 0;
		setReplaySettled(!coverInitialReplay);
		// The cap is armed from `opened`, NOT from here. A slow attach — the
		// daemon runs a liveness probe and spawns the runtime client before the
		// first byte, which is why OPEN_TIMEOUT_MS budgets 3s — would otherwise
		// burn the whole cap on the handshake, flush an empty buffer, and leave
		// `replayBuffering` false for the rest of the attachment. The replay
		// would then land frame-by-frame with the bug fully intact, behind a
		// pointless blank cover. `opened` fires from setPTY immediately before
		// copyOut, so anchoring there means the cap only ever measures the burst.
		// If `opened` never arrives, openTimer tears down and teardownMux lifts
		// the cover.

		// A retained pane may reconnect while parked. It still needs the output
		// stream, but its stale off-screen grid must not resize the shared PTY.
		// Zero dimensions mean "attach without claiming a size"; the first
		// visible fit emits the authoritative grid after activation.
		const visible = optionsRef.current.isVisible !== false;
		r.needsVisibleSizeSync = !visible;
		const openCols = visible ? terminal.cols : 0;
		const openRows = visible ? terminal.rows : 0;
		mux.open(handle, openCols, openRows);
		r.lastPublishedGrid =
			openCols > 0 && openRows > 0 ? { cols: openCols, rows: openRows } : null;
		r.openTimer = setTimeout(() => {
			if (!isCurrentAttachment(generation, handle, mux)) return;
			r.openTimer = null;
			// Only the first timeout of a reattach sequence is reported; the
			// backoff loop retrying against a restarting daemon is not news.
			if (r.attempts === 0) {
				void captureRendererEvent("ao.renderer.terminal_attach_failed", { reason: "open_timeout" });
			}
			transition("reattaching");
			teardownMux();
			scheduleReattach();
		}, OPEN_TIMEOUT_MS);
	}, [
		clearOpenTimer,
		clearReplayTimers,
		invalidateWorkspaces,
		isCurrentAttachment,
		scheduleReattach,
		teardownMux,
		transition,
	]);
	connectRef.current = connect;

	/**
	 * Bind a terminal to the current session's PTY. Call once the terminal is
	 * opened (and fitted); returns the detach function for effect cleanup.
	 */
	const attach = useCallback(
		(terminal: AttachableTerminal) => {
			const r = runtime.current;
			const handle = optionsRef.current.shellTerminalHandleId ?? sessionRef.current?.terminalHandleId ?? null;
			r.terminal = terminal;
			r.handle = handle;
			r.detached = false;
			r.attempts = 0;
			setError(undefined);
			if (handle) {
				if (optionsRef.current.daemonReady) {
					transition("connecting");
					connect();
				} else {
					transition("reattaching");
				}
			} else {
				transition("idle");
			}
			return () => {
				// Before the generation bump — past it the flush's own guard rejects
				// it and the buffered bytes (and any URL in them) are lost. This is
				// the session-switch path, so it is the one that matters most.
				r.flushReplay?.(true);
				r.generation += 1;
				r.detached = true;
				teardownMux();
				r.terminal = null;
				r.handle = null;
				r.inputReady = false;
				r.needsVisibleSizeSync = false;
				setError(undefined);
				// Detaching ends any pending replay: never leave the next mount of
				// this hook believing a burst is still in flight.
				setReplaySettled(true);
				transition("idle");
			};
		},
		[connect, teardownMux, transition],
	);

	// Publish the retained terminal's positive grid only after activation has
	// painted it and made it visible. A parked reconnect opens at 0×0, while a
	// continuously connected parked terminal may be refitted locally with resize
	// forwarding suppressed. In both cases activation must explicitly promote the
	// retained primary back to an authoritative size.
	const syncVisibleSize = useCallback((cols: number, rows: number) => {
		const r = runtime.current;
		if (
			optionsRef.current.isVisible === false ||
			r.detached ||
			!r.mux ||
			!r.handle ||
			!r.needsVisibleSizeSync ||
			cols <= 0 ||
			rows <= 0
		) {
			return;
		}
		if (r.resizeTimer) {
			clearTimeout(r.resizeTimer);
			r.resizeTimer = null;
		}
		r.needsVisibleSizeSync = false;
		const published = r.lastPublishedGrid;
		if (published?.cols === cols && published.rows === rows) return;
		r.mux.resize(r.handle, cols, rows);
		r.lastPublishedGrid = { cols, rows };
	}, []);

	// Daemon came back while we were waiting: reconnect immediately, without
	// backoff debt from attempts made against the dead daemon.
	const daemonReady = options.daemonReady;
	useEffect(() => {
		const r = runtime.current;
		if (!daemonReady || r.detached) return;
		if (stateRef.current !== "reattaching" || r.retryTimer) return;
		r.attempts = 0;
		connect();
	}, [daemonReady, connect]);

	// A parked cache entry keeps parsing output, but it must be inert as a PTY
	// client. Cancel resize work queued while it was visible and remember that a
	// hidden local refit cannot be forwarded. useLayoutEffect runs before the
	// cache's activation preparation, so the first visible frame always publishes
	// its final positive grid even when xterm's local size no longer changes.
	const isVisible = options.isVisible !== false;
	useLayoutEffect(() => {
		if (isVisible) return;
		const r = runtime.current;
		r.needsVisibleSizeSync = true;
		if (r.resizeTimer) {
			clearTimeout(r.resizeTimer);
			r.resizeTimer = null;
		}
	}, [isVisible]);

	useEffect(() => {
		const r = runtime.current;
		const handle = session?.terminalHandleId ?? null;
		const isActive = session ? sessionIsActive(session) : false;
		const wasActive = previousSessionActiveRef.current;
		const previousActivityState = previousActivityStateRef.current;
		previousSessionActiveRef.current = isActive;
		previousActivityStateRef.current = session?.activity?.state;
		const restoredSession = !wasActive && isActive;
		const resumedAgent = previousActivityState === "exited" && session?.activity?.state !== "exited";
		if (!handle || (!restoredSession && !resumedAgent) || r.detached || !r.terminal) {
			return;
		}
		if (r.handle !== handle) return;
		if (stateRef.current !== "exited" && stateRef.current !== "error") return;
		if (optionsRef.current.daemonReady) {
			transition("connecting");
			connect();
		} else {
			transition("reattaching");
		}
	}, [
		connect,
		session?.activity?.state,
		session?.isTerminated,
		session?.status,
		session?.terminalHandleId,
		transition,
	]);

	// Belt-and-braces: never leak a socket past unmount, even if the owner
	// forgot to call detach.
	useEffect(
		() => () => {
			const r = runtime.current;
			// Same ordering rule as the detach path above.
			r.flushReplay?.(true);
			r.generation += 1;
			r.detached = true;
			r.inputReady = false;
			teardownMux();
		},
		[teardownMux],
	);

	return { attach, state, error, replaySettled, syncVisibleSize };
}
