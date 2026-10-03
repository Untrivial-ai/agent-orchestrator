import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));

import { clearNotification } from "./api";
import type { ServerConfig } from "./config";

const hostA: ServerConfig = { host: "a.test", hostId: "h_a", httpPort: "3011", muxPort: "", password: "token-a" };
const hostB: ServerConfig = { host: "b.test", hostId: "h_b", httpPort: "3011", muxPort: "", password: "token-b" };

describe("mobile notification clear", () => {
	beforeEach(() => vi.stubGlobal("fetch", vi.fn()));
	afterEach(() => vi.unstubAllGlobals());

	it("uses only the selected host's endpoint and bearer, even for a colliding ID", async () => {
		vi.mocked(fetch).mockResolvedValue(new Response("{}"));
		await clearNotification(hostB, "same/id");
		expect(fetch).toHaveBeenCalledExactlyOnceWith(
			"http://b.test:3011/api/v1/notifications/same%2Fid",
			expect.objectContaining({ method: "DELETE", headers: expect.objectContaining({ Authorization: "Bearer token-b" }) }),
		);
		expect(vi.mocked(fetch).mock.calls[0][0]).not.toContain(hostA.host);
	});

	it("treats an already-cleared row as success", async () => {
		vi.mocked(fetch).mockResolvedValue(
			new Response(JSON.stringify({ code: "NOTIFICATION_NOT_FOUND", message: "Unknown notification" }), { status: 404 }),
		);
		await expect(clearNotification(hostA, "gone")).resolves.toBeUndefined();
	});

	it("does not hide a missing delete route as a successful clear", async () => {
		vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ code: "NOT_FOUND" }), { status: 404 }));
		await expect(clearNotification(hostA, "gone")).rejects.toMatchObject({ status: 404, code: "NOT_FOUND" });
	});
});
