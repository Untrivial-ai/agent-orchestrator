import { describe, expect, it } from "vitest";
import {
	interfaceSwitchAlert,
	interfaceSwitchUnavailableMessage,
	interfaceTransitionFailureAttempts,
	interfaceTransitionNextPoll,
	interfaceTransitionPollInterval,
	nativeSessionReadinessAttempts,
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

describe("failed rechecks get their own bounded budget", () => {
	const waiting = { reasonCode: "NATIVE_SESSION_UNVERIFIED" };

	it("backs off instead of hammering, and gives up", () => {
		expect(interfaceTransitionNextPoll({ consecutiveFailures: 1 })).toBe(1_000);
		expect(interfaceTransitionNextPoll({ consecutiveFailures: 2 })).toBe(2_000);
		expect(interfaceTransitionNextPoll({ consecutiveFailures: 3 })).toBe(4_000);
		expect(interfaceTransitionNextPoll({ consecutiveFailures: 4 })).toBe(8_000);
		expect(
			interfaceTransitionNextPoll({ consecutiveFailures: interfaceTransitionFailureAttempts }),
		).toBeUndefined();
	});

	it("bounds a 404 arriving mid-transition, which the status poll alone cannot", () => {
		// A failed request never advances `status`, so rescheduling on the last
		// known phase would re-arm the 300ms transition poll forever.
		const draining = { transition: { phase: "draining" } };
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 0 })).toBe(300);
		expect(interfaceTransitionNextPoll({ status: draining, consecutiveFailures: 1 })).toBe(1_000);
		expect(
			interfaceTransitionNextPoll({
				status: draining,
				consecutiveFailures: interfaceTransitionFailureAttempts,
			}),
		).toBeUndefined();
	});

	it("retries a cold start that never landed, so one dropped fetch is recoverable", () => {
		// No status at all: without the failure branch nothing would ever schedule.
		expect(interfaceTransitionNextPoll({ status: undefined, consecutiveFailures: 1 })).toBe(1_000);
		expect(interfaceTransitionNextPoll({ status: undefined, consecutiveFailures: 0 })).toBeUndefined();
	});

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
