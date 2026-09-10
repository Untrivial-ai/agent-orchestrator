import { describe, expect, it } from "vitest";
import { RE_RACE_AFTER_FAILURES, RE_RACE_COOLDOWN_MS, shouldReRace } from "./reRace";

const at = (over: Partial<Parameters<typeof shouldReRace>[0]> = {}) => ({
	consecutiveFailures: RE_RACE_AFTER_FAILURES,
	lastReRaceAt: 0,
	now: 100_000,
	unreachable: false,
	...over,
});

describe("shouldReRace", () => {
	// The failure that matters: paired over LAN, then Wi-Fi drops. The stored
	// endpoint is now unreachable and nothing else will notice — there is no
	// network-change listener — so a run of failed polls is the signal to race
	// the candidates again and land on the tunnel.
	it("re-races once the failures look like a dead endpoint", () => {
		expect(shouldReRace(at())).toBe(true);
	});

	// One failed poll is normal: a dropped packet, a backgrounded radio, a
	// daemon mid-restart. Re-racing on every blip would thrash the connection.
	it("tolerates a single failure", () => {
		expect(shouldReRace(at({ consecutiveFailures: 1 }))).toBe(false);
	});

	it("does nothing while the connection is healthy", () => {
		expect(shouldReRace(at({ consecutiveFailures: 0 }))).toBe(false);
	});

	// Racing every few seconds against a genuinely offline phone would burn
	// battery and hammer the daemon once it returns.
	it("will not re-race again inside the cooldown", () => {
		expect(shouldReRace(at({ lastReRaceAt: 100_000 - RE_RACE_COOLDOWN_MS + 1 }))).toBe(false);
	});

	it("re-races again once the cooldown has passed", () => {
		expect(shouldReRace(at({ lastReRaceAt: 100_000 - RE_RACE_COOLDOWN_MS - 1 }))).toBe(true);
	});

	// Leaving a network makes requests fail with no HTTP status at all — there
	// was no server to answer. That is not a blip to ride out, it is the signal
	// that the endpoint is gone, so racing on the very first one saves waiting
	// out another poll interval and another timeout: up to forty seconds of the
	// app looking broken.
	it("races immediately when the endpoint is unreachable", () => {
		expect(shouldReRace(at({ consecutiveFailures: 1, unreachable: true }))).toBe(true);
	});

	// A server that answered — 401, 500, a lockout — is reachable. Racing would
	// find the same endpoint and change nothing.
	it("does not race when the server answered with an error", () => {
		expect(shouldReRace(at({ consecutiveFailures: 1, unreachable: false }))).toBe(false);
	});

	// The cooldown still applies, or an offline phone would race on every poll.
	it("respects the cooldown even when unreachable", () => {
		expect(
			shouldReRace(at({ consecutiveFailures: 1, unreachable: true, lastReRaceAt: 100_000 - 1_000 })),
		).toBe(false);
	});

	// First ever failure run: there is no previous attempt to wait behind.
	it("re-races on the first failure run without waiting", () => {
		expect(shouldReRace(at({ lastReRaceAt: 0, now: 5_000 }))).toBe(true);
	});
});
