import { describe, expect, it, vi } from "vitest";
import { raceEndpoints } from "./race";
import type { Endpoint } from "./endpoints";

const lan: Endpoint = { kind: "lan", host: "192.168.1.42", port: 3011, secure: false };
const tailscale: Endpoint = { kind: "tailscale", host: "100.72.46.7", port: 3011, secure: false };
const tunnel: Endpoint = { kind: "tunnel", host: "abc.trycloudflare.com", port: 443, secure: true };

/** A probe that answers per-host, after an optional delay. */
const probeWith = (
	answers: Record<string, { hostId?: string; delayMs?: number; fail?: boolean }>,
) =>
	vi.fn(async (e: Endpoint) => {
		const a = answers[e.host];
		if (a?.delayMs) await new Promise((r) => setTimeout(r, a.delayMs));
		if (!a || a.fail) throw new Error("unreachable");
		return { hostId: a.hostId ?? "h_paired" };
	});

describe("raceEndpoints", () => {
	it("returns the endpoint that answers", async () => {
		const got = await raceEndpoints([lan], "h_paired", probeWith({ "192.168.1.42": {} }));

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.endpoint).toEqual(lan);
	});

	it("reports failure when nothing answers", async () => {
		const got = await raceEndpoints([lan, tunnel], "h_paired", probeWith({}));

		expect(got.ok).toBe(false);
	});

	it("reports failure for an empty candidate list", async () => {
		// No network at all is a real state, not a crash.
		const got = await raceEndpoints([], "h_paired", probeWith({}));

		expect(got.ok).toBe(false);
	});

	it("ignores an endpoint that fails and keeps the one that answers", async () => {
		const got = await raceEndpoints(
			[lan, tunnel],
			"h_paired",
			probeWith({ "192.168.1.42": { fail: true }, "abc.trycloudflare.com": {} }),
		);

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.endpoint.kind).toBe("tunnel");
	});

	// The security property. A private address is not an identity: 192.168.1.42
	// exists on most networks and is a different machine on each. Trusting it
	// because something answered would send this device's bearer token to a
	// stranger's device.
	it("refuses an endpoint whose host identity does not match", async () => {
		const got = await raceEndpoints(
			[lan],
			"h_paired",
			probeWith({ "192.168.1.42": { hostId: "h_someone_elses_laptop" } }),
		);

		expect(got.ok).toBe(false);
	});

	it("skips the impostor and keeps racing the rest", async () => {
		const got = await raceEndpoints(
			[lan, tunnel],
			"h_paired",
			probeWith({
				"192.168.1.42": { hostId: "h_someone_elses_laptop" },
				"abc.trycloudflare.com": {},
			}),
		);

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.endpoint.kind).toBe("tunnel");
	});

	// A preferred endpoint that is only slightly slower still wins: the grace
	// window lets it catch up, because a 50ms head start is not worth spending
	// the whole session on the slow path.
	it("lets a slightly slower preferred endpoint win", async () => {
		const got = await raceEndpoints(
			[lan, tunnel],
			"h_paired",
			probeWith({
				"192.168.1.42": { delayMs: 40 },
				"abc.trycloudflare.com": { delayMs: 1 },
			}),
			{ graceMs: 200 },
		);

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.endpoint.kind).toBe("lan");
	});

	// Past the grace window the fast answer is taken. Otherwise a LAN address
	// that is merely slow — a sleeping host, a saturated link — would hold up
	// every connect while a working tunnel sat idle.
	it("does not wait indefinitely for a preferred endpoint", async () => {
		const got = await raceEndpoints(
			[lan, tunnel],
			"h_paired",
			probeWith({
				"192.168.1.42": { delayMs: 400 },
				"abc.trycloudflare.com": { delayMs: 1 },
			}),
			{ graceMs: 50 },
		);

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.endpoint.kind).toBe("tunnel");
	});

	// The grace window starts at the first success, so an all-fast race is not
	// slowed down by it.
	it("returns promptly when every endpoint answers quickly", async () => {
		const started = Date.now();
		const got = await raceEndpoints(
			[lan, tunnel],
			"h_paired",
			probeWith({ "192.168.1.42": {}, "abc.trycloudflare.com": {} }),
			{ graceMs: 500 },
		);

		expect(got.ok).toBe(true);
		// All probes settled, so there is nothing left to wait for.
		expect(Date.now() - started).toBeLessThan(400);
	});

	// A machine migrated from the single-server config has no id yet — the
	// daemon only began issuing them alongside the race. It must still connect,
	// adopting the identity it is told, which is exactly how the app behaved
	// before identity existed. One successful connect and it is verified from
	// then on.
	it("accepts any identity when the host has none recorded yet", async () => {
		const got = await raceEndpoints(
			[lan],
			"",
			probeWith({ "192.168.1.42": { hostId: "h_learned_now" } }),
		);

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.hostId).toBe("h_learned_now");
	});

	it("reports the identity that answered so it can be recorded", async () => {
		const got = await raceEndpoints([lan], "h_paired", probeWith({ "192.168.1.42": {} }));

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.hostId).toBe("h_paired");
	});

	// On a tie, prefer the cheapest path. Measured: LAN is ~1ms and 328MB/s
	// against the tunnel's 11-65ms and 4.4MB/s.
	it("prefers lan over tailscale over tunnel when they answer together", async () => {
		const got = await raceEndpoints(
			[tunnel, tailscale, lan],
			"h_paired",
			probeWith({
				"abc.trycloudflare.com": {},
				"100.72.46.7": {},
				"192.168.1.42": {},
			}),
		);

		expect(got.ok).toBe(true);
		if (got.ok) expect(got.endpoint.kind).toBe("lan");
	});
});
