import { describe, expect, it } from "vitest";
import { matchInstruction, parseSnapshotEntries } from "./browser-act-matcher";

describe("parseSnapshotEntries", () => {
	it("parses quoted names with a leading tree-indent dash", () => {
		expect(parseSnapshotEntries('- button "Open" [ref=e1]')).toEqual([{ role: "button", name: "Open", ref: "e1" }]);
	});

	it("parses bare (unquoted) names, matching a second observed fixture format", () => {
		expect(parseSnapshotEntries("button Save [ref=e1]")).toEqual([{ role: "button", name: "Save", ref: "e1" }]);
	});

	it("parses nested/indented entries and an empty quoted name", () => {
		const text = ['- navigation "Main"', '  - link "Home" [ref=e1]', '  - link "" [ref=e2]'].join("\n");
		expect(parseSnapshotEntries(text)).toEqual([
			{ role: "link", name: "Home", ref: "e1" },
			{ role: "link", name: "", ref: "e2" },
		]);
	});

	it("excludes lines with no [ref=eN] — nothing actionable to return", () => {
		const text = ['- heading "Welcome"', '- button "Submit" [ref=e3]'].join("\n");
		expect(parseSnapshotEntries(text)).toEqual([{ role: "button", name: "Submit", ref: "e3" }]);
	});

	it("handles multiple roles across a realistic multi-line snapshot", () => {
		const text = [
			'- heading "Checkout"',
			'- textbox "Email" [ref=e1]',
			'- button "Add to Cart" [ref=e2]',
			'- button "Submit" [ref=e3]',
		].join("\n");
		expect(parseSnapshotEntries(text)).toEqual([
			{ role: "textbox", name: "Email", ref: "e1" },
			{ role: "button", name: "Add to Cart", ref: "e2" },
			{ role: "button", name: "Submit", ref: "e3" },
		]);
	});
});

describe("matchInstruction", () => {
	it("resolves a single exact name match", () => {
		const result = matchInstruction("the submit button", '- button "Submit" [ref=e3]');
		expect(result).toEqual({ outcome: "matched", candidate: { role: "button", name: "Submit", ref: "e3", score: 13 } });
	});

	it("resolves a fuzzy match without an exact string match on the whole instruction", () => {
		const result = matchInstruction(
			"submit",
			['- button "Submit" [ref=e3]', '- button "Cancel" [ref=e4]'].join("\n"),
		);
		expect(result).toEqual({ outcome: "matched", candidate: { role: "button", name: "Submit", ref: "e3", score: 10 } });
	});

	it("reports ambiguous when two candidates score identically (e.g. two identical buttons)", () => {
		const text = ['- button "Add to Cart" [ref=e1]', '- button "Add to Cart" [ref=e2]'].join("\n");
		const result = matchInstruction("add to cart", text);
		expect(result.outcome).toBe("ambiguous");
		if (result.outcome === "ambiguous") {
			expect(result.candidates.map((c) => c.ref)).toEqual(["e1", "e2"]);
		}
	});

	it("reports ambiguous for a role-only instruction against many same-role elements", () => {
		const text = ['- button "Add to Cart" [ref=e1]', '- button "Buy Now" [ref=e2]', '- button "Wishlist" [ref=e3]'].join("\n");
		const result = matchInstruction("the button", text);
		expect(result.outcome).toBe("ambiguous");
	});

	it("reports no-match when nothing scores at all", () => {
		const result = matchInstruction("the submit button", '- textbox "Email" [ref=e1]');
		expect(result).toEqual({ outcome: "no-match" });
	});

	it("breaks a tie deterministically with --nth instead of declining", () => {
		const text = ['- button "Add to Cart" [ref=e1]', '- button "Add to Cart" [ref=e2]'].join("\n");
		const result = matchInstruction("add to cart", text, { nth: 1 });
		expect(result).toEqual({ outcome: "matched", candidate: { role: "button", name: "Add to Cart", ref: "e2", score: 10 } });
	});

	it("never lets a lower-tier fuzzy candidate outrank an exact-name match elsewhere in the snapshot", () => {
		const text = ['- button "Submit" [ref=e1]', '- button "Submit and continue" [ref=e2]'].join("\n");
		const result = matchInstruction("submit", text);
		expect(result).toEqual({ outcome: "matched", candidate: { role: "button", name: "Submit", ref: "e1", score: 10 } });
	});
});
