import { describe, expect, it } from "vitest";
import { availablePromptHeight } from "./spawnPromptLayout";

describe("availablePromptHeight", () => {
	it("ends the prompt where keyboard-lifted controls begin", () => {
		expect(availablePromptHeight(620, 340)).toBe(280);
	});

	it("does not extend behind the controls in a short sheet", () => {
		expect(availablePromptHeight(250, 340)).toBe(0);
	});

	it("fills the editor area with the keyboard closed", () => {
		expect(availablePromptHeight(620, 0)).toBe(620);
	});
});
