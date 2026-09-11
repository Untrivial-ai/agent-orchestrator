import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { NewTaskDialog } from "./NewTaskDialog";

const { getMock, postMock, ensureAgentReadinessMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	ensureAgentReadinessMock: vi.fn(),
}));

vi.mock("../hooks/useAgentReadinessQuery", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../hooks/useAgentReadinessQuery")>();
	return { ...actual, useEnsureAgentReadiness: ensureAgentReadinessMock };
});

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (typeof error === "object" && error !== null && "message" in error) {
			const body = error as { code?: unknown; message: unknown };
			const message = String(body.message);
			return typeof body.code === "string" && body.code !== "" ? `${message} (${body.code})` : message;
		}
		return fallback;
	},
	apiErrorCode: (error: unknown) =>
		typeof error === "object" && error !== null && "code" in error
			? String((error as { code: unknown }).code)
			: undefined,
}));

function renderDialog() {
	const onCreated = vi.fn();
	const onOpenChange = vi.fn();
	render(
		<QueryClientProvider client={new QueryClient()}>
			<NewTaskDialog open projectId="proj-1" onCreated={onCreated} onOpenChange={onOpenChange} />
		</QueryClientProvider>,
	);
	return { onCreated, onOpenChange };
}

function requestBody() {
	const call = postMock.mock.calls.find(([path]) => path === "/api/v1/orchestrators/delegate");
	if (!call) throw new Error("delegate was never called");
	return (call[1] as { body: Record<string, unknown> }).body;
}

function delegateCalls() {
	return postMock.mock.calls.filter(([path]) => path === "/api/v1/orchestrators/delegate");
}

const agentInventory = {
	agents: [
		agentReadiness("claude-code", "Claude Code"),
		agentReadiness("cursor", "Cursor"),
		agentReadiness("kiro", "Kiro", { authentication: "unknown" }),
	],
};

const directModelCatalog = {
	agentId: "claude-code",
	models: [],
	selectionMode: "catalog",
	allowCustom: true,
	customModelEntry: "direct",
	source: "command",
	fetchedAt: "2026-08-31T00:00:00Z",
	refreshRecommended: false,
	stale: false,
};

async function waitForAgentCatalog() {
	await waitFor(() => expect(screen.getAllByText("Claude Code").length).toBeGreaterThan(0));
}

beforeEach(() => {
	ensureAgentReadinessMock.mockReset();
	getMock.mockReset().mockImplementation(async (path: string) => {
		if (path === "/api/v1/agents/readiness") {
			return { data: agentInventory, error: undefined };
		}
		if (path === "/api/v1/agents/{agent}/models") {
			return { data: directModelCatalog, error: undefined };
		}
		return {
			data: { status: "ok", project: { id: "proj-1", config: { worker: { agent: "claude-code" } } } },
			error: undefined,
		};
	});
	postMock.mockReset().mockImplementation(async (path: string) => {
		if (path === "/api/v1/agents/readiness/ensure") return { data: agentInventory, error: undefined };
		return { data: { ok: true, workerId: "worker-1", orchestratorId: "orch-1" }, error: undefined };
	});
});

afterEach(() => vi.restoreAllMocks());

