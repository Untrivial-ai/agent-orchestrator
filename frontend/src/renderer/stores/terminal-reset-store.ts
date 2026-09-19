import { create } from "zustand";

// Per-session terminal reset/reconnect signals for cloud sessions.
//
// A cloud terminal is otherwise keyed only on the (unchanged) session id, so a
// restore — which re-provisions a FRESH box under the same id but a NEW worker
// epoch — needs an explicit signal or the pane keeps its cached mux factory (and
// stale cursor) and clings to the dead terminal. `bump` provides that:
//
//   - `nonces` bumps the terminal cache key + mux factory key, so the pane
//     rebuilds from scratch (new factory closure, cursor at 0) and re-mints.
//   - `reconnecting` marks the session as coming up on a fresh box. The terminal
//     shows a calm "Connecting…" while true, instead of letting the user type
//     into the previous box's dead terminal.
//   - `baselineEpoch` records the worker epoch AT restore time. The box is only
//     genuinely up once a NEW epoch appears (the fresh worker bootstrapped and
//     created its terminal). Gating on the epoch — not runtimeConnected — is what
//     makes a rapid delete→restore correct: the OLD worker's connection lingers
//     briefly, so runtimeConnected stays true and the pane would otherwise attach
//     to the old epoch's terminal (shows content, but the old worker is gone, so
//     you cannot type). markConnected clears `reconnecting` the instant the epoch
//     advances past the baseline.
//
// Not persisted — it only matters within a live session while a terminal is
// mounted.
type TerminalResetState = {
	nonces: Record<string, number>;
	reconnecting: Record<string, boolean>;
	baselineEpoch: Record<string, number>;
	bump: (sessionId: string, currentEpoch: number) => void;
	markConnected: (sessionId: string) => void;
};

export const useTerminalResetStore = create<TerminalResetState>((set) => ({
	nonces: {},
	reconnecting: {},
	baselineEpoch: {},
	bump: (sessionId, currentEpoch) =>
		set((state) => ({
			nonces: { ...state.nonces, [sessionId]: (state.nonces[sessionId] ?? 0) + 1 },
			reconnecting: { ...state.reconnecting, [sessionId]: true },
			baselineEpoch: { ...state.baselineEpoch, [sessionId]: currentEpoch },
		})),
	markConnected: (sessionId) =>
		set((state) =>
			state.reconnecting[sessionId]
				? { reconnecting: { ...state.reconnecting, [sessionId]: false } }
				: state,
		),
}));

/**
 * Reads a session's current terminal-reset nonce without a React subscription,
 * for hook-free callers (e.g. deriving a cache key inside a memo). Returns 0
 * when the session has never been reset.
 */
export function terminalResetNonce(sessionId?: string): number {
	if (!sessionId) return 0;
	return useTerminalResetStore.getState().nonces[sessionId] ?? 0;
}
