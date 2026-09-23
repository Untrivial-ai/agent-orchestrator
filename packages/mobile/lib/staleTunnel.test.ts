import { describe, expect, it } from "vitest";
import type { Endpoint } from "./endpoints";
import { tunnelMayHaveRotated } from "./staleTunnel";

const lan: Endpoint = { kind: "lan", host: "192.168.1.42", port: 3011, secure: false };
const tailscale: Endpoint = { kind: "tailscale", host: "100.72.46.7", port: 3011, secure: false };
const tunnel: Endpoint = { kind: "tunnel", host: "old.trycloudflare.com", port: 443, secure: true };

// A quick tunnel takes a new hostname every time the connector restarts, and
// the phone only learns the new one by connecting — which it cannot do when the
// stored tunnel is the dead one and there is no LAN or tailnet to fall back to.
// Nothing in the app can fix that; what it can do is stop showing a bare
// "can't connect" and say the code needs rescanning.
describe("recognising a rotated tunnel", () => {
	it("suspects rotation when the only remote path is a tunnel that failed", () => {
		expect(tunnelMayHaveRotated([lan, tunnel], false)).toBe(true);
	});

	// Nothing to rotate: a machine with no tunnel is simply out of range.
	it("does not suspect rotation when no tunnel was ever advertised", () => {
		expect(tunnelMayHaveRotated([lan, tailscale], false)).toBe(false);
	});

	// If anything answered, the machine is reachable and the hostname is fine.
	it("does not suspect rotation while something is still reachable", () => {
		expect(tunnelMayHaveRotated([lan, tunnel], true)).toBe(false);
	});

	it("has nothing to suspect with no endpoints at all", () => {
		expect(tunnelMayHaveRotated([], false)).toBe(false);
	});
});
