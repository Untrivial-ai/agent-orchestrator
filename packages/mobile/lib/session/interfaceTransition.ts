import { classifyConnectionFailure } from "../connectionError";
import type { SessionInterfaceTransitionStatus } from "../chat/api";

type InterfaceTransition = { phase: string };

type InterfaceTransitionStatus = Pick<SessionInterfaceTransitionStatus, "reasonCode" | "reason"> & {
	transition?: InterfaceTransition;
};

const activePhases = new Set([
	"requested",
	"preflighting",
	"draining",
	"source_stopping",
	"source_stopped",
	"target_starting",
	"activating",
]);

// Transient: the daemon reports these until the terminal's session-start hook
// proves which native conversation it is running, so rechecking is what lets the
// switch enable itself.
const nativeSessionReadinessCodes = new Set(["NATIVE_SESSION_MISSING", "NATIVE_SESSION_UNVERIFIED"]);
const nativeSessionReadinessPoll = 1_000;

// The recheck is bounded because these codes are not guaranteed to clear at all:
// a `--resume` relaunch stays UNVERIFIED indefinitely (#4122), so "poll until it
// resolves" has no terminating condition and would mean one request per second
// for as long as the screen stays open. A session that is going to settle does so
// in about a second, so this window is generous; past it the tap path re-asks,
// which is the escape hatch for the rare late clear.
//
// The budget is per readiness state, not per screen: the daemon reports MISSING
// before UNVERIFIED, and that progression is real news, so it earns a fresh
// window. Worst case is therefore two windows, not one.
//
// Only ANSWERS are counted here — a timeout says nothing about whether the
// native session became ready, so it must not spend the window. That leaves
// this budget unable to end a wait against a daemon that never answers, which
// is what `speculativeFailureAttempts` below is for.
export const nativeSessionReadinessAttempts = 10;

export function mobileInterfaceTransitionIsActive(transition?: InterfaceTransition): boolean {
	return Boolean(transition && activePhases.has(transition.phase));
}

export function mobileInterfaceTransitionIsCancellable(transition?: InterfaceTransition): boolean {
	return Boolean(
		transition && ["requested", "preflighting", "draining"].includes(transition.phase),
	);
}

function nativeSessionReadinessPending(status?: InterfaceTransitionStatus): boolean {
	return Boolean(status?.reasonCode && nativeSessionReadinessCodes.has(status.reasonCode));
}

// `readinessAttempts` counts rechecks already spent on the CURRENT readiness wait.
// A live transition is a finite operation with terminal phases, so its 300ms poll
// stays unbounded; only the open-ended readiness wait is capped.
export function interfaceTransitionPollInterval(
	status?: InterfaceTransitionStatus,
	readinessAttempts = 0,
): number | undefined {
	if (mobileInterfaceTransitionIsActive(status?.transition)) return 300;
	if (nativeSessionReadinessPending(status) && readinessAttempts < nativeSessionReadinessAttempts) {
		return nativeSessionReadinessPoll;
	}
	return undefined;
}

// A failed request never advances `status`, so a poll rescheduled from the last
// known status re-arms on it: a session deleted mid-transition would 404 at
// 300ms indefinitely. But only a 404 or 410 is the daemon's word that the
// session is gone. A timeout, a refused connection or a 5xx is a fact about the
// link, not the session, and a link comes back.
//
// 401/403/429 never reach the scheduler: the hook stops polling on those (see
// `shouldKeepPolling`), because retrying a rejected password arms the lockout.
export function interfaceTransitionSessionGone(status: number | undefined): boolean {
	return status === 404 || status === 410;
}

const failureBackoff = [1_000, 2_000, 4_000, 8_000];

/**
 * How many consecutive failures a poll with nothing in flight will absorb
 * before it gives up.
 *
 * The two waits this scheduler serves are not the same shape, so they do not
 * share a retry policy. A live handoff is a real operation with a banner on
 * screen: it retries until the link returns, because a blanket cap stranded one
 * behind any outage longer than the budget — once the last timer had fired,
 * connectivity returning changed nothing and the banner froze until the screen
 * was remounted (#4852 review). A readiness wait is speculative: #4122 says it
 * may never clear even on a healthy link, which is why it was given a budget at
 * all, and against a daemon that never answers it cannot make progress by
 * waiting longer. The same goes for a mount fetch that never landed.
 *
 * Five is five requests with 1+2+4+8s between them, the fifth failure being
 * the one that stops it: about 15s against a refused connection, and about 75s
 * when every request burns REQUEST_TIMEOUT_MS instead of answering. Past it the
 * escape is the header recheck, tappable precisely because no transition is
 * live, and any request that lands clears the count and restarts this loop.
 *
 * A spent count survives backgrounding, like the readiness window and for the
 * same reason: `appActive` re-runs the effect, so anything the effect owned
 * would make app-switching the way to refill a budget. Returning to the app is
 * news about the user, not about the link.
 *
 * This never competes with the 401 stop, which foregrounding does give one
 * fresh request: a rejection is a request that landed, so `refresh` clears the
 * count rather than spending it, and `pollable` owns that stop on its own.
 */
