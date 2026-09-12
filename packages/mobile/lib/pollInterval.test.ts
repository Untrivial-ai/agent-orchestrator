import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

import { DEFAULT_CONFIG, type ServerConfig } from "./config";
import { DIRECT_POLL_MS, pollIntervalFor, TUNNEL_POLL_MS } from "./pollInterval";

const cfg = (over: Partial<ServerConfig> = {}): ServerConfig => ({
	...DEFAULT_CONFIG, host: "192.168.1.42", httpPort: "3011", password: "pw", ...over,
});

describe("pollIntervalFor", () => {
	// Measured: a Cloudflare quick tunnel forwards the body in ~128 KB chunks,
	// so a few-hundred-byte chat event is never pushed through on its own. The
	// live stream cannot deliver over that path, which leaves polling as the
	// only thing that moves the UI — so it has to be quick enough to feel live.
	it("polls quickly over the tunnel, where the event stream cannot deliver", () => {
		expect(pollIntervalFor(cfg({ endpointKind: "tunnel" }))).toBe(TUNNEL_POLL_MS);
		expect(TUNNEL_POLL_MS).toBeLessThan(DIRECT_POLL_MS);
	});

	// Direct paths stream fine, so the poll is only a backstop and should stay
	// cheap on battery and data.
	it.each(["lan", "tailscale"] as const)("keeps the normal interval over %s", (kind) => {
		expect(pollIntervalFor(cfg({ endpointKind: kind }))).toBe(DIRECT_POLL_MS);
	});

	// A pairing made before this field existed, or a config restored from
	// storage without it, must not silently start polling every couple of
	// seconds forever.
	it("keeps the normal interval when the endpoint kind is unknown", () => {
		expect(pollIntervalFor(cfg({ endpointKind: undefined }))).toBe(DIRECT_POLL_MS);
		expect(pollIntervalFor(null)).toBe(DIRECT_POLL_MS);
	});
});
