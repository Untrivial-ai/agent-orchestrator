import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { ReviewerSelect } from "./ReviewerSelect";

describe("ReviewerSelect", () => {
	it("renders its recovery action beside the trigger and outside the dropdown", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<ReviewerSelect
					agents={[agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
					ariaLabel="Reviewer"
					onChange={() => undefined}
					recoveryAction={<button type="button">Log in</button>}
					value="codex"
				/>
			</QueryClientProvider>,
		);

		const trigger = screen.getByRole("button", { name: "Reviewer" });
		const action = screen.getByRole("button", { name: "Log in" });
		expect(trigger.parentElement).toBe(action.parentElement);

		await userEvent.click(trigger);
		expect(await screen.findByRole("menu")).not.toContainElement(action);
	});

	it("can place an explanatory recovery action below the trigger", () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<ReviewerSelect
					agents={[agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
					ariaLabel="Reviewer"
					onChange={() => undefined}
					recoveryAction={<button type="button">Log in to Codex</button>}
					recoveryActionPlacement="below"
					value="codex"
				/>
			</QueryClientProvider>,
		);

		const trigger = screen.getByRole("button", { name: "Reviewer" });
		const action = screen.getByRole("button", { name: "Log in to Codex" });
		expect(trigger.compareDocumentPosition(action) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
	});
});
