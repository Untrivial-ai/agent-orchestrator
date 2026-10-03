import { afterEach, describe, expect, it, vi } from "vitest";

// Everything below the seam reaches into native storage; only the refresh
// wiring is under test here.
vi.mock("./config", () => ({}));
vi.mock("./hosts", () => ({
	findHost: vi.fn(),
	updateHostEndpoints: vi.fn(),
	adoptHostIdentity: vi.fn(),
	touchHost: vi.fn(),
}));

import { ENDPOINT_REFRESH_TIMEOUT_MS, probeEndpoint, probeIdentity, rejectedEndpointNeedsRace, runtimeConnectDeps } from "./connectRuntime";
import { IncompatibleHostVersionError } from "./race";

const config = { host: "192.168.1.5", httpPort: "3011", password: "stale", secure: false } as Parameters<ReturnType<typeof runtimeConnectDeps>["refreshEndpoints"]>[0];

afterEach(() => {
	vi.unstubAllGlobals();
	vi.useRealTimers();
});

// The endpoint refresh is authenticated, so with a stale password it counts
// towards the daemon's lockout. Settings' Test connection turns it off so a
// tap spends one attempt (its own ping), not two.
describe("runtimeConnectDeps", () => {
	it("checks a rejected address without presenting the saved bearer", async () => {
		const fetch = vi.fn()
			.mockResolvedValueOnce({ ok: true, json: async () => ({ hostId: "another-host", apiVersion: 1 }) })
			.mockResolvedValueOnce({ ok: true, json: async () => ({ hostId: "paired-host", apiVersion: 1 }) });
		vi.stubGlobal("fetch", fetch);
		const paired = { ...config, hostId: "paired-host", endpointKind: "lan" as const };

		await expect(rejectedEndpointNeedsRace(paired, 401)).resolves.toBe(true);
		await expect(rejectedEndpointNeedsRace(paired, 403)).resolves.toBe(false);
		await expect(rejectedEndpointNeedsRace(paired, 421)).resolves.toBe(true);
		await expect(rejectedEndpointNeedsRace(paired, 429)).resolves.toBe(false);
		expect(fetch).toHaveBeenCalledTimes(2);
		for (const [url, init] of fetch.mock.calls) {
			expect(url).toBe("http://192.168.1.5:3011/api/v1/identity");
			expect(init).toMatchObject({ method: "GET" });
			expect(init.headers).toBeUndefined();
		}
	});

	it("skips the authenticated endpoint refresh when asked", async () => {
		const fetch = vi.fn();
		vi.stubGlobal("fetch", fetch);
		await expect(runtimeConnectDeps({ refreshEndpoints: false }).refreshEndpoints(config)).resolves.toEqual([]);
		expect(fetch).not.toHaveBeenCalled();
	});

	it("refreshes by default", async () => {
		const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ endpoints: [] }) });
		vi.stubGlobal("fetch", fetch);
		await runtimeConnectDeps().refreshEndpoints({ ...config, hostId: "h_A" });
		expect(fetch).toHaveBeenCalledWith("http://192.168.1.5:3011/api/v1/endpoints", {
			headers: { Authorization: "Bearer stale", "X-AO-Expected-Host-ID": "h_A" },
			signal: expect.any(AbortSignal),
		});
	});

	// The refresh runs inside launch resolution, after the race already found a
	// working endpoint. A hung request on a slow network must not hold the app
	// in "connecting" for as long as the OS lets it hang.
	it("gives up on a refresh that does not answer in time", async () => {
		vi.useFakeTimers();
		const fetch = vi.fn(
			(_url: string, init: { signal: AbortSignal }) =>
				new Promise((_resolve, reject) => {
					init.signal.addEventListener("abort", () => reject(new Error("aborted")));
				}),
		);
		vi.stubGlobal("fetch", fetch);
		const refresh = runtimeConnectDeps().refreshEndpoints(config);
		const settled = expect(refresh).rejects.toThrow("aborted");
		await vi.advanceTimersByTimeAsync(ENDPOINT_REFRESH_TIMEOUT_MS);
		await settled;
	});
});

describe("identity API compatibility", () => {
	it("accepts the PR-base v1 identity without sending a password", async () => {
		const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ hostId: "h_A", apiVersion: 1 }) });
		vi.stubGlobal("fetch", fetch);
		const endpoint = { kind: "lan" as const, host: "192.168.1.5", port: 3011, secure: false };
		await expect(probeEndpoint(endpoint, new AbortController().signal)).resolves.toEqual({ hostId: "h_A" });
		expect(fetch.mock.calls[0][1].headers).toBeUndefined();
	});

	it.each([undefined, 2])("rejects identity version %s before authentication", async (apiVersion) => {
		const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ hostId: "h_A", apiVersion }) });
		vi.stubGlobal("fetch", fetch);
		await expect(probeIdentity(config)).rejects.toBeInstanceOf(IncompatibleHostVersionError);
		await expect(probeEndpoint({ kind: "lan", host: config.host, port: 3011, secure: false }, new AbortController().signal))
			.rejects.toBeInstanceOf(IncompatibleHostVersionError);
		expect(fetch.mock.calls.every(([, init]) => !init?.headers?.Authorization)).toBe(true);
	});
});
