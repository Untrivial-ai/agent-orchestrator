import { describe, expect, it } from "vitest";
import { matchInstruction, parseSnapshotEntries } from "./browser-act-matcher";

const refs = {
	e1: { role: "heading", name: "Hi" },
	e2: { role: "button", name: "Sign In" },
	e3: { role: "button", name: "Nope" },
	e5: { role: "checkbox", name: "" },
	e10: { role: "switch", name: "Toggle" },
	e11: { role: "option", name: "A" },
};

describe("parseSnapshotEntries", () => {
	it("parses the structured refs map emitted by agent-browser", () => {
		expect(parseSnapshotEntries(refs)).toEqual([
			{ role: "heading", name: "Hi", ref: "e1" },
			{ role: "button", name: "Sign In", ref: "e2" },
			{ role: "button", name: "Nope", ref: "e3" },
			{ role: "checkbox", name: "", ref: "e5" },
			{ role: "switch", name: "Toggle", ref: "e10" },
			{ role: "option", name: "A", ref: "e11" },
		]);
	});

	it("ignores malformed refs instead of inventing candidates", () => {
		expect(
			parseSnapshotEntries({
				e1: { role: "button", name: "Save" },
				bad: { role: "button", name: "Delete" },
				e2: { role: 12, name: "Cancel" },
				e3: { role: "button" },
			}),
		).toEqual([{ role: "button", name: "Save", ref: "e1" }]);
	});

	it("restores document order when the native refs map uses lexicographic keys", () => {
		expect(
			parseSnapshotEntries({
				e1: { role: "button", name: "First" },
				e10: { role: "button", name: "Tenth" },
				e11: { role: "button", name: "Eleventh" },
				e2: { role: "button", name: "Second" },
				e3: { role: "button", name: "Third" },
			}),
		).toEqual([
			{ role: "button", name: "First", ref: "e1" },
			{ role: "button", name: "Second", ref: "e2" },
			{ role: "button", name: "Third", ref: "e3" },
			{ role: "button", name: "Tenth", ref: "e10" },
			{ role: "button", name: "Eleventh", ref: "e11" },
		]);
	});
});

describe("matchInstruction", () => {
	it("resolves a single exact name match", () => {
		expect(matchInstruction("the submit button", { e3: { role: "button", name: "Submit" } })).toEqual({
			outcome: "matched",
			candidate: { role: "button", name: "Submit", ref: "e3", score: 13 },
		});
	});

	it("resolves a fuzzy match without an exact string match on the whole instruction", () => {
		expect(
			matchInstruction("submit", {
				e3: { role: "button", name: "Submit" },
				e4: { role: "button", name: "Cancel" },
			}),
		).toEqual({
			outcome: "matched",
			candidate: { role: "button", name: "Submit", ref: "e3", score: 10 },
		});
	});

	it("reports ambiguous when two candidates score identically", () => {
		const result = matchInstruction("add to cart", {
			e1: { role: "button", name: "Add to Cart" },
			e2: { role: "button", name: "Add to Cart" },
		});
		expect(result.outcome).toBe("ambiguous");
		if (result.outcome === "ambiguous") {
			expect(result.candidates.map((candidate) => candidate.ref)).toEqual(["e1", "e2"]);
		}
	});

	it("does not match a lone candidate below the confidence floor", () => {
		expect(matchInstruction("the cancel button", { e1: { role: "button", name: "Confirm delete" } })).toEqual({
			outcome: "no-match",
		});
	});

	it.each(["", "C"])("does not inflate an unrelated short accessible name %j", (name) => {
		expect(matchInstruction("the cancel button", { e1: { role: "button", name } })).toEqual({
			outcome: "no-match",
		});
	});

	it("does not let --nth force a lone candidate below the confidence floor", () => {
		expect(
			matchInstruction("the cancel button", { e1: { role: "button", name: "Confirm delete" } }, { nth: 0 }),
		).toEqual({ outcome: "no-match" });
	});

	it("does not let an action word outrank the explicit role of an unnamed control", () => {
		expect(
			matchInstruction("check the checkbox", {
				e1: { role: "checkbox", name: "" },
				e2: { role: "button", name: "Check" },
			}),
		).toEqual({ outcome: "no-match" });
	});

	it("lets explicit --nth select an unnamed control from an action-prefixed role-only instruction", () => {
		expect(
			matchInstruction(
				"check the checkbox",
				{
					e1: { role: "checkbox", name: "" },
					e2: { role: "button", name: "Check" },
				},
				{ nth: 0 },
			),
		).toEqual({
			outcome: "matched",
			candidate: { role: "checkbox", name: "", ref: "e1", score: 3 },
		});
	});

	it("uses only a trailing role noun as the role hint", () => {
		expect(
			matchInstruction(
				"the Save Image button",
				{
					e1: { role: "image", name: "Save" },
					e2: { role: "button", name: "Save Image" },
				},
			),
		).toEqual({
			outcome: "matched",
			candidate: { role: "button", name: "Save Image", ref: "e2", score: 13 },
		});
	});

	it("keeps a leading action word when it is part of the element name", () => {
		expect(
			matchInstruction(
				"select all",
				{
					e1: { role: "button", name: "Select all" },
					e2: { role: "button", name: "Clear all" },
				},
			),
		).toEqual({
			outcome: "matched",
			candidate: { role: "button", name: "Select all", ref: "e1", score: 10 },
		});
	});

	it("reports ambiguous for a role-only instruction against many elements", () => {
		const result = matchInstruction("the button", {
			e1: { role: "button", name: "Add to Cart" },
			e2: { role: "button", name: "Buy Now" },
			e3: { role: "button", name: "Wishlist" },
		});
		expect(result.outcome).toBe("ambiguous");
	});

	it("reports no-match when nothing scores at all", () => {
		expect(matchInstruction("the submit button", { e1: { role: "textbox", name: "Email" } })).toEqual({
			outcome: "no-match",
		});
	});

	it("breaks a tie deterministically with --nth instead of declining", () => {
		expect(
			matchInstruction(
				"add to cart",
				{
					e1: { role: "button", name: "Add to Cart" },
					e2: { role: "button", name: "Add to Cart" },
				},
				{ nth: 1 },
			),
		).toEqual({
			outcome: "matched",
			candidate: { role: "button", name: "Add to Cart", ref: "e2", score: 10 },
		});
	});

	it("never lets a lower-tier fuzzy candidate outrank an exact-name match", () => {
		expect(
			matchInstruction("submit", {
				e1: { role: "button", name: "Submit" },
				e2: { role: "button", name: "Submit and continue" },
			}),
		).toEqual({
			outcome: "matched",
			candidate: { role: "button", name: "Submit", ref: "e1", score: 10 },
		});
	});
});
