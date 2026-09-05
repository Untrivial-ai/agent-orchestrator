import { useCallback, useEffect, useRef, useState } from "react";
import { AppState as RNAppState } from "react-native";
import type { ServerConfig } from "../config";
import { ApiError } from "../api";
import { shouldPoll } from "../appStatePoll";
import { shouldKeepPolling } from "../connectionError";
import {
	acknowledgeSessionInterfaceTransitionNotice,
	cancelSessionInterfaceTransition,
	getSessionInterfaceTransition,
	startSessionInterfaceTransition,
	type SessionInterfaceTransitionStatus,
} from "../chat/api";
import {
	interfaceTransitionNextPoll,
	mobileInterfaceTransitionIsActive,
} from "./interfaceTransition";

export {
	interfaceSwitchAlert,
	mobileInterfaceTransitionIsActive,
	mobileInterfaceTransitionIsCancellable,
} from "./interfaceTransition";
export type { InterfaceSwitchRecheck } from "./interfaceTransition";

/**
 * Discriminated so a caller can tell "asked, and the answer is no" from "could
 * not ask". `undefined` means the recheck was never attempted (no config yet).
 */
export type InterfaceTransitionRecheck =
	| { ok: true; status: SessionInterfaceTransitionStatus; stale?: boolean }
	| { ok: false; error: string; status?: number; stale?: boolean };

