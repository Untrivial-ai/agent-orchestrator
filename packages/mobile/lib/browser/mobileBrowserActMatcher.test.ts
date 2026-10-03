import { describe, expect, it } from "vitest";
import { matchMobileBrowserInstruction } from "./mobileBrowserActMatcher";

describe("mobile browser act matcher", () => {
	it("matches an exact accessible name and role", () => {
		expect(matchMobileBrowserInstruction("the submit button", {
			e1: { role: "link", name: "Help" },
			e2: { role: "button", name: "Submit" },
		})).toEqual({ outcome: "matched", candidate: { role: "button", name: "Submit", ref: "e2", score: 13 } });
	});

	it("declines an ambiguous mutation unless nth disambiguates it", () => {
		const refs = {
			e1: { role: "button", name: "Add to Cart" },
			e2: { role: "button", name: "Add to Cart" },
		};
		expect(matchMobileBrowserInstruction("add to cart", refs).outcome).toBe("ambiguous");
		expect(matchMobileBrowserInstruction("add to cart", refs, 1)).toMatchObject({ outcome: "matched", candidate: { ref: "e2" } });
	});

	it("does not guess when no candidate is credible", () => {
		expect(matchMobileBrowserInstruction("cancel button", {
			e1: { role: "button", name: "Confirm deletion" },
		})).toEqual({ outcome: "no-match" });
	});
});
