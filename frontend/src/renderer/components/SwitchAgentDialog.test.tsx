import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { agentModelsQueryKey } from "../hooks/useAgentModelsQuery";
import type { AgentSwitchSummary, WorkspaceSession } from "../types/workspace";
import { SwitchAgentDialog } from "./SwitchAgentDialog";
import { TooltipProvider } from "./ui/tooltip";

const switchMocks = vi.hoisted(() => ({
	clear: vi.fn(),
	mutate: vi.fn(),
	recoverMutate: vi.fn(),
	recoverState: {
		error: null as Error | null,
		isPending: false,
	},
	state: {
		error: null as string | null,
		isPending: false,
	},
}));

vi.mock("../hooks/useSwitchAgent", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../hooks/useSwitchAgent")>();
	return {
		...actual,
		clearSwitchAgentState: switchMocks.clear,
		createSwitchAgentIdempotencyKey: () => "idempotency-1",
		useSwitchAgent: () => ({ mutate: switchMocks.mutate }),
		useRecoverAgentSwitch: () => ({ ...switchMocks.recoverState, mutate: switchMocks.recoverMutate }),
		useSwitchAgentState: () => switchMocks.state,
	};
});

const worker: WorkspaceSession = {
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	branch: "ao/sess-1",
	id: "sess-1",
	kind: "worker",
	provider: "claude-code",
	prs: [],
	status: "working",
	terminalHandleId: "source-terminal",
	title: "do the thing",
	updatedAt: "2026-06-10T00:00:00Z",
	workspaceId: "proj-1",
	workspaceName: "my-app",
};

function renderDialog(
	session: WorkspaceSession = worker,
	onOpenChange = vi.fn(),
	agentSwitch?: AgentSwitchSummary,
) {
	const queryClient = new QueryClient({
		defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
	});
	for (const agentId of ["claude-code", "codex"]) {
		queryClient.setQueryData(agentModelsQueryKey(agentId, session.workspaceId), {
			agentId,
			allowCustom: false,
			fetchedAt: "2026-06-10T00:00:00Z",
			models:
				agentId === "codex"
					? [
							{ id: "gpt-5.4", label: "GPT-5.4", isDefault: true },
							{ id: "gpt-5.4-mini", label: "GPT-5.4 Mini" },
						]
					: [{ id: "claude-opus-4-6", label: "Claude Opus 4.6", isDefault: true }],
			selectionMode: "catalog",
			source: "test",
			stale: false,
		});
	}
	const result = render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<SwitchAgentDialog
					agentSwitch={agentSwitch}
					container={document.body}
					onOpenChange={onOpenChange}
					open
					session={session}
				/>
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return { ...result, onOpenChange, queryClient };
}

beforeEach(() => {
	switchMocks.clear.mockReset();
	switchMocks.mutate.mockReset();
	switchMocks.recoverMutate.mockReset();
	switchMocks.recoverState.error = null;
	switchMocks.recoverState.isPending = false;
	switchMocks.state.error = null;
	switchMocks.state.isPending = false;
});

