import { describe, expect, it } from "vitest";
import type { Endpoint } from "./endpoints";
import { mergeEndpoints } from "./mergeEndpoints";

const lan: Endpoint = { kind: "lan", host: "192.168.1.2", port: 3011, secure: false };
const lan2: Endpoint = { kind: "lan", host: "192.168.1.9", port: 3011, secure: false };
const tailscale: Endpoint = { kind: "tailscale", host: "100.1.1.1", port: 3011, secure: false };
const oldTunnel: Endpoint = { kind: "tunnel", host: "old.trycloudflare.com", port: 443, secure: true };
const newTunnel: Endpoint = { kind: "tunnel", host: "new.trycloudflare.com", port: 443, secure: true };

describe("merging a refreshed endpoint list over the stored one", () => {
	it("takes the fresh list when it covers the same kinds", () => {
		expect(mergeEndpoints([lan, oldTunnel], [lan, newTunnel])).toEqual([lan, newTunnel]);
	});

	// The bug this exists for, measured on device: a quick tunnel takes ~34s to
	// restart and settle, and the daemon correctly advertises no tunnel for that
	// whole window. A refresh landing inside it used to erase the only endpoint
	// that works from outside the house, and nothing refreshed again until the
	// next race — so the phone could be left with no route home at all.
	it("keeps a known tunnel when the refresh has none", () => {
		expect(mergeEndpoints([lan, oldTunnel], [lan])).toEqual([lan, oldTunnel]);
	});

	// A stale hostname costs one failed probe in a race that runs candidates
	// concurrently. Dropping it costs every remote connection until something
	// forces a refresh. The asymmetry is the whole argument for keeping it.
	it("prefers a stale candidate over no candidate", () => {
		const merged = mergeEndpoints([oldTunnel], []);
		expect(merged).toEqual([oldTunnel]);
	});

	it("replaces every endpoint of a kind the refresh does mention", () => {
		expect(mergeEndpoints([lan, lan2], [lan2])).toEqual([lan2]);
	});

	it("keeps kinds the refresh omits while taking the ones it has", () => {
		expect(mergeEndpoints([lan, tailscale, oldTunnel], [lan2])).toEqual([lan2, tailscale, oldTunnel]);
	});

	it("returns the stored list unchanged when the refresh is empty", () => {
		expect(mergeEndpoints([lan, tailscale], [])).toEqual([lan, tailscale]);
	});

	it("does not duplicate an endpoint present in both", () => {
		expect(mergeEndpoints([lan], [lan])).toEqual([lan]);
	});

	it("has nothing to keep on a first pairing", () => {
		expect(mergeEndpoints([], [lan, newTunnel])).toEqual([lan, newTunnel]);
	});
});
