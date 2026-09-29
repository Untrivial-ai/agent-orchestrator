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

import { ENDPOINT_REFRESH_TIMEOUT_MS, runtimeConnectDeps } from "./connectRuntime";

const config = { host: "192.168.1.5", httpPort: "3011", password: "stale", secure: false } as Parameters<ReturnType<typeof runtimeConnectDeps>["refreshEndpoints"]>[0];

afterEach(() => {
	vi.unstubAllGlobals();
	vi.useRealTimers();
});

// The endpoint refresh is authenticated, so with a stale password it counts
// towards the daemon's lockout. Settings' Test connection turns it off so a
// tap spends one attempt (its own ping), not two.
describe("runtimeConnectDeps", () => {
	it("skips the authenticated endpoint refresh when asked", async () => {
		const fetch = vi.fn();
		vi.stubGlobal("fetch", fetch);
		await expect(runtimeConnectDeps({ refreshEndpoints: false }).refreshEndpoints(config)).resolves.toEqual([]);
		expect(fetch).not.toHaveBeenCalled();
	});

	it("refreshes by default", async () => {
		const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ endpoints: [] }) });
		vi.stubGlobal("fetch", fetch);
		await runtimeConnectDeps().refreshEndpoints(config);
		expect(fetch).toHaveBeenCalledWith("http://192.168.1.5:3011/api/v1/endpoints", {
			headers: { Authorization: "Bearer stale" },
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
