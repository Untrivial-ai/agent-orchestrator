import { describe, expect, it } from "vitest";
import { reviewerChoices, reviewerSwitchSelection, reviewerSwitchWarning } from "./reviewerControls";

describe("mobile reviewer controls", () => {
	it("allows authorized and auth-unknown reviewers but disables unavailable agents", () => {
		const choices = reviewerChoices({
			supported: [{ id: "codex", label: "Codex" }, { id: "claude-code", label: "Claude" }, { id: "aider", label: "Aider" }],
			installed: [{ id: "codex", label: "Codex", authStatus: "authorized" }, { id: "claude-code", label: "Claude", authStatus: "unknown" }],
			authorized: [{ id: "codex", label: "Codex" }],
		});
		expect(choices.map(({ id, selectable, status }) => ({ id, selectable, status }))).toEqual([
			{ id: "codex", selectable: true, status: "" },
			{ id: "claude-code", selectable: true, status: "Auth unknown" },
			{ id: "aider", selectable: false, status: "Needs install" },
		]);
	});

	it("clears the override to inherit the project default", () => {
		expect(reviewerSwitchSelection("", {})).toEqual({});
	});

	it("keeps model and mode on a reviewer switch", () => {
		expect(reviewerSwitchSelection("codex", { model: "gpt-5", mode: "plan", effort: "" })).toEqual({
			harness: "codex",
			agentConfig: { model: "gpt-5", mode: "plan" },
		});
	});

	it("only warns when switching an active review", () => {
		expect(reviewerSwitchWarning(false)).toBeUndefined();
		expect(reviewerSwitchWarning(true)).toContain("cancels its running review");
	});
});
