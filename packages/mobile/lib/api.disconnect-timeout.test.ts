import { afterEach, describe, expect, it, vi } from "vitest";
import { unpairFromDaemon } from "./api";
import type { ServerConfig } from "./config";
import { UnreachableError } from "./connectionError";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn(),
}));

const config: ServerConfig = {
	host: "192.0.2.1",
	httpPort: "3011",
	muxPort: "14801",
	password: "secret",
};

describe("disconnect request timeout", () => {
	afterEach(() => {
		vi.useRealTimers();
		vi.unstubAllGlobals();
	});

	it("abandons the best-effort daemon unpair after two seconds", async () => {
		vi.useFakeTimers();
		vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => new Promise((_resolve, reject) => {
			init?.signal?.addEventListener("abort", () => {
				const error = new Error("aborted");
				error.name = "AbortError";
				reject(error);
			});
		})));

		let failure: unknown;
		const request = unpairFromDaemon(config, "install-id").catch((error) => {
			failure = error;
		});

		try {
			await vi.advanceTimersByTimeAsync(2_000);
			// Asserted by type, not wording: the copy is user-facing and changes.
			expect(failure).toBeInstanceOf(UnreachableError);
			expect(failure).toMatchObject({ reason: "timeout" });
		} finally {
			await vi.advanceTimersByTimeAsync(10_000);
			await request;
		}
	});
});
