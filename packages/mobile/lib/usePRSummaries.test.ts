import { describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: {} }));
vi.mock("expo-secure-store", () => ({}));
vi.mock("./api", () => ({ getSessionPR: vi.fn() }));

import { DEFAULT_CONFIG } from "./config";
import { prSummaryCacheKey } from "./usePRSummaries";

describe("PR summary cache identity", () => {
	it("separates the same session ID on two hosts while sharing one host across endpoints", () => {
		const hostA = { ...DEFAULT_CONFIG, hostId: "host-a", host: "192.168.1.10" };
		const hostB = { ...DEFAULT_CONFIG, hostId: "host-b", host: "192.168.1.11" };
		const hostAOverTunnel = { ...hostA, host: "a.example.com", secure: true };

		expect(prSummaryCacheKey(hostA, "same-session")).not.toBe(prSummaryCacheKey(hostB, "same-session"));
		expect(prSummaryCacheKey(hostA, "same-session")).toBe(prSummaryCacheKey(hostAOverTunnel, "same-session"));
	});
});
