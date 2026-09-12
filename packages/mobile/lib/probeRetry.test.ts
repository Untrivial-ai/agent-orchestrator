import { describe, expect, it } from "vitest";
import { shouldRetryProbe, TUNNEL_PROBE_WINDOW_MS } from "./probeRetry";

// A tunnel hostname is advertised before every resolver in the world knows it.
// The daemon cannot test that: probing from the machine that owns the tunnel
// checks its own resolver, and doing it early caches an NXDOMAIN locally that
// outlives propagation. The phone's resolver is the one that decides, so the
// phone is where a retry belongs.
describe("retrying a probe while a hostname propagates", () => {
	it("retries a tunnel candidate inside the window", () => {
		expect(shouldRetryProbe("tunnel", 0)).toBe(true);
		expect(shouldRetryProbe("tunnel", TUNNEL_PROBE_WINDOW_MS - 1)).toBe(true);
	});

	it("gives up once the window has passed", () => {
		expect(shouldRetryProbe("tunnel", TUNNEL_PROBE_WINDOW_MS)).toBe(false);
	});

	// A LAN or tailnet address either resolves immediately or is not there.
	// Retrying would only delay the race behind an address that cannot appear.
	it("never retries a direct candidate", () => {
		expect(shouldRetryProbe("lan", 0)).toBe(false);
		expect(shouldRetryProbe("tailscale", 0)).toBe(false);
	});
});
