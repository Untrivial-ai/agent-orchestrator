import { describe, expect, it } from "vitest";
import { elicitationPromptCopy, elicitationStepPresentation, groupedQuestionInputs, inputOptions, missingRequiredInputs, safeHttpURL, toggleInputValue, validateInput } from "./elicitationModel";

describe("mobile Chat elicitation model", () => {
	it("opens only explicit web URLs", () => {
		expect(safeHttpURL("https://example.com/login")?.hostname).toBe("example.com");
		expect(safeHttpURL("http://example.com")).toBeDefined();
		expect(safeHttpURL("javascript:alert(1)")).toBeUndefined();
		expect(safeHttpURL("file:///etc/passwd")).toBeUndefined();
		expect(safeHttpURL("not a URL")).toBeUndefined();
	});

	it("validates required, string, number and integer constraints", () => {
		expect(missingRequiredInputs(["name", "scopes"], { name: "", scopes: [] })).toEqual(["name", "scopes"]);
		expect(validateInput({ type: "string", minLength: 3 }, "ab")).toContain("at least 3");
		expect(validateInput({ type: "integer" }, 1.5)).toContain("whole number");
		expect(validateInput({ type: "number", minimum: 2, maximum: 4 }, 1)).toContain("at least 2");
		expect(validateInput({ type: "number", minimum: 2, maximum: 4 }, 5)).toContain("at most 4");
	});

	it("normalizes provider choices and multi-select toggles", () => {
		expect(inputOptions({ type: "string", oneOf: [{ const: "fast", title: "Fast", description: "Less context" }] })).toEqual([
			{ value: "fast", label: "Fast", description: "Less context" },
		]);
		expect(toggleInputValue(["read"], "write")).toEqual(["read", "write"]);
		expect(toggleInputValue(["read", "write"], "read")).toEqual(["write"]);
	});

	it("groups numbered agent questions with their custom-answer fields", () => {
		const properties = [
			["question_1", { type: "string" }],
			["question_1_custom", { type: "string" }],
			["question_2", { type: "string" }],
		] as const;
		expect(groupedQuestionInputs(properties)).toEqual([
			[["question_1", { type: "string" }], ["question_1_custom", { type: "string" }]],
			[["question_2", { type: "string" }]],
		]);
	});

	it("leaves ordinary schemas on one form instead of inventing question pages", () => {
		expect(groupedQuestionInputs([["branch", { type: "string" }]])).toBeUndefined();
		expect(groupedQuestionInputs([])).toBeUndefined();
	});

	it("presents paged questions as one quiet status line with native navigation", () => {
		expect(elicitationStepPresentation(0, 3)).toEqual({
			status: "Needs input · Question 1 of 3",
			showBack: false,
			primaryLabel: "Next",
		});
		expect(elicitationStepPresentation(2, 3)).toEqual({
			status: "Needs input · Question 3 of 3",
			showBack: true,
			primaryLabel: "Continue",
		});
	});

	it("drops generic form instructions so the actual question leads", () => {
		expect(elicitationPromptCopy("Please answer the following questions.")).toBeUndefined();
		expect(elicitationPromptCopy("Choose the rollout strategy for production.")).toBe("Choose the rollout strategy for production.");
	});
});
