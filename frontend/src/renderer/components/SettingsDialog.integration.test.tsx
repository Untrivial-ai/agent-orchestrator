import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { agentReadinessQueryKey } from "../hooks/useAgentReadinessQuery";
import { agentAuthPlansQueryKey } from "../hooks/useAgentAuth";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient } from "../lib/api-client";
import { useUiStore } from "../stores/ui-store";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { CreateProjectAgentSheet } from "./CreateProjectAgentSheet";
import { NewTaskDialog } from "./NewTaskDialog";
import { SettingsDialog } from "./SettingsDialog";
import { TooltipProvider } from "./ui/tooltip";

// Routing and daemon requests are external boundaries; every modal, settings
// form, selector, and recovery action in these tests is the real component.
vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...await importOriginal<typeof import("@tanstack/react-router")>(),
	useNavigate: () => vi.fn(),
}));

const project = {
	id: "proj-1", name: "Project One", kind: "single_repo", path: "/repo/project-one", repo: "",
	config: { worker: { agent: "codex" }, orchestrator: { agent: "claude-code" } },
};
let catalog = { agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")] };

function RecoveryDialogs({ origin }: { origin?: "create-project" | "new-task" }) {
	const [open, setOpen] = useState(true);
	return (
		<>
			{origin === "create-project" && <CreateProjectAgentSheet open={open} onOpenChange={setOpen} isCreating={false} kind="single_repo" path="/repo/new-project" onSubmit={async () => undefined} />}
			{origin === "new-task" && <NewTaskDialog open={open} onOpenChange={setOpen} projectId="proj-1" onCreated={() => undefined} />}
			<SettingsDialog />
		</>
	);
}

function renderDialogs(origin?: "create-project" | "new-task") {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
	client.setQueryData(agentReadinessQueryKey, catalog);
	client.setQueryData(workspaceQueryKey, []);
	render(<QueryClientProvider client={client}><TooltipProvider><RecoveryDialogs origin={origin} /></TooltipProvider></QueryClientProvider>);
	return client;
}

async function openAgentManagement(label: string) {
	await userEvent.click(await screen.findByLabelText(label));
	const action = screen.queryByRole("menuitem", { name: "Manage agents…" }) ?? screen.getByRole("option", { name: "Manage agents…" });
	await userEvent.click(action);
	await screen.findByRole("textbox", { name: "Search harnesses" });
}

function requireCodexLogin(client?: QueryClient) {
	catalog = { agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex", { authentication: "unauthorized" })] };
	if (client) act(() => client.setQueryData(agentReadinessQueryKey, catalog));
}

beforeEach(() => {
	useUiStore.setState({ settingsModal: null });
	catalog = { agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")] };
	vi.spyOn(apiClient, "GET").mockImplementation(async (path) => {
		if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project } } as never;
		if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
		if (path === "/api/v1/agents/installers") return { data: { agents: [] } } as never;
		if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
		if (path === "/api/v1/agents/auth-plans") return { data: { plans: [{ agentId: "codex", action: "login", available: true, launchMode: "terminal" }] } } as never;
		if (path === "/api/v1/agents/{agent}/models") return { data: { agentId: "codex", models: [], selectionMode: "text", allowCustom: true, source: "manual", fetchedAt: "2026-09-19T00:00:00Z", stale: false } } as never;
		if (path === "/api/v1/settings") return { data: { defaultSessionMode: "tui", chatHarnesses: [], cloudEnabled: false, localEnabled: true } } as never;
		throw new Error(`Unexpected GET ${path}`);
	});
	vi.spyOn(apiClient, "POST").mockImplementation(async (path) => {
		if (path === "/api/v1/agents/readiness/ensure") return { data: catalog } as never;
		if (path === "/api/v1/agents/codex/accounts/ensure") return { data: { accountRevision: 0, accounts: [], capabilities: {}, deviceReconciliation: { status: "verified", activeAccountVerified: false, reasonCode: "verified", retryable: false } } } as never;
		throw new Error(`Unexpected POST ${path}`);
	});
});

afterEach(() => vi.restoreAllMocks());

describe("Settings recovery modal integration", () => {
	it("leaves focus on the targeted Harness action when readiness is already cached", async () => {
		requireCodexLogin();
		const client = renderDialogs();
		act(() => {
			client.setQueryData(agentReadinessQueryKey, catalog);
			client.setQueryData(["agent-installers"], []);
			client.setQueryData(["agent-install-jobs"], []);
			client.setQueryData(agentAuthPlansQueryKey, [
				{ agentId: "codex", action: "login", available: true, launchMode: "terminal" },
			]);
			useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "codex" });
		});

		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		const login = await within(row).findByRole("button", { name: "Login" });
		await waitFor(() => expect(document.activeElement).toBe(login));
	});

	it("returns focus to the project selector after setup and refreshes its choices", async () => {
		requireCodexLogin();
		useUiStore.getState().openProjectSettings("proj-1");
		const client = renderDialogs();
		await userEvent.click(await screen.findByRole("button", { name: "Agents" }));
		const projectDialog = screen.getByRole("dialog");
		const trigger = await screen.findByLabelText("Default worker agent");
		await openAgentManagement("Default worker agent");
		await screen.findByRole("textbox", { name: "Search harnesses" });

		act(() => client.setQueryData(agentReadinessQueryKey, {
			agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")],
		}));
		await waitFor(() => expect(trigger).not.toHaveTextContent("Needs setup"));
		await userEvent.click(screen.getByRole("button", { name: "Close settings" }));

		await waitFor(() => {
			expect(document.activeElement?.isConnected).toBe(true);
			expect(projectDialog).toContainElement(document.activeElement as HTMLElement);
			expect(trigger).toHaveFocus();
		});
		await userEvent.click(trigger);
		expect(await screen.findByRole("menuitem", { name: /Codex/ })).toBeInTheDocument();
		await userEvent.keyboard("{Escape}");
		expect(screen.getByRole("button", { name: "Agents" })).toHaveAttribute("aria-current", "page");
		await userEvent.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	it("allows pointer interaction above create-project and restores its selections and intake draft on close", async () => {
		const client = renderDialogs("create-project");
		await userEvent.click(screen.getByRole("combobox", { name: "Worker agent" }));
		await userEvent.click(await screen.findByRole("option", { name: /Codex/i }));
		await userEvent.click(screen.getByLabelText("Automatically work on assigned issues"));
		await userEvent.type(screen.getByLabelText("Assignee"), "octocat");
		requireCodexLogin(client);
		await openAgentManagement("Worker agent");

		const search = await screen.findByRole("textbox", { name: "Search harnesses" });
		await userEvent.type(search, "Claude");
		expect(search).toHaveValue("Claude");
		await userEvent.click(screen.getByRole("button", { name: "Close settings" }));

		expect(await screen.findByRole("combobox", { name: "Worker agent" })).toHaveTextContent("Codex");
		expect(screen.getByLabelText("Assignee")).toHaveValue("octocat");
		expect(useUiStore.getState().settingsModal).toBeNull();
		await userEvent.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	it("Escape dismisses only recovery settings above a new-task dialog and preserves its typed task", async () => {
		requireCodexLogin();
		renderDialogs("new-task");
		await userEvent.type(screen.getByRole("textbox", { name: "Task" }), "Keep the task draft");
		await openAgentManagement("Agent");
		await screen.findByRole("textbox", { name: "Search harnesses" });
		await userEvent.keyboard("{Escape}");

		expect(await screen.findByRole("textbox", { name: "Task" })).toHaveValue("Keep the task draft");
		expect(useUiStore.getState().settingsModal).toBeNull();
		await userEvent.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	it.each(["close button", "Escape"])("keeps the real project form mounted beneath recovery and returns to its draft via %s", async (dismiss) => {
		requireCodexLogin();
		useUiStore.getState().openProjectSettings("proj-1");
		renderDialogs();
		await userEvent.click(await screen.findByRole("button", { name: "Edit Project name" }));
		const name = screen.getByRole("textbox", { name: "Project name" });
		await userEvent.clear(name);
		await userEvent.type(name, "Unsaved project name");
		await userEvent.click(screen.getByRole("button", { name: "Agents" }));
		const form = document.getElementById("project-settings-form");
		await openAgentManagement("Default worker agent");
		await screen.findByRole("textbox", { name: "Search harnesses" });

		expect(form).toBeInTheDocument();
		if (dismiss === "Escape") await userEvent.keyboard("{Escape}");
		else await userEvent.click(screen.getByRole("button", { name: "Close settings" }));

		expect(await screen.findByRole("button", { name: "Default worker agent" })).toHaveTextContent("Codex");
		expect(screen.getByRole("button", { name: "Agents" })).toHaveAttribute("aria-current", "page");
		expect(document.getElementById("project-settings-form")).toBe(form);
		await userEvent.click(screen.getByRole("button", { name: "Identity" }));
		expect(await screen.findByRole("button", { name: "Edit Project name" })).toHaveTextContent("Unsaved project name");
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
		await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close settings" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});
});
