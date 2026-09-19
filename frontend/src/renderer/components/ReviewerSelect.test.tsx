import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { useUiStore } from "../stores/ui-store";
import { ReviewerSelect } from "./ReviewerSelect";

describe("ReviewerSelect", () => {
	it("hides unavailable reviewers and opens Harness from the menu while preserving its value", async () => {
		const onChange = vi.fn();
		useUiStore.setState({ settingsModal: null });
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="codex" onChange={onChange}
			agents={[agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
		/></QueryClientProvider>);
		const trigger = screen.getByRole("button", { name: "Reviewer" });
		expect(trigger).toHaveTextContent("Needs setup");
		await userEvent.click(trigger);
		expect(screen.getByRole("menuitem", { name: /Claude Code/ })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: /Codex/ })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("menuitem", { name: "Manage agents…" }));
		await waitFor(() => expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "harness", focusAgentId: "codex" }));
		expect(onChange).not.toHaveBeenCalled();
	});
	it("does not offer an unauthorized default reviewer as a selectable default", async () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="" defaultHarness="codex" defaultOptionLabel="Project default" onChange={() => undefined}
			agents={[agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
		/></QueryClientProvider>);
		await userEvent.click(screen.getByRole("button", { name: "Reviewer" }));
		expect(screen.queryByRole("menuitem", { name: /Project default/ })).not.toBeInTheDocument();
		expect(screen.getByText("No agents ready")).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Manage agents…" })).toBeInTheDocument();
	});

});
