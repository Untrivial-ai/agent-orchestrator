import { describe, expect, it } from "vitest";

import {
	countWords,
	isLinkedInProfileUrl,
	isTweetUrl,
	keepWithinWordLimit,
} from "./testimonial-submission";

describe("testimonial word limits", () => {
	it("counts words separated by any whitespace", () => {
		expect(countWords("  AO makes\nparallel work\tclear.  ")).toBe(5);
	});

	it("leaves submissions within the limit unchanged", () => {
		expect(keepWithinWordLimit("one  two\nthree", "", 3)).toBe("one  two\nthree");
	});

	it("preserves whitespace while the submission is at the limit", () => {
		expect(keepWithinWordLimit("one  two\nthree  ", "one  two\nthree", 3)).toBe(
			"one  two\nthree  ",
		);
	});

	it("rejects edits above the limit without rewriting the current text", () => {
		const currentValue = "one  two\nthree  ";
		expect(keepWithinWordLimit(`${currentValue}four`, currentValue, 3)).toBe(
			currentValue,
		);
	});

	it("rejects pasted text above the submission cap", () => {
		const currentValue = "An existing testimonial.";
		const pastedText = Array.from({ length: 351 }, (_, index) => `word-${index}`).join(" ");
		expect(keepWithinWordLimit(pastedText, currentValue)).toBe(currentValue);
	});
});

describe("testimonial profile links", () => {
	it("accepts LinkedIn profile URLs", () => {
		expect(isLinkedInProfileUrl("https://www.linkedin.com/in/example-person/")).toBe(true);
	});

	it("rejects non-profile LinkedIn URLs", () => {
		expect(isLinkedInProfileUrl("https://www.linkedin.com/company/example")).toBe(false);
	});

	it("accepts X and legacy Twitter status URLs", () => {
		expect(isTweetUrl("https://x.com/example/status/1234567890")).toBe(true);
		expect(isTweetUrl("https://twitter.com/example/status/1234567890")).toBe(true);
	});

	it("rejects social profile URLs without a status", () => {
		expect(isTweetUrl("https://x.com/example")).toBe(false);
	});
});
