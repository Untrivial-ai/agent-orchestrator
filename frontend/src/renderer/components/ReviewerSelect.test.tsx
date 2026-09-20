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
	it("keeps the project-default reset available when its resolved reviewer needs setup", async () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="" defaultHarness="codex" defaultOptionLabel="Project default" onChange={() => undefined}
			agents={[agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
		/></QueryClientProvider>);
		await userEvent.click(screen.getByRole("button", { name: "Reviewer" }));
		expect(screen.getByRole("menuitem", { name: /Project default/ })).toBeInTheDocument();
		expect(screen.queryByText("No agents ready")).not.toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Manage agents…" })).toBeInTheDocument();
	});

	it("keeps fallback reviewers usable until a readiness snapshot arrives", async () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="codex" onChange={() => undefined}
		/></QueryClientProvider>);

		const trigger = screen.getByRole("button", { name: "Reviewer" });
		expect(trigger).not.toHaveTextContent("Needs setup");
		await userEvent.click(trigger);
		expect(screen.getByRole("menuitem", { name: /Claude Code/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: /Codex/ })).toBeInTheDocument();
	});

});