export function useInterfaceTransition(
	cfg: ServerConfig | null,
	sessionId: string,
	onSettled?: () => void | Promise<void>,
) {
	const [status, setStatus] = useState<SessionInterfaceTransitionStatus>();
	const [loading, setLoading] = useState(Boolean(cfg && sessionId));
	const [starting, setStarting] = useState(false);
	const [cancelling, setCancelling] = useState(false);
	const [acknowledgingNotice, setAcknowledgingNotice] = useState(false);
	const [acknowledgeNoticeError, setAcknowledgeNoticeError] = useState<string>();
	const [error, setError] = useState<string>();
	// A rejected recheck is not a failed one. The readiness poll runs at 1s, and
	// the bridge locks a device out for a minute after five failed auths, so a
	// rotated password would arm that lockout in five seconds and hold it there
	// for as long as the screen stays open. Same rule as the board poll.
	const [pollable, setPollable] = useState(true);
	// Lets a failed fetch reach the poll effect. Without it a first fetch that never
	// landed leaves `status` undefined, the effect schedules nothing, and no later
	// request is ever made — the switch stays greyed out for the life of the screen.
	const [fetchFailed, setFetchFailed] = useState(false);
	const settledRef = useRef("");
	const onSettledRef = useRef(onSettled);
	onSettledRef.current = onSettled;
	// Requests can take up to REQUEST_TIMEOUT_MS, so a slow one can resolve after a
	// newer one already has. Only the newest is allowed to write state; otherwise a
	// stale answer could revert a `supported: true` status back to unsupported.
	const requestSeq = useRef(0);

	// Resolves with the fetched status so a tap can act on a fresh answer.
	const refresh = useCallback(async (): Promise<InterfaceTransitionRecheck | undefined> => {
		if (!cfg || !sessionId) return undefined;
		const seq = ++requestSeq.current;
		const current = () => seq === requestSeq.current;
		try {
			const next = await getSessionInterfaceTransition(cfg, sessionId);
			// A superseded answer is not allowed to write state, stamp `settledRef`,
			// or fire `onSettled` — that callback is a full board refetch, and running
			// it off a response the hook just refused to trust settles the wrong turn.
			if (!current()) return { ok: true, status: next, stale: true };
			setStatus(next);
			setError(undefined);
			setPollable(true);
			setFetchFailed(false);
			const transition = next.transition;
			if (transition && !mobileInterfaceTransitionIsActive(transition) && settledRef.current !== transition.id) {
				settledRef.current = transition.id;
				await onSettledRef.current?.();
			}
			return { ok: true, status: next };
		} catch (cause) {
			const message = cause instanceof Error ? cause.message : String(cause);
			const httpStatus = cause instanceof ApiError ? cause.status : undefined;
			const stale = !current();
			if (!stale) {
				setError(message);
				setPollable(shouldKeepPolling(httpStatus));
				setFetchFailed(true);
			}
			// The status code rides along so the caller can tell a rejection from an
			// unreachable daemon; `shouldKeepPolling` consumes it and drops it.
			return { ok: false, error: message, status: httpStatus, stale };
		} finally {
			if (current()) setLoading(false);
		}
	}, [cfg, sessionId]);

	useEffect(() => {
		void refresh();
	}, [refresh]);

	// The poll is also the daemon's liveness signal (see shouldPoll), so a
	// backgrounded phone must stop ticking the roster rather than hold the
	// desktop's live dot on. The board poll already applies this rule.
	const [appActive, setAppActive] = useState(() => shouldPoll(RNAppState.currentState));
	useEffect(() => {
		const sub = RNAppState.addEventListener("change", (s) => setAppActive(shouldPoll(s)));
		return () => sub.remove();
	}, []);

	// Latest status without depending on its identity: every poll returns a fresh
	// object, and depending on it would re-run this effect on every tick.
	const statusRef = useRef<SessionInterfaceTransitionStatus | undefined>(undefined);
	statusRef.current = status;

	// The readiness budget is keyed on the situation rather than owned by the
	// effect, so backgrounding and returning does not hand the same wait a fresh
	// ten attempts — `appActive` is in the dep list and would otherwise make app
	// switching the way to restart a spent window.
	const readinessRef = useRef({ key: "", attempts: 0 });

	useEffect(() => {
		if (!cfg || !sessionId || !pollable || !appActive) return;
		// `sessionId` is in the key so a screen reused for another session does not
		// inherit the previous one's spent budget.
		const key = `${sessionId}|${status?.transition?.phase ?? ""}|${status?.reasonCode ?? ""}`;
		if (readinessRef.current.key !== key) readinessRef.current = { key, attempts: 0 };

		let cancelled = false;
		// Seeded from the last fetch so a failure that happened outside this loop —
		// the mount fetch, most importantly — still gets retried.
		let failures = fetchFailed ? 1 : 0;
		let timer: ReturnType<typeof setTimeout> | undefined;

		const schedule = (current: SessionInterfaceTransitionStatus | undefined) => {
			const delay = interfaceTransitionNextPoll({
				status: current,
				readinessAttempts: readinessRef.current.attempts,
				consecutiveFailures: failures,
			});
			if (cancelled || delay === undefined) return;
			timer = setTimeout(run, delay);
		};

		// Self-scheduling rather than setInterval: the next tick is only queued once
		// this one has come back, so an unreachable daemon holding requests open for
		// REQUEST_TIMEOUT_MS cannot stack a dozen of them.
		const run = async () => {
			const result = await refresh();
			if (cancelled) return;
			if (result?.stale) {
				// A superseded answer teaches us nothing and must not move either
				// counter: the newer request owns the status. Keep the loop alive on
				// whatever the hook actually adopted.
				schedule(statusRef.current);
				return;
			}
			if (result?.ok) {
				failures = 0;
				// Answers are what the readiness window is for. Counting requests would
				// let a run of timeouts spend the whole budget without ever learning
				// whether the native session became ready.
				readinessRef.current.attempts += 1;
				schedule(result.status);
				return;
			}
			failures += 1;
			schedule(statusRef.current);
		};

		schedule(statusRef.current);
		return () => {
			cancelled = true;
			if (timer) clearTimeout(timer);
		};
	}, [appActive, cfg, fetchFailed, pollable, refresh, sessionId, status?.transition?.phase, status?.reasonCode]);

	const start = useCallback(
		async (targetMode: "chat" | "tui", policy: "drain" | "interrupt") => {
			if (!cfg) throw new Error("No AO server configured");
			setStarting(true);
			setError(undefined);
			try {
				const transition = await startSessionInterfaceTransition(cfg, sessionId, targetMode, policy);
				setStatus((current) => ({
					supported: current?.supported ?? true,
					targetMode,
					transition,
				}));
			} catch (cause) {
				const message = cause instanceof Error ? cause.message : String(cause);
				setError(message);
				throw cause;
			} finally {
				setStarting(false);
			}
		},
		[cfg, sessionId],
	);

	const cancel = useCallback(async () => {
		if (!cfg) throw new Error("No AO server configured");
		setCancelling(true);
		setError(undefined);
		try {
			await cancelSessionInterfaceTransition(cfg, sessionId);
			await refresh();
		} catch (cause) {
			const message = cause instanceof Error ? cause.message : String(cause);
			setError(message);
			throw cause;
		} finally {
			setCancelling(false);
		}
	}, [cfg, refresh, sessionId]);

	const acknowledgeNotice = useCallback(
		async (transitionId: string) => {
			if (!cfg) throw new Error("No AO server configured");
			setAcknowledgingNotice(true);
			setAcknowledgeNoticeError(undefined);
			try {
				const transition = await acknowledgeSessionInterfaceTransitionNotice(
					cfg,
					sessionId,
					transitionId,
				);
				setStatus((current) =>
					current?.transition?.id === transition.id ? { ...current, transition } : current,
				);
			} catch (cause) {
				const message = cause instanceof Error ? cause.message : String(cause);
				setAcknowledgeNoticeError(message);
				throw cause;
			} finally {
				setAcknowledgingNotice(false);
			}
		},
		[cfg, sessionId],
	);

	return {
		status,
		transition: status?.transition,
		loading,
		starting,
		cancelling,
		acknowledgingNotice,
		error,
		acknowledgeNoticeError,
		start,
		cancel,
		acknowledgeNotice,
		refresh,
	};
}
