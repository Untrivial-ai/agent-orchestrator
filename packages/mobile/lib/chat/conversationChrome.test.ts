import { describe, expect, it } from "vitest";
import { contextReadout, elapsedLabel, mcpServerFailureLabel, quotaWarning, resetLabel, workingElapsedLabel } from "./conversationChrome";

describe("mobile Chat conversation chrome", () => {
	it("uses the same context thresholds and visible minimum as desktop", () => {
		expect(contextReadout({ contextUsed: 1, contextWindow: 1000, inputTokens: 0, outputTokens: 0, cachedTokens: 0, totalTokens: 1 }))
			.toMatchObject({ percent: 0, fillPercent: 2, severity: "normal" });
		expect(contextReadout({ contextUsed: 70, contextWindow: 100, inputTokens: 0, outputTokens: 0, cachedTokens: 0, totalTokens: 70 })?.severity).toBe("warn");
		expect(contextReadout({ contextUsed: 900, contextWindow: 1000, inputTokens: 0, outputTokens: 0, cachedTokens: 0, totalTokens: 900 })?.severity).toBe("critical");
	});

	it("warns on the tighter reported quota window and ignores absent ones", () => {
		expect(quotaWarning({ primaryUsedPercent: -1, secondaryUsedPercent: 82, secondaryResetsInSeconds: 7200, planLabel: "weekly" }))
			.toEqual({ percent: 82, severity: "warn", resetsInSeconds: 7200, planLabel: "weekly" });
		expect(quotaWarning({ primaryUsedPercent: 40, secondaryUsedPercent: -1 })).toBeUndefined();
		expect(quotaWarning({ primaryUsedPercent: 91, secondaryUsedPercent: 80 })?.severity).toBe("critical");
	});

	it("formats live turn and reset durations without wall-clock assumptions", () => {
		expect(elapsedLabel("2026-08-05T00:00:00Z", Date.parse("2026-08-05T00:02:03Z"))).toBe("2m 3s");
		expect(resetLabel(172_800)).toBe("2d");
	});

	it("starts a running turn at one second and advances from the server timestamp", () => {
		const startedAt = "2026-08-05T00:00:00Z";
		expect(workingElapsedLabel(startedAt, Date.parse(startedAt))).toBe("1s");
		expect(workingElapsedLabel(startedAt, Date.parse("2026-08-05T00:01:02Z"))).toBe("1m 2s");
	});

	it("keeps both the MCP failure class and provider diagnostic", () => {
		expect(mcpServerFailureLabel({ name: "github", status: "failed", failureReason: "auth", error: "token expired" }))
			.toBe("github (auth: token expired)");
	});
});
