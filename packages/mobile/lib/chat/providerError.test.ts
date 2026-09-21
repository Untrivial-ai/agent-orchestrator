import { describe, expect, it } from "vitest";

import { errorActivityDuplicatesTurn, providerErrorCopy } from "./providerError";
import type { ConversationActivity } from "./types";

const activity = (over: Partial<ConversationActivity>): ConversationActivity =>
	({ kind: "activity", activityKind: "error", status: "failed", sequence: 1, summary: "", ...over }) as ConversationActivity;

describe("providerErrorCopy", () => {
	// The reason this exists: Codex sends the usage-limit sentence as both the
	// summary and detail.error, and rendering each in turn printed one failure
	// twice inside a single card.
	it("says an identical message once", () => {
		const text = "You've hit your usage limit. Upgrade to Pro or try again at 10:29 PM.";
		const copy = providerErrorCopy(activity({ summary: text, detail: { error: text } as ConversationActivity["detail"] }));
		expect(copy.headline).toBe(text);
		expect(copy.detail).toBeUndefined();
	});

	it("keeps a detail that adds something", () => {
		const copy = providerErrorCopy(activity({
			summary: "The provider rejected the request",
			detail: { error: "quota_exceeded" } as ConversationActivity["detail"],
		}));
		expect(copy).toEqual({ headline: "The provider rejected the request", detail: "quota_exceeded" });
	});

	it("prefers detail.message as the headline", () => {
		const copy = providerErrorCopy(activity({
			summary: "Turn failed",
			detail: { message: "Rate limited" } as ConversationActivity["detail"],
		}));
		expect(copy.headline).toBe("Rate limited");
	});

	it("falls back to detail.error when there is no headline", () => {
		expect(providerErrorCopy(activity({ detail: { error: "boom" } as ConversationActivity["detail"] })).headline).toBe("boom");
	});

	it("never returns an empty headline", () => {
		expect(providerErrorCopy(activity({})).headline).toBe("Provider error");
	});

	describe("providers that wrap the real message in JSON", () => {
		it("unwraps message and additionalDetails", () => {
			const raw = 'stream error: {"error":{"message":"Usage limit reached","additionalDetails":"Resets at 10:29 PM"}}';
			expect(providerErrorCopy(activity({ summary: raw }))).toEqual({
				headline: "Usage limit reached",
				detail: "Resets at 10:29 PM",
			});
		});

		it("says it once when the wrapped fields agree", () => {
			const raw = '{"error":{"message":"Usage limit reached","additionalDetails":"Usage limit reached"}}';
			expect(providerErrorCopy(activity({ summary: raw }))).toEqual({ headline: "Usage limit reached" });
		});

		it("reads the fields even when the JSON does not parse", () => {
			const raw = 'error {"message":"Usage limit reached","additionalDetails":"Buy credits"} trailing junk';
			expect(providerErrorCopy(activity({ summary: raw })).headline).toBe("Usage limit reached");
		});
	});
});

describe("errorActivityDuplicatesTurn", () => {
	const text = "You've hit your usage limit. Upgrade to Pro or try again at 10:29 PM.";

	it("spots the activity restating its turn's failure", () => {
		const a = activity({ summary: text, detail: { error: text } as ConversationActivity["detail"] });
		expect(errorActivityDuplicatesTurn(a, text)).toBe(true);
	});

	it("keeps an activity that says something else", () => {
		const a = activity({ summary: "A tool crashed" });
		expect(errorActivityDuplicatesTurn(a, text)).toBe(false);
	});

	it("keeps everything when the turn reports no error", () => {
		const a = activity({ summary: text });
		expect(errorActivityDuplicatesTurn(a, undefined)).toBe(false);
		expect(errorActivityDuplicatesTurn(a, "   ")).toBe(false);
	});

	it("only ever drops error activities", () => {
		const a = activity({ activityKind: "command", summary: text });
		expect(errorActivityDuplicatesTurn(a, text)).toBe(false);
	});
});
