import { describe, expect, it } from "vitest";
import {
	interfaceSwitchAlert,
	interfaceSwitchUnavailableMessage,
	interfaceTransitionNextPoll,
	interfaceTransitionPollInterval,
	interfaceTransitionSessionGone,
	nativeSessionReadinessAttempts,
	speculativeFailureAttempts,
} from "./interfaceTransition";

const daemonReason =
	"session: native conversation id is not confirmed for the current terminal launch for claude-code";

describe("mobile interface transition polling", () => {
	it("polls quickly while a transition is active", () => {
		expect(interfaceTransitionPollInterval({ transition: { phase: "draining" } })).toBe(300);
	});

	it("does not poll while idle or after a transition settles", () => {
		expect(interfaceTransitionPollInterval()).toBeUndefined();
		expect(interfaceTransitionPollInterval({})).toBeUndefined();
		expect(interfaceTransitionPollInterval({ transition: { phase: "completed" } })).toBeUndefined();
	});

	it.each(["NATIVE_SESSION_MISSING", "NATIVE_SESSION_UNVERIFIED"])(
		"rechecks %s once a second, since it clears without the user doing anything",
		(reasonCode) => {
			expect(interfaceTransitionPollInterval({ reasonCode })).toBe(1_000);
		},
	);

	it.each(["CHAT_UNSUPPORTED", "INTERFACE_HANDOFF_UNSUPPORTED", "SESSION_TERMINATED"])(
		"never polls %s, which no amount of waiting resolves",
		(reasonCode) => {
			expect(interfaceTransitionPollInterval({ reasonCode })).toBeUndefined();
		},
	);

	it("keeps the fast cadence when a transition starts during a readiness wait", () => {
		expect(
			interfaceTransitionPollInterval({
				reasonCode: "NATIVE_SESSION_UNVERIFIED",
				transition: { phase: "requested" },
			}),
		).toBe(300);
	});
});

describe("chat unavailable copy", () => {
	it("replaces the daemon's Go error while the native session is still settling", () => {
		const message = interfaceSwitchUnavailableMessage({
			reasonCode: "NATIVE_SESSION_UNVERIFIED",
			reason: daemonReason,
		});
		expect(message).not.toContain(daemonReason);
		expect(message).toContain("Try again in a moment");
	});

	it("passes the daemon's reason through for a verdict the user has to act on", () => {
		expect(
			interfaceSwitchUnavailableMessage({
				reasonCode: "CHAT_UNSUPPORTED",
				reason: "cursor does not support Chat UI.",
			}),
		).toBe("cursor does not support Chat UI.");
	});

	it("falls back to the request error, then to generic copy", () => {
		expect(interfaceSwitchUnavailableMessage(undefined, "Network request failed")).toBe(
			"Network request failed",
		);
		expect(interfaceSwitchUnavailableMessage()).toMatch(/compatible native conversation handoff/);
	});
});

describe("readiness recheck is bounded", () => {
	const waiting = { reasonCode: "NATIVE_SESSION_UNVERIFIED" };

	it("keeps rechecking through the window", () => {
		expect(interfaceTransitionPollInterval(waiting, 0)).toBe(1_000);
		expect(interfaceTransitionPollInterval(waiting, nativeSessionReadinessAttempts - 1)).toBe(1_000);
	});

	it("stops once the window is spent, so #4122 cannot poll forever", () => {
		expect(interfaceTransitionPollInterval(waiting, nativeSessionReadinessAttempts)).toBeUndefined();
		expect(
			interfaceTransitionPollInterval(waiting, nativeSessionReadinessAttempts + 50),
		).toBeUndefined();
	});

	it("does not bound a live transition, which settles on its own", () => {
		expect(
			interfaceTransitionPollInterval(
				{ transition: { phase: "draining" } },
				nativeSessionReadinessAttempts + 50,
			),
		).toBe(300);
	});

	it("defaults to the start of the window so an unspent caller still polls", () => {
		expect(interfaceTransitionPollInterval(waiting)).toBe(1_000);
	});
});

