import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

import { DEFAULT_CONFIG, type ServerConfig } from "../config";
import { CONVERSATION_POLL_MS, conversationPollIntervalFor } from "./conversationPoll";

const cfg = (over: Partial<ServerConfig> = {}): ServerConfig => ({
	...DEFAULT_CONFIG, host: "h", httpPort: "3011", password: "pw", ...over,
});

describe("conversationPollIntervalFor", () => {
	// The chat screen loads once and then relies entirely on the event stream.
	// Over a Cloudflare quick tunnel that stream cannot deliver — the body is
	// forwarded in ~128 KB chunks and a chat event is a few hundred bytes — so
	// without a poll the conversation simply never updates. Speeding up the
	// store's poll did not help: that one refreshes the session list, not the
	// conversation.
	it("polls the conversation over the tunnel, where the stream cannot deliver", () => {
		expect(conversationPollIntervalFor(cfg({ endpointKind: "tunnel" }))).toBe(CONVERSATION_POLL_MS);
	});

	// On direct paths the stream delivers, and polling the conversation on top
	// of it would be pure waste.
	it.each(["lan", "tailscale"] as const)("does not poll over %s", (kind) => {
		expect(conversationPollIntervalFor(cfg({ endpointKind: kind }))).toBeNull();
	});

	it("does not poll when the endpoint kind is unknown", () => {
		expect(conversationPollIntervalFor(cfg({ endpointKind: undefined }))).toBeNull();
		expect(conversationPollIntervalFor(null)).toBeNull();
	});
});
