import { describe, expect, it } from "vitest";
import type { Endpoint, EndpointKind } from "./endpoints";
import { RE_RACE_COOLDOWN_MS } from "./reRace";
import {
	shouldRaceForUpgrade,
	UPGRADE_RACE_INTERVAL_MS,
	type UpgradeRaceState,
} from "./upgradeRace";

const lan: Endpoint = { kind: "lan", host: "192.168.1.2", port: 3011, secure: false };
const tailscale: Endpoint = { kind: "tailscale", host: "100.1.1.1", port: 3011, secure: false };
const tunnel: Endpoint = { kind: "tunnel", host: "x.trycloudflare.com", port: 443, secure: true };

function state(over: Partial<UpgradeRaceState> = {}): UpgradeRaceState {
	return {
		currentKind: "tunnel",
		known: [lan, tunnel],
		lastRaceAt: 1_000_000,
		now: 1_000_000 + UPGRADE_RACE_INTERVAL_MS,
		resumed: false,
		...over,
	};
}

describe("racing to move back onto a better endpoint", () => {
	// The bug this exists for: once the app falls to the tunnel it stays there
	// even after Wi-Fi comes back, because the only thing that re-races is
	// failure. Observed on device — the phone held a Cloudflare connection with
	// a working LAN sitting unused.
	it("races when a better endpoint than the current one is known", () => {
		expect(shouldRaceForUpgrade(state())).toBe(true);
	});

	it("does not race when already on the best known endpoint", () => {
		expect(shouldRaceForUpgrade(state({ currentKind: "lan" }))).toBe(false);
	});

	// Nothing better exists, so a race would rediscover the same endpoint and
	// spend battery for no gain.
	it("does not race when the tunnel is the only thing known", () => {
		expect(shouldRaceForUpgrade(state({ known: [tunnel] }))).toBe(false);
	});

	it("waits out the interval between attempts", () => {
		expect(shouldRaceForUpgrade(state({ now: 1_000_000 + UPGRADE_RACE_INTERVAL_MS - 1 }))).toBe(
			false,
		);
	});

	// Waking somewhere new is the case that matters: phone sleeps at home on
	// Wi-Fi, wakes on cellular, or the reverse. Waiting out a full interval
	// there leaves the app on the wrong path exactly when the user is looking,
	// so a resume races on the much shorter cooldown instead.
	it("races on resume well before the periodic interval would allow", () => {
		const soon = 1_000_000 + RE_RACE_COOLDOWN_MS;
		expect(shouldRaceForUpgrade(state({ now: soon, resumed: true }))).toBe(true);
		expect(shouldRaceForUpgrade(state({ now: soon, resumed: false }))).toBe(false);
	});

	// A resume still must not thrash: backgrounding and foregrounding repeatedly
	// would otherwise race on every switch.
	it("does not race on resume within the cooldown of the last race", () => {
		expect(shouldRaceForUpgrade(state({ now: 1_000_000 + 500, resumed: true }))).toBe(false);
	});

	it("races on resume once the cooldown has passed", () => {
		expect(
			shouldRaceForUpgrade(state({ now: 1_000_000 + 20_000, resumed: true, known: [tailscale, tunnel] })),
		).toBe(true);
	});

	// A migrated host has no recorded kind. Racing blindly on a timer would
	// undo the "keep the previous object" work that keeps streams alive.
	it("does not race when the current endpoint kind is unknown", () => {
		expect(shouldRaceForUpgrade(state({ currentKind: undefined }))).toBe(false);
	});

	// An endpoint kind a newer daemon advertises that this build does not rank
	// must not be treated as an upgrade over everything.
	it("does not treat an unknown advertised kind as better", () => {
		const exotic = { kind: "quantum" as EndpointKind, host: "h", port: 1, secure: true };
		expect(shouldRaceForUpgrade(state({ currentKind: "tunnel", known: [exotic, tunnel] }))).toBe(
			false,
		);
	});
});
