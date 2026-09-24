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
			ariaLabel="Reviewer" value="codex" defaultHarness="claude-code" onChange={onChange}
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
	it("shows the resolved reviewer while keeping the inherited selection", async () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="" defaultHarness="codex" onChange={() => undefined}
			agents={[agentReadiness("codex", "Codex", { authentication: "unauthorized" })]}
		/></QueryClientProvider>);
		const trigger = screen.getByRole("button", { name: "Reviewer" });
		expect(trigger).toHaveTextContent("Codex");
		expect(trigger).not.toHaveTextContent("default");
		await userEvent.click(trigger);
		expect(screen.getByRole("menuitem", { name: /Codex/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Manage agents…" })).toBeInTheDocument();
	});

	it("keeps fallback reviewers usable until a readiness snapshot arrives", async () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="codex" defaultHarness="claude-code" onChange={() => undefined}
		/></QueryClientProvider>);

		const trigger = screen.getByRole("button", { name: "Reviewer" });
		expect(trigger).not.toHaveTextContent("Needs setup");
		await userEvent.click(trigger);
		expect(screen.getByRole("menuitem", { name: /Claude Code/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: /Codex/ })).toBeInTheDocument();
	});

	it("shows the catalog's concrete model for an inherited reviewer", () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		client.setQueryData(["agent-models", "codex", ""], {
			agentId: "codex", selectionMode: "catalog", models: [
				{ id: "gpt-test", label: "GPT Test", isDefault: true },
			],
		});
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="" defaultHarness="codex" onChange={() => undefined}
		/></QueryClientProvider>);
		expect(screen.getByRole("button", { name: "Reviewer" })).toHaveTextContent("Codex · GPT Test");
	});

	it("requires a concrete reviewer model when the catalog reports no default", async () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		client.setQueryData(["agent-models", "codex", ""], {
			agentId: "codex", selectionMode: "catalog", models: [{ id: "gpt-test", label: "GPT Test" }],
		});
		const onConfigChange = vi.fn();
		render(<QueryClientProvider client={client}><ReviewerSelect
			ariaLabel="Reviewer" value="codex" defaultHarness="claude-code" onChange={() => undefined} onConfigChange={onConfigChange}
		/></QueryClientProvider>);

		const trigger = screen.getByRole("button", { name: "Reviewer" });
		expect(trigger).toHaveTextContent("Model not reported");
		await userEvent.click(trigger);
		await userEvent.click(screen.getByRole("menuitem", { name: /Codex/ }));
		expect(screen.queryByRole("menuitem", { name: "Agent choice" })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("menuitem", { name: "GPT Test" }));
		expect(onConfigChange).toHaveBeenCalledWith("codex", { model: "gpt-test" });
	});

});
