import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));

import { previewFilePath } from "./api";

describe("daemon preview-files route", () => {
	it("escapes the session id and each path segment while keeping the separators", () => {
		expect(previewFilePath("sess 1", ".ao/attachments/attachment-a b.png")).toBe(
			"/api/v1/sessions/sess%201/preview/files/.ao/attachments/attachment-a%20b.png",
		);
		expect(previewFilePath("s", "dist/index.html")).toBe("/api/v1/sessions/s/preview/files/dist/index.html");
	});
});