export const speculativeFailureAttempts = 5;

/**
 * The one scheduler for the status poll. Failures back off on their own count,
 * because a run of failures teaches us nothing about `status` and must not be
 * paid for out of the readiness window. `failureStatus` is the HTTP status of
 * the latest failure, `undefined` when nothing answered.
 *
 * Retrying on failure with no status at all is deliberate: a first fetch that
 * never landed would otherwise leave the screen with no poll to start, and the
 * switch would stay greyed out for the life of the screen. That retry is
 * bounded like the readiness wait — see speculativeFailureAttempts — because
 * there is no operation in flight to keep it alive indefinitely.
 */
export function interfaceTransitionNextPoll(args: {
	status?: InterfaceTransitionStatus;
	readinessAttempts?: number;
	consecutiveFailures?: number;
	failureStatus?: number;
}): number | undefined {
	const failures = args.consecutiveFailures ?? 0;
	if (failures > 0) {
		if (interfaceTransitionSessionGone(args.failureStatus)) return undefined;
		// A live handoff is retried for as long as the screen is open; anything
		// else is speculative and stops. See speculativeFailureAttempts.
		const live = mobileInterfaceTransitionIsActive(args.status?.transition);
		if (!live && failures >= speculativeFailureAttempts) return undefined;
		return failureBackoff[Math.min(failures - 1, failureBackoff.length - 1)];
	}
	return interfaceTransitionPollInterval(args.status, args.readinessAttempts ?? 0);
}

// The daemon's reason is a Go error: fair for a verdict, useless for a wait.
export function interfaceSwitchUnavailableMessage(
	status?: InterfaceTransitionStatus,
	fallbackError?: string,
): string {
	if (nativeSessionReadinessPending(status)) {
		return "The terminal has not confirmed its agent conversation yet. Try again in a moment, or send the terminal a message first.";
	}
	return (
		status?.reason ||
		fallbackError ||
		"This agent has not declared a compatible native conversation handoff."
	);
}

/**
 * How the recheck behind a tap turned out. `not-attempted` is its own case
 * because there is no config yet — treating that as an answer would report a
 * cold start as an agent that cannot do Chat.
 */
export type InterfaceSwitchRecheck =
	| { outcome: "answered" }
	| { outcome: "failed"; error: string; status?: number }
	| { outcome: "not-attempted" };

/**
 * A recheck that never got an answer is not a verdict about the agent. Without
 * this split a connectivity failure surfaces as "This agent has not declared a
 * compatible native conversation handoff." — telling the user their agent is
 * incapable when in fact we never got to ask.
 *
 * The failure branch then splits again on the same rule the rest of the app
 * follows (see connectionError.ts): being reached and rejected is not the same
 * as never being reached. A rotated password sends the user to re-pair, not to
 * go and check their Wi-Fi.
 */
export function interfaceSwitchAlert(
	status?: InterfaceTransitionStatus,
	fallbackError?: string,
	recheck: InterfaceSwitchRecheck = { outcome: "answered" },
): { title: string; message: string } {
	if (recheck.outcome === "not-attempted") {
		return {
			title: "Not connected yet",
			message: "AO has not finished loading this phone's connection settings. Try again in a moment.",
		};
	}
	if (recheck.outcome === "failed") {
		switch (classifyConnectionFailure(recheck.status)) {
			case "auth":
				return {
					title: "AO rejected this phone",
					message:
						"The connection password has changed, so this phone can no longer talk to AO. Open Settings \u2192 Connect Mobile on your computer and scan the code again.",
				};
			case "rate-limited":
				return {
					title: "AO is not accepting requests",
					message:
						"AO has paused this phone for a minute. That usually means the connection password changed \u2014 re-scan the code in Settings \u2192 Connect Mobile on your computer.",
				};
			case "server-error":
				return {
					title: "AO could not answer",
					message: `AO was reached but could not say whether this session can switch to Chat. ${recheck.error}`,
				};
			default:
				return {
					title: "Could not reach AO",
					message: `This phone could not reach AO to check whether this session can switch to Chat. ${recheck.error}`,
				};
		}
	}
	return {
		title: nativeSessionReadinessPending(status) ? "Not ready yet" : "Chat unavailable",
		message: interfaceSwitchUnavailableMessage(status, fallbackError),
	};
}