describe("failed rechecks back off on their own count", () => {
	const waiting = { reasonCode: "NATIVE_SESSION_UNVERIFIED" };
	const draining = { transition: { phase: "draining" } };

	it("backs off instead of hammering, then holds at the ceiling for a live handoff", () => {
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 1 })).toBe(1_000);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 2 })).toBe(2_000);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 3 })).toBe(4_000);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 4 })).toBe(8_000);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 5 })).toBe(8_000);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 500 })).toBe(8_000);
	});

	// The review's row: an hour on NATIVE_SESSION_UNVERIFIED with every request
	// timing out was 183 requests, `attempts` still 0, still scheduled. Answers
	// are the only thing that can spend the readiness window, and failures had
	// stopped terminating anything, so nothing was left to end the wait.
	it("gives up on a readiness wait the link will not let it finish", () => {
		let failures = 0;
		let elapsed = 0;
		let requests = 0;
		let delay: number | undefined;
		do {
			failures += 1;
			requests += 1;
			delay = interfaceTransitionNextPoll({ status: waiting, readinessAttempts: 0, consecutiveFailures: failures });
			elapsed += (delay ?? 0) + 12_000; // each attempt also waits out REQUEST_TIMEOUT_MS
		} while (delay !== undefined && requests < 500);
		expect(requests).toBe(speculativeFailureAttempts);
		expect(elapsed).toBeLessThan(90_000);
	});

	// Same shape for a mount fetch that never landed: it is worth retrying, but
	// there is no operation in flight to keep it alive indefinitely.
	it("gives up on a cold start the link will not let it finish", () => {
		expect(interfaceTransitionNextPoll({ consecutiveFailures: speculativeFailureAttempts - 1 })).toBe(8_000);
		expect(interfaceTransitionNextPoll({ consecutiveFailures: speculativeFailureAttempts })).toBeUndefined();
	});

	// A live handoff is the one wait that outlives its failures: the user is
	// watching a banner and the operation is real, so it holds at the ceiling for
	// as long as the screen is open. An hour of timeouts must not end it.
	it("never gives up on a live handoff, however long the link is down", () => {
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 300 })).toBe(8_000);
	});

	// The review asked for this to be decided rather than inherited: a 5xx on a
	// speculative wait now stops, because a daemon failing this one handler is a
	// fact about the session too. Under a live handoff it still retries.
	it.each([500, 502, 503])("stops a speculative wait on a persistent %s, but not a live handoff", (failureStatus) => {
		expect(
			interfaceTransitionNextPoll({ status: waiting, consecutiveFailures: speculativeFailureAttempts, failureStatus }),
		).toBeUndefined();
		expect(
			interfaceTransitionNextPoll({ status: draining, consecutiveFailures: speculativeFailureAttempts, failureStatus }),
		).toBe(8_000);
	});

	it("keeps a timer alive through a 90s outage mid-transition, so the handoff resumes when the link does", () => {
		// A budget of five used to spend itself in about 75s of timeouts and leave
		// no timer to notice the network coming back: the banner froze for good.
		let elapsed = 0;
		let failures = 0;
		while (elapsed < 90_000) {
			failures += 1;
			const delay = interfaceTransitionNextPoll({ status: draining, consecutiveFailures: failures });
			expect(delay).toBe(failures < 4 ? [1_000, 2_000, 4_000][failures - 1] : 8_000);
			elapsed += (delay ?? 0) + 12_000; // each attempt also waits out REQUEST_TIMEOUT_MS
		}
		// The first answer after the outage resets the count; back to the fast cadence.
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 0 })).toBe(300);
	});

	it.each([500, 502, 503])("treats a %s as the link's problem, not the session's", (failureStatus) => {
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 1, failureStatus })).toBe(1_000);
		expect(interfaceTransitionSessionGone(failureStatus)).toBe(false);
	});

	it.each([404, 410])("stops at once on a %s, the daemon's word that the session is gone", (failureStatus) => {
		// A failed request never advances `status`, so rescheduling on the last
		// known phase would otherwise re-arm the 300ms transition poll forever
		// against a session that was deleted mid-transition.
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 0 })).toBe(300);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 1, failureStatus })).toBeUndefined();
		expect(interfaceTransitionNextPoll({ status: waiting, consecutiveFailures: 1, failureStatus })).toBeUndefined();
		expect(interfaceTransitionSessionGone(failureStatus)).toBe(true);
	});

	it("does not mistake a request that never landed for a gone session", () => {
		expect(interfaceTransitionSessionGone(undefined)).toBe(false);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 1, failureStatus: undefined })).toBe(1_000);
	});

	it("retries a cold start that never landed, so one dropped fetch is recoverable", () => {
		// No status at all: without the failure branch nothing would ever schedule.
		expect(interfaceTransitionNextPoll({ status: undefined, consecutiveFailures: 1 })).toBe(1_000);
		expect(interfaceTransitionNextPoll({ status: undefined, consecutiveFailures: 0 })).toBeUndefined();
	});

	// Answers, and only answers, spend the readiness window — a timeout says
	// nothing about whether the native session became ready. What ends a wait
	// the link will not let us finish is the failure count, tested above.
	it("does not spend the readiness budget on failures", () => {
		expect(
			interfaceTransitionNextPoll({
				status: waiting,
				readinessAttempts: nativeSessionReadinessAttempts - 1,
				consecutiveFailures: 0,
			}),
		).toBe(1_000);
	});
});