describe("NewTaskDialog", () => {
	it("renders one continuous composer surface with a visible settings-style title", async () => {
		renderDialog();
		await waitForAgentCatalog();

		const dialog = screen.getByRole("dialog", { name: "Create a new task" });
		expect(dialog.querySelector(".composer-prompt-surface")).not.toBeNull();
		expect(screen.getByText("Create a new task")).toHaveClass("settings-dialog-title");
		expect(screen.queryByText("Runs with")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Close new task dialog" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Agent" })).toHaveTextContent("Claude Code");
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("Use Claude Code's default");
		expect(screen.getByRole("button", { name: "Add file" })).toBeInTheDocument();
		expect(screen.getByLabelText("Task").getAttribute("placeholder")).toBeTruthy();
		expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Branch")).not.toBeInTheDocument();
	});

	it("dismisses the chrome-free card with Escape", async () => {
		const { onOpenChange } = renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		await user.keyboard("{Escape}");
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	it("starts the original task naming the preselected project-default agent and optional model", async () => {
		const { onCreated, onOpenChange } = renderDialog();
		const user = userEvent.setup();
		const brief = "  Restore the fallback renderer after WebGL init fails.  ";

		await waitForAgentCatalog();

		await user.type(screen.getByLabelText("Task"), brief);
		await user.click(await screen.findByRole("button", { name: "Model" }));
		await user.type(screen.getByRole("searchbox", { name: "Search model" }), "placeholder-model");
		await user.click(screen.getByRole("menuitem", { name: "Use “placeholder-model” as a custom model" }));
		await user.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(requestBody).not.toThrow());
		expect(postMock).toHaveBeenCalledWith("/api/v1/orchestrators/delegate", {
			body: {
				projectId: "proj-1",
				brief,
				// The dialog preselects the project's worker agent, so the delegate
				// call names it instead of relying on a server-side fallback.
				agent: "claude-code",
				model: "placeholder-model",
			},
		});
		expect(requestBody()).not.toHaveProperty("issueId");
		expect(requestBody()).not.toHaveProperty("branch");
		expect(requestBody()).not.toHaveProperty("harness");
		expect(onCreated).toHaveBeenCalledWith("worker-1");
		expect(onOpenChange).toHaveBeenCalledWith(false);
	}, 20_000);

	it("offers an explicit Terminal UI retry when Chat preflight fails", async () => {
		let delegateAttempts = 0;
		postMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents/readiness/ensure") return { data: agentInventory, error: undefined };
			delegateAttempts += 1;
			if (delegateAttempts === 1) {
				return {
					data: undefined,
					error: { code: "CHAT_AUTH_REQUIRED", message: "Claude Code needs login" },
				};
			}
			return { data: { ok: true, workerId: "worker-tui" }, error: undefined };
		});
		const { onCreated } = renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		await user.type(screen.getByLabelText("Task"), "Fix it");
		await user.click(screen.getByRole("button", { name: "Start task" }));

		const fallback = await screen.findByRole("button", { name: "Create as Terminal UI" });
		expect(requestBody()).not.toHaveProperty("mode");
		await user.click(fallback);

		await waitFor(() => expect(delegateCalls()).toHaveLength(2));
		const retryBody = (delegateCalls()[1][1] as { body: Record<string, unknown> }).body;
		expect(retryBody.mode).toBe("tui");
		expect(onCreated).toHaveBeenCalledWith("worker-tui");
	});

	it("sends the chosen agent when the user overrides the default", async () => {
		renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		await user.type(screen.getByLabelText("Task"), "B");

		await user.click(screen.getByRole("button", { name: "Agent" }));
		await user.click(await screen.findByRole("menuitem", { name: "Cursor" }));

		await user.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(requestBody).not.toThrow());
		expect(requestBody().agent).toBe("cursor");
	});

	it("allows selecting an installed agent with unknown auth", async () => {
		renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		await user.click(screen.getByRole("button", { name: "Agent" }));
		const options = await screen.findAllByRole("menuitem");
		expect(options.map((option) => option.textContent)).toEqual(["Claude Code", "Cursor", "KiroAuth unknown"]);
		expect(options[2]).not.toHaveAttribute("aria-disabled", "true");
		await user.click(options[2]);

		await user.type(screen.getByLabelText("Task"), "B");
		await user.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(requestBody).not.toThrow());
		expect(requestBody().agent).toBe("kiro");
	});

	it("starts an untitled task without an initial prompt", async () => {
		const { onCreated, onOpenChange } = renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		await user.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(requestBody).not.toThrow());
		expect(requestBody()).toMatchObject({
			projectId: "proj-1",
			brief: "",
			agent: "claude-code",
		});
		expect(onCreated).toHaveBeenCalledWith("worker-1");
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	it("shows an empty Model field for scratch projects and omits it from delegation", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents/readiness") {
				return {
					data: {
						agents: [agentReadiness("claude-code", "Claude Code")],
					},
					error: undefined,
				};
			}
			if (path === "/api/v1/agents/{agent}/models") {
				return { data: directModelCatalog, error: undefined };
			}
			return {
				data: {
					status: "ok",
					project: { id: "proj-1", kind: "scratch", config: { worker: { agent: "claude-code" } } },
				},
				error: undefined,
			};
		});

		renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		expect(screen.queryByLabelText("Branch")).not.toBeInTheDocument();
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("Use Claude Code's default");

		await user.type(screen.getByLabelText("Task"), "Build a quick prototype in scratch.");
		await user.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(requestBody).not.toThrow());
		expect(requestBody()).not.toHaveProperty("branch");
		expect(requestBody().model).toBeUndefined();
	});

	it("submits on Enter and inserts a newline on Shift+Enter in the task", async () => {
		renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		const task = screen.getByLabelText("Task");
		await user.type(task, "First line");
		// Shift+Enter must NOT submit — it adds a newline.
		await user.keyboard("{Shift>}{Enter}{/Shift}");
		await user.type(task, "Second line");
		expect(postMock).not.toHaveBeenCalledWith("/api/v1/orchestrators/delegate", expect.anything());

		// Plain Enter submits the task.
		await user.keyboard("{Enter}");
		await waitFor(() => expect(requestBody).not.toThrow());
		expect(requestBody().brief).toContain("\n");
	});

	it("does not submit on Alt+Enter or Shift+Enter but does on plain Enter in the task", async () => {
		renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		const task = screen.getByLabelText("Task");
		await user.type(task, "Line");

		// Alt+Enter must NOT submit — Alt is excluded so it can't submit by accident.
		await user.keyboard("{Alt>}{Enter}{/Alt}");
		expect(postMock).not.toHaveBeenCalledWith("/api/v1/orchestrators/delegate", expect.anything());

		// Shift+Enter must NOT submit — it inserts a newline.
		await user.keyboard("{Shift>}{Enter}{/Shift}");
		expect(postMock).not.toHaveBeenCalledWith("/api/v1/orchestrators/delegate", expect.anything());

		// Plain Enter submits the task.
		await user.keyboard("{Enter}");
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
	});

	it.each([
		{
			code: "UNKNOWN_HARNESS",
			message: "Unknown requested agent",
		},
		{
			code: "INTERNAL",
			message: "task start failed",
		},
	])("displays daemon start errors for $code", async ({ code, message }) => {
		postMock.mockResolvedValueOnce({
			data: undefined,
			error: { code, message },
		});
		renderDialog();
		const user = userEvent.setup();
		await waitForAgentCatalog();

		await user.type(screen.getByLabelText("Task"), "Restore fallback renderer.");
		await user.click(screen.getByRole("button", { name: "Start task" }));

		expect(await screen.findByText(`${message} (${code})`)).toBeInTheDocument();
	});
});
