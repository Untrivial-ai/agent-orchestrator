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
	// Also what the screens key their Retry action on.
	const [fetchFailed, setFetchFailed] = useState(false);
	const settledRef = useRef("");
	const onSettledRef = useRef(onSettled);
	onSettledRef.current = onSettled;
	// Requests can take up to REQUEST_TIMEOUT_MS, so a slow one can resolve after a
	// newer one already has. Only the newest is allowed to write state; otherwise a
	// stale answer could revert a `supported: true` status back to unsupported.
	const requestSeq = useRef(0);
	// Consecutive failed requests and the HTTP status of the latest one. Held by
	// the hook rather than the poll effect for the same reason `readinessRef` is
	// (below): the effect re-runs on `appActive`, so a count it owned would
	// restart the backoff from 1s every time the app was switched, and while
	// failures had a budget it silently refilled that too. It is written only
	// where a request ends, so a tap recheck and the poll loop share one count,
	// and a stale answer never touches it. Any request that lands clears it: a
	// start POST that the daemon accepted is proof the link is back, and leaving
	// the count on it would open the new transition on the backoff cadence with
	// a Retry showing for a switch that is in fact proceeding.
	const failureRef = useRef<{ count: number; status?: number }>({ count: 0 });
	const noteRequestLanded = useCallback(() => {
		failureRef.current = { count: 0 };
		setFetchFailed(false);
	}, []);

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
			noteRequestLanded();
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
				const keepPolling = shouldKeepPolling(httpStatus);
				setError(message);
				setPollable(keepPolling);
				setFetchFailed(true);
				// A rejection is a request that LANDED: the daemon answered, so the
				// link is not what is wrong and the failure backoff has nothing to
				// measure. `pollable` owns the stop for those, and clearing the count
				// keeps the two from interacting — counting them would let five app
				// switches under a rotated password spend the speculative budget, and
				// the sixth foreground would then get no fresh request at all, which
				// is the one thing that notices the password was fixed.
				failureRef.current = keepPolling
					? { count: failureRef.current.count + 1, status: httpStatus }
					: { count: 0 };
			}
			// The status code rides along so the caller can tell a rejection from an
			// unreachable daemon; `shouldKeepPolling` consumes it and drops it.
			return { ok: false, error: message, status: httpStatus, stale };
		} finally {
			if (current()) setLoading(false);
		}
	}, [cfg, noteRequestLanded, sessionId]);

	useEffect(() => {
		// A new session or config starts with a clean count: the previous
		// session's 404 must not stop the poll for the one that replaced it.
		failureRef.current = { count: 0 };
		void refresh();
	}, [refresh]);

	// The poll is also the daemon's liveness signal (see shouldPoll), so a
	// backgrounded phone must stop ticking the roster rather than hold the
	// desktop's live dot on. The board poll already applies this rule.
	const [appActive, setAppActive] = useState(() => shouldPoll(RNAppState.currentState));
	useEffect(() => {
		const sub = RNAppState.addEventListener("change", (s) => {
			const active = shouldPoll(s);
			setAppActive(active);
			// The board poll's stop flag lives in its effect, so it gets one fresh
			// request per foreground and recovers once the lockout clears. `pollable`
			// is state and would survive; give it the same second chance. A single
			// request per app switch cannot arm anything.
			if (active) setPollable(true);
		});
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
		let timer: ReturnType<typeof setTimeout> | undefined;

		const schedule = (current: SessionInterfaceTransitionStatus | undefined) => {
			const delay = interfaceTransitionNextPoll({
				status: current,
				readinessAttempts: readinessRef.current.attempts,
				consecutiveFailures: failureRef.current.count,
				failureStatus: failureRef.current.status,
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
				// Answers are what the readiness window is for. Counting requests would
				// let a run of timeouts spend the whole budget without ever learning
				// whether the native session became ready.
				readinessRef.current.attempts += 1;
				schedule(result.status);
				return;
			}
			// `refresh` has already counted this failure. A failure never produces a
			// new status, so the loop re-arms on the one the hook still holds; the
			// scheduler decides from the count and the status code whether that is
			// a backoff or, for a session the daemon says is gone, a stop.
			schedule(statusRef.current);
		};

		schedule(statusRef.current);
		return () => {
			cancelled = true;
			if (timer) clearTimeout(timer);
		};
	}, [
		appActive,
		cfg,
		// Deliberately a dependency and deliberately unread in the body: a mount
		// fetch that never lands leaves `status` undefined, and this is the only
		// thing that re-runs the effect so the loop gets scheduled at all. A
		// dependency cleanup that drops it as unused would silently take
		// cold-start recovery with it.
		fetchFailed,
		pollable,
		refresh,
		sessionId,
		status?.transition?.phase,
		status?.reasonCode,
	]);

	const start = useCallback(
		async (targetMode: "chat" | "tui", policy: "drain" | "interrupt") => {
			if (!cfg) throw new Error("No AO server configured");
			setStarting(true);
			setError(undefined);
			try {
				const transition = await startSessionInterfaceTransition(cfg, sessionId, targetMode, policy);
				noteRequestLanded();
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
		[cfg, noteRequestLanded, sessionId],
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
				noteRequestLanded();
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
		[cfg, noteRequestLanded, sessionId],
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
		// True from a failed request until the next one lands. The transition
		// banners show a Retry while it is set: the poll keeps trying on its own at
		// up to 8s, but a user who can see the network is back should not have to
		// wait for the tick.
		fetchFailed,
		start,
		cancel,
		acknowledgeNotice,
		refresh,
	};
}
