import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

import AsyncStorage from "@react-native-async-storage/async-storage";
import { DEFAULT_CONFIG, machineIdentity, type ServerConfig } from "../config";
import { clearEventCursorsForHost, eventCursorKey, HEAD_CURSOR, initialCursorFor } from "./eventCursor";

const cfg = (over: Partial<ServerConfig> = {}): ServerConfig => ({
	...DEFAULT_CONFIG, host: "192.168.1.42", httpPort: "3011", secure: false, password: "pw", ...over,
});

describe("eventCursorKey", () => {
	// The identity rule moved to machineIdentity so the store's retained project
	// list keys on the same one (#5058 review). Two callers that disagreed about
	// what counts as the same machine would be a bug in whichever was wrong.
	it("is the shared machine identity behind a stable prefix", () => {
		const identified = cfg({ hostId: "h_abc" });
		expect(eventCursorKey(identified)).toBe(`ao.chat.events.${machineIdentity(identified)}`);
	});

	// Pinned as literals, not against the helper that produces them: this key is
	// where a real device's cursor lives, and changing its shape silently orphans
	// every stored cursor — which costs a full replay, 14,338 events and ~20s on
	// the measured daemon, on every machine the app has ever talked to. A test
	// written as `toBe(\`ao.chat.events.${machineIdentity(cfg)}\`)` cannot catch
	// that, because both sides move together.
	it("is exactly the key this shipped with", () => {
		expect(eventCursorKey(cfg({ hostId: "h_abc" }))).toBe("ao.chat.events.host.h_abc");
		expect(eventCursorKey(cfg({ hostId: undefined, host: "192.168.1.42", httpPort: "3011", secure: false }))).toBe(
			"ao.chat.events.http.192.168.1.42.3011",
		);
		expect(eventCursorKey(cfg({ hostId: undefined, host: "x.trycloudflare.com", httpPort: "443", secure: true }))).toBe(
			"ao.chat.events.https.x.trycloudflare.com.443",
		);
	});

	// The cursor was keyed by address, so the same machine reached over LAN,
	// Tailscale and the tunnel had three separate cursors. Every network change
	// therefore started from zero and replayed the entire backlog — measured at
	// 14,338 events and 3 MB, which is the ~20s stall before the first reply
	// appears.
	it("is the same for one machine across every endpoint it answers on", () => {
		const viaLan = eventCursorKey(cfg({ hostId: "h_abc", host: "192.168.1.42", httpPort: "3011" }));
		const viaTailscale = eventCursorKey(cfg({ hostId: "h_abc", host: "100.72.46.7", httpPort: "3011" }));
		const viaTunnel = eventCursorKey(cfg({ hostId: "h_abc", host: "x.trycloudflare.com", httpPort: "443", secure: true }));

		expect(viaTailscale).toBe(viaLan);
		expect(viaTunnel).toBe(viaLan);
	});

	it("keeps different machines apart", () => {
		expect(eventCursorKey(cfg({ hostId: "h_abc" }))).not.toBe(eventCursorKey(cfg({ hostId: "h_xyz" })));
	});

	// A pairing migrated from the single-server config has no identity until it
	// connects once, so it still has to fall back to the address.
	it("falls back to the address when the machine has no identity yet", () => {
		const a = eventCursorKey(cfg({ hostId: undefined, host: "192.168.1.42" }));
		const b = eventCursorKey(cfg({ hostId: undefined, host: "10.0.0.5" }));
		expect(a).not.toBe(b);
		expect(a).toContain("192.168.1.42");
	});
});

describe("clearEventCursorsForHost", () => {
	it("uses address keys for a migrated host with no identity", async () => {
		await clearEventCursorsForHost({
			id: "",
			name: "old desktop",
			platform: "",
			endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			lastConnected: 1,
		});

		expect(AsyncStorage.removeItem).toHaveBeenCalledWith("ao.chat.events.http.192.168.1.42.3011");
		expect(AsyncStorage.removeItem).not.toHaveBeenCalledWith("ao.chat.events.host.");
	});
});

describe("initialCursorFor", () => {
	// A first connection has no business replaying history: chat content comes
	// from REST, and the stream exists for live updates. Asking beyond head makes
	// the daemon clamp to head and report it back, so a fresh pairing starts
	// live instead of chewing through 14,338 stale events.
	it("starts at head when there is no stored cursor", () => {
		expect(initialCursorFor(null)).toBe(HEAD_CURSOR);
	});

	it("resumes from a stored cursor", () => {
		expect(initialCursorFor("4210")).toBe(4210);
	});

	it("starts at head when the stored value is unusable", () => {
		for (const bad of ["", "not-a-number", "-5"]) {
			expect(initialCursorFor(bad)).toBe(HEAD_CURSOR);
		}
	});

	// Zero is what the old address-keyed cursor produced on every network change.
	// Honouring it would replay the whole backlog exactly as before.
	it("treats a stored zero as head rather than a full replay", () => {
		expect(initialCursorFor("0")).toBe(HEAD_CURSOR);
	});
});