describe("chat unavailable alert", () => {
	it("says it could not reach AO when nothing answered", () => {
		const alert = interfaceSwitchAlert(undefined, undefined, {
			outcome: "failed",
			error: "Network request failed",
		});
		expect(alert.title).toBe("Could not reach AO");
		expect(alert.message).toContain("Network request failed");
		expect(alert.message).not.toMatch(/compatible native conversation handoff/);
	});

	it("sends a rejected phone to re-pair rather than to check its Wi-Fi", () => {
		const alert = interfaceSwitchAlert(undefined, undefined, {
			outcome: "failed",
			error: "401 Unauthorized",
			status: 401,
		});
		expect(alert.title).toBe("AO rejected this phone");
		expect(alert.message).toContain("scan the code again");
		expect(alert.message).not.toMatch(/could not reach/i);
	});

	it("does not blame the network for a session the daemon says is gone", () => {
		const alert = interfaceSwitchAlert(undefined, undefined, {
			outcome: "failed",
			error: "404 Not Found - Unknown session",
			status: 404,
		});
		expect(alert.title).toBe("AO could not answer");
		expect(alert.message).toContain("was reached");
	});

	it("names the lockout and its real cause rather than reporting a dead link", () => {
		const alert = interfaceSwitchAlert(undefined, undefined, {
			outcome: "failed",
			error: "429",
			status: 429,
		});
		expect(alert.title).toBe("AO is not accepting requests");
		expect(alert.message).toContain("Connect Mobile");
		expect(alert.message).not.toMatch(/could not reach/i);
	});

	it("does not call a cold start an incapable agent", () => {
		const alert = interfaceSwitchAlert(undefined, undefined, { outcome: "not-attempted" });
		expect(alert.title).toBe("Not connected yet");
		expect(alert.message).not.toMatch(/compatible native conversation handoff/);
	});

	it("reports the agent's verdict when the daemon did answer", () => {
		const alert = interfaceSwitchAlert({
			reasonCode: "CHAT_UNSUPPORTED",
			reason: "cursor does not support Chat UI.",
		});
		expect(alert.title).toBe("Chat unavailable");
		expect(alert.message).toBe("cursor does not support Chat UI.");
	});

	it("titles a still-settling session as waiting, not as unavailable", () => {
		const alert = interfaceSwitchAlert({ reasonCode: "NATIVE_SESSION_UNVERIFIED" });
		expect(alert.title).toBe("Not ready yet");
		expect(alert.message).toContain("Try again in a moment");
	});
});
