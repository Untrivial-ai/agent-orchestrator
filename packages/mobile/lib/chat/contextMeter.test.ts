import { describe, expect, it } from "vitest";

import { contextMeterModel } from "./contextMeter";
import type { ConversationUsage } from "./types";

const usage = (contextUsed: number, contextWindow = 100): ConversationUsage => ({
	contextUsed,
	contextWindow,
	inputTokens: 0,
	outputTokens: 0,
	cachedTokens: 0,
	totalTokens: contextUsed,
});

describe("contextMeterModel", () => {
	it("stays hidden while there is budget to spare", () => {
		expect(contextMeterModel(usage(10), false)).toBeNull();
		expect(contextMeterModel(usage(69), false)).toBeNull();
	});

	it("appears at the warn threshold", () => {
		const model = contextMeterModel(usage(70), false);
		expect(model?.severity).toBe("warn");
		expect(model?.percent).toBe(70);
		expect(model?.label).toBe("70% context");
	});

	it("escalates to critical at 90%", () => {
		expect(contextMeterModel(usage(90), false)?.severity).toBe("critical");
		expect(contextMeterModel(usage(97), false)?.severity).toBe("critical");
	});

	// Two resource warnings at once read as noise, not urgency.
	it("yields to the account-quota banner", () => {
		expect(contextMeterModel(usage(95), true)).toBeNull();
	});

	it("shows nothing without usage", () => {
		expect(contextMeterModel(undefined, false)).toBeNull();
	});

	it("shows nothing when the harness reports no context window", () => {
		expect(contextMeterModel(usage(5000, 0), false)).toBeNull();
	});

	it("keeps the bar visible rather than letting it round to nothing", () => {
		const model = contextMeterModel(usage(7001, 10000), false);
		expect(model?.fillPercent).toBeGreaterThanOrEqual(2);
	});
});