describe("SwitchAgentDialog", () => {
	it("renders a compact agent and model picker without optional context or cancel actions", () => {
		renderDialog();

		const dialog = screen.getByRole("dialog", { name: "Switch agent" });
		expect(dialog).toHaveAttribute("data-slot", "dialog-content");
		const backdrop = screen.getByTestId("switch-agent-terminal-backdrop");
		expect(backdrop).toHaveClass("agent-switch-terminal-scrim");
		expect(
			within(dialog).getByText(
				"Move this session from Claude Code to another agent. AO will preserve the current native session and hand off the work.",
			),
		).toBeInTheDocument();
		expect(within(dialog).getByRole("button", { name: "Target agent" })).toBeInTheDocument();
		expect(within(dialog).getByRole("button", { name: "Model" })).toBeInTheDocument();
		expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
		expect(within(dialog).queryByText("Switch history")).not.toBeInTheDocument();
		expect(within(dialog).queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
		expect(within(dialog).getByRole("button", { name: "Close switch agent dialog" })).toBeInTheDocument();
		const switchButton = within(dialog).getByRole("button", { name: "Switch" });
		expect(switchButton).toHaveClass("size-(--size-settings-action-height)");
		expect(switchButton.textContent).toBe("");
		expect(switchButton.querySelector(".lucide-repeat-2")).not.toBeNull();
	});

	it("dismisses from the close button before admission", async () => {
		const { onOpenChange } = renderDialog();

		await userEvent.click(screen.getByRole("button", { name: "Close switch agent dialog" }));

		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	it("closes only after switch admission succeeds", async () => {
		const { onOpenChange } = renderDialog();
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });
		await userEvent.click(within(dialog).getByRole("button", { name: "Model" }));
		await userEvent.click(screen.getByRole("menuitem", { name: "GPT-5.4 Mini" }));

		await userEvent.click(within(dialog).getByRole("button", { name: "Switch" }));

		expect(switchMocks.mutate).toHaveBeenCalledWith(
			{
				idempotencyKey: "idempotency-1",
				model: "gpt-5.4-mini",
				session: worker,
				targetHarness: "codex",
			},
			{ onSuccess: expect.any(Function) },
		);
		expect(onOpenChange).not.toHaveBeenCalled();

		const options = switchMocks.mutate.mock.calls[0]?.[1] as { onSuccess: () => void };
		options.onSuccess();
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	it("keeps direct model IDs in the same searchable model picker", async () => {
		const { queryClient } = renderDialog();
		queryClient.setQueryData(agentModelsQueryKey("codex", worker.workspaceId), {
			agentId: "codex",
			allowCustom: true,
			customModelEntry: "direct",
			fetchedAt: "2026-06-10T00:00:00Z",
			models: [{ id: "gpt-5.4", label: "GPT-5.4", isDefault: true }],
			selectionMode: "catalog",
			source: "test",
			stale: false,
		});

		const dialog = screen.getByRole("dialog", { name: "Switch agent" });
		const model = within(dialog).getByRole("button", { name: "Model" });
		await userEvent.click(model);
		await userEvent.type(screen.getByRole("searchbox", { name: "Search model" }), "private/model-id");
		await userEvent.click(
			screen.getByRole("menuitem", { name: "Use “private/model-id” as a custom model" }),
		);

		expect(model).toHaveTextContent("private/model-id");
		expect(within(dialog).queryByRole("textbox", { name: "Model" })).not.toBeInTheDocument();
	});

	it("resets the previous target model when the active agent changes", async () => {
		const { queryClient, rerender } = renderDialog();
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });
		await userEvent.click(within(dialog).getByRole("button", { name: "Model" }));
		await userEvent.click(screen.getByRole("menuitem", { name: "GPT-5.4 Mini" }));
		expect(within(dialog).getByRole("button", { name: "Model" })).toHaveTextContent("GPT-5.4 Mini");

		const switchedSession = { ...worker, provider: "codex" as const };
		rerender(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<SwitchAgentDialog
						container={document.body}
						onOpenChange={vi.fn()}
						open
						session={switchedSession}
					/>
				</TooltipProvider>
			</QueryClientProvider>,
		);

		await waitFor(() =>
			expect(screen.getByRole("button", { name: "Model" })).toHaveTextContent(
				"Use Claude Code's default",
			),
		);
		await userEvent.click(screen.getByRole("button", { name: "Switch" }));
		expect(switchMocks.mutate).toHaveBeenLastCalledWith(
			{
				idempotencyKey: "idempotency-1",
				model: "",
				session: switchedSession,
				targetHarness: "claude-code",
			},
			{ onSuccess: expect.any(Function) },
		);
	});

	it("keeps admission controls visible but disabled while displaying Starting...", () => {
		switchMocks.state.isPending = true;

		renderDialog();
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });

		expect(within(dialog).getByRole("button", { name: "Target agent" })).toBeDisabled();
		expect(within(dialog).getByRole("button", { name: "Model" })).toBeDisabled();
		expect(within(dialog).getByRole("button", { name: "Close switch agent dialog" })).toBeDisabled();
		expect(within(dialog).getByRole("button", { name: "Starting..." })).toBeDisabled();
	});

	it("keeps admission failures inline for correction", () => {
		switchMocks.state.error = "target agent is unavailable";

		renderDialog();

		expect(screen.getByRole("alert")).toHaveTextContent("target agent is unavailable");
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();
	});

	it("closes the stale composer when a durable switch starts elsewhere", async () => {
		const onOpenChange = vi.fn();
		renderDialog({
			...worker,
			activeAgentSwitch: {
				agentHandoffStatus: "requested",
				fromHarness: "claude-code",
				id: "switch-external",
				state: "preparing_handoff",
				targetHarness: "codex",
			},
		}, onOpenChange);

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(switchMocks.mutate).not.toHaveBeenCalled();
	});

	it("shows a recovery explanation and refreshes durable state", async () => {
		const recoverySession = {
			...worker,
			activeAgentSwitch: {
				agentHandoffStatus: "received",
				errorCode: "target_start_unconfirmed",
				fromHarness: "claude-code",
				id: "switch-recovery",
				state: "starting_target",
				targetHarness: "codex",
			},
		} satisfies WorkspaceSession;
		const { queryClient } = renderDialog(recoverySession);
		const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });

		expect(within(dialog).getByText("Target startup could not be confirmed")).toBeInTheDocument();
		expect(within(dialog).queryByRole("button", { name: "Target agent" })).not.toBeInTheDocument();
		await userEvent.click(within(dialog).getByRole("button", { name: "Refresh" }));

		expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-agent-switches", "sess-1"] });
		expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
	});

	it("offers to restore the previous agent after source rollback fails", async () => {
		const recoverySession = {
			...worker,
			activeAgentSwitch: {
				agentHandoffStatus: "received",
				errorCode: "source_restore_unconfirmed",
				fromHarness: "claude-code",
				id: "switch-source-recovery",
				state: "source_stopped",
				targetHarness: "codex",
			},
		} satisfies WorkspaceSession;
		renderDialog(recoverySession);
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });

		expect(within(dialog).getByText("Claude Code could not be restored")).toBeInTheDocument();
		await userEvent.click(within(dialog).getByRole("button", { name: "Restore Claude Code" }));
		expect(switchMocks.recoverMutate).toHaveBeenCalledWith({
			sessionId: "sess-1",
			switchId: "switch-source-recovery",
		});
		expect(within(dialog).queryByRole("button", { name: "Target agent" })).not.toBeInTheDocument();
	});

	it("offers to recover an unconfirmed source stop", async () => {
		const recoverySession = {
			...worker,
			activeAgentSwitch: {
				agentHandoffStatus: "received",
				errorCode: "source_stop_unconfirmed",
				fromHarness: "claude-code",
				id: "switch-source-stop-recovery",
				state: "stopping_source",
				targetHarness: "codex",
			},
		} satisfies WorkspaceSession;
		renderDialog(recoverySession);
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });

		expect(within(dialog).getByText("Claude Code status could not be confirmed")).toBeInTheDocument();
		await userEvent.click(within(dialog).getByRole("button", { name: "Check Claude Code" }));
		expect(switchMocks.recoverMutate).toHaveBeenCalledWith({
			sessionId: "sess-1",
			switchId: "switch-source-stop-recovery",
		});
	});

	it("releases stale recovery UI when switch history observes terminal recovery", () => {
		const recoverySession = {
			...worker,
			activeAgentSwitch: {
				agentHandoffStatus: "received",
				errorCode: "source_stop_unconfirmed",
				fromHarness: "claude-code",
				id: "switch-source-stop-recovery",
				state: "stopping_source",
				targetHarness: "codex",
			},
		} satisfies WorkspaceSession;
		const recoveredSwitch = {
			...recoverySession.activeAgentSwitch,
			state: "failed",
		} satisfies AgentSwitchSummary;

		renderDialog(recoverySession, vi.fn(), recoveredSwitch);
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });

		expect(within(dialog).queryByText("Claude Code status could not be confirmed")).not.toBeInTheDocument();
		expect(within(dialog).queryByRole("button", { name: "Check Claude Code" })).not.toBeInTheDocument();
		expect(within(dialog).getByRole("button", { name: "Target agent" })).toBeInTheDocument();
	});
});
