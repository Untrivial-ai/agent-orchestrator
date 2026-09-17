import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

import { DEFAULT_CONFIG, type ServerConfig } from "./config";
import { sameServerConfig } from "./sameConfig";

const cfg = (over: Partial<ServerConfig> = {}): ServerConfig => ({
	...DEFAULT_CONFIG, host: "192.168.1.42", httpPort: "3011", secure: false, password: "pw", ...over,
});

describe("sameServerConfig", () => {
	// Resolution builds a fresh object every time. Effects across the app key
	// on the config's identity — the live conversation stream, the poll loop —
	// so handing them a new object for an unchanged endpoint tears the stream
	// down and rebuilds it, leaving the UI to update only on the 8s poll.
	it("treats an identical endpoint as unchanged", () => {
		expect(sameServerConfig(cfg(), cfg())).toBe(true);
	});

	it.each([
		["host", { host: "10.0.0.5" }],
		["port", { httpPort: "443" }],
		["tls", { secure: true }],
		["password", { password: "rotated" }],
	])("treats a changed %s as different", (_name, over) => {
		expect(sameServerConfig(cfg(), cfg(over))).toBe(false);
	});

	// A machine migrated from the single-server config connects once without an
	// identity and adopts one. Everything else about the endpoint is unchanged,
	// so without comparing this the app would keep the previous object and the
	// event stream would carry on using the address-keyed cursor it should have
	// just graduated from.
	it("treats gaining a host identity as a change", () => {
		expect(sameServerConfig(cfg({ hostId: undefined }), cfg({ hostId: "h_abc" }))).toBe(false);
	});

	it("treats a different machine on the same address as a change", () => {
		expect(sameServerConfig(cfg({ hostId: "h_abc" }), cfg({ hostId: "h_xyz" }))).toBe(false);
	});

	it("handles a missing previous config", () => {
		expect(sameServerConfig(null, cfg())).toBe(false);
	});
});
