import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { AgentSelectorRecoveryAction } from "./AgentSelectorRecoveryAction";

afterEach(() => {
	act(() => useUiStore.getState().closeSettings());
});

describe("AgentSelectorRecoveryAction", () => {
	it.each([
		["authorized", false],
		["not_applicable", false],
		["unauthorized", true],
	] as const)("omits an action for %s readiness when loading is %s", (authentication, isLoading) => {
		const { container } = render(
			<AgentSelectorRecoveryAction
				agentId="codex"
				agents={[agentReadiness("codex", "Codex", { authentication })]}
				isLoading={isLoading}
				variant="compact"
			/>,
		);

		expect(container).toBeEmptyDOMElement();
	});

	it("opens the selected agent in Harness settings without discarding sibling form state", async () => {
		function OriginatingForm() {
			const [brief, setBrief] = useState("");
			return (
				<>
					<input aria-label="Task" value={brief} onChange={(event) => setBrief(event.target.value)} />
					<AgentSelectorRecoveryAction
						agentId="codex"
						agents={[agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
						isLoading={false}
						variant="explanatory"
					/>
				</>
			);
		}

		render(<OriginatingForm />);
		await userEvent.type(screen.getByRole("textbox", { name: "Task" }), "Keep this draft");
		await userEvent.click(screen.getByRole("button", { name: "Log in" }));

		expect(useUiStore.getState().settingsModal).toEqual({
			scope: "global",
			section: "harness",
			focusAgentId: "codex",
		});

		act(() => useUiStore.getState().closeSettings());
		expect(screen.getByRole("textbox", { name: "Task" })).toHaveValue("Keep this draft");
	});
});
