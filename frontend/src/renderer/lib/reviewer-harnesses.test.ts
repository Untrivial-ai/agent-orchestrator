import { describe, expect, it } from "vitest";
import { KNOWN_REVIEWER_HARNESS_IDS, toReviewerHarnessId } from "./reviewer-harnesses";

describe("OpenCode 2 reviewer identity", () => {
	it("is selectable as a distinct reviewer harness", () => {
		expect(KNOWN_REVIEWER_HARNESS_IDS.has("opencode-v2")).toBe(true);
		expect(toReviewerHarnessId("opencode-v2")).toBe("opencode-v2");
		expect(toReviewerHarnessId("opencode")).toBe("opencode");
	});
});
