import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AGENT_OPTIONS } from "../lib/agent-options";
import {
	deriveAgentSwitchPresentation,
	type AgentSwitchPresentation,
} from "../lib/agent-switch-presentation";
import type { AgentSwitchSummary, WorkspaceSession } from "../types/workspace";
import { TerminalSwitchAgentButton } from "./TerminalSwitchAgentButton";
import { TooltipProvider } from "./ui/tooltip";

const { getMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: getMock,
		POST: postMock,
	},
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
}));

const worker: WorkspaceSession = {
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	branch: "ao/sess-1",
	id: "sess-1",
	kind: "worker",
	provider: "claude-code",
	prs: [],
	status: "working",
	title: "do the thing",
	updatedAt: "2026-06-10T00:00:00Z",
	workspaceId: "proj-1",
	workspaceName: "my-app",
};

function switchRecord(overrides: Partial<AgentSwitchSummary> = {}): AgentSwitchSummary {
	return {
		agentHandoffStatus: "not_attempted",
		fromHarness: "claude-code",
		id: "switch-1",
		state: "starting_target",
		targetHarness: "codex",
		...overrides,
	};
}

function switchPresentation(
	agentSwitch: AgentSwitchSummary,
	session: WorkspaceSession = worker,
): AgentSwitchPresentation {
	return deriveAgentSwitchPresentation({
		agentSwitch,
		activityState: session.activity?.state,
		currentHarness: session.provider,
		isTerminated: Boolean(session.isTerminated),
		terminalHandleId: session.terminalHandleId,
	});
}

function SwitchControlHarness({
	onOpenChangeObserved,
	presentation,
	session,
}: {
	onOpenChangeObserved?: (open: boolean) => void;
	presentation?: AgentSwitchPresentation;
	session: WorkspaceSession;
}) {
	const [container, setContainer] = useState<HTMLDivElement | null>(null);
	const [open, setOpen] = useState(false);
	const handleOpenChange = (nextOpen: boolean) => {
		onOpenChangeObserved?.(nextOpen);
		setOpen(nextOpen);
	};
	return (
		<div className="relative" data-testid="terminal-container" ref={setContainer}>
			<TerminalSwitchAgentButton
				container={container}
				onOpenChange={handleOpenChange}
				open={open}
				presentation={presentation}
				session={session}
				switchError={null}
			/>
		</div>
	);
}

function renderControl(
	session: WorkspaceSession = worker,
	presentation?: AgentSwitchPresentation,
	onOpenChangeObserved?: (open: boolean) => void,
) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	const control = (nextSession: WorkspaceSession, nextPresentation = presentation) => (
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<SwitchControlHarness
					key={nextSession.id}
					onOpenChangeObserved={onOpenChangeObserved}
					presentation={nextPresentation}
					session={nextSession}
				/>
			</TooltipProvider>
		</QueryClientProvider>
	);
	const result = render(control(session));
	return {
		...result,
		queryClient,
	};
}

beforeEach(() => {
	getMock.mockReset();
	getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
		if (path === "/api/v1/agents/{agent}/models") {
			const agentId = options?.params?.path?.agent ?? "codex";
			return {
				data: {
					agentId,
					allowCustom: false,
					fetchedAt: "2026-06-10T00:00:00Z",
					models: [{ id: agentId === "codex" ? "gpt-5.4" : "claude-opus-4-6", label: "Default" }],
					selectionMode: "catalog",
					source: "test",
					stale: false,
				},
				error: undefined,
				response: { status: 200 },
			};
		}
		return { data: { switches: [] }, error: undefined, response: { status: 200 } };
	});
	postMock.mockReset();
});

describe("TerminalSwitchAgentButton", () => {
	it("renders with the shared top-bar control treatment", async () => {
		renderControl();

		const button = await screen.findByRole("button", { name: "Switch agent" });
		expect(button.classList.contains("topbar-control--icon")).toBe(true);
		expect(button.classList.contains("size-control-md")).toBe(true);
		expect(button.classList.contains("size-control-lg")).toBe(false);
		expect(button.classList.contains("rounded-md")).toBe(true);
		expect(button.classList.contains("ml-1")).toBe(false);
		expect(button.classList.contains("rounded-full")).toBe(false);
		expect(button.querySelector(".lucide-repeat-2")).not.toBeNull();
		expect(button.querySelector("img")).toBeNull();
		expect(button.textContent).toBe("");
	});

	it.each([
		["unsupported provider", { provider: "cursor" }],
		["terminated worker", { isTerminated: true, status: "terminated" }],
		["orchestrator", { id: "orch-1", kind: "orchestrator" }],
	] as const)("does not render for an %s", (_name, overrides) => {
		renderControl({ ...worker, ...overrides } as WorkspaceSession);
		expect(screen.queryByRole("button", { name: "Switch agent" })).not.toBeInTheDocument();
	});

	it("opens the existing dialog and submits the selected switch", async () => {
		const activeSwitch = switchRecord();
		postMock.mockResolvedValue({ data: { switch: activeSwitch }, error: undefined, response: { status: 202 } });
		const { queryClient } = renderControl();
		const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");

		await userEvent.click(await screen.findByRole("button", { name: "Switch agent" }));
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });
		expect(document.querySelector(".dialog-overlay")).not.toBeInTheDocument();
		expect(document.body.style.pointerEvents).not.toBe("none");
		expect(screen.getByTestId("terminal-container")).toContainElement(dialog);
		expect(screen.getByTestId("terminal-container")).toContainElement(
			screen.getByTestId("switch-agent-terminal-backdrop"),
		);
		const targetAgent = within(dialog).getByRole("button", { name: "Target agent" });
		expect(targetAgent).toHaveTextContent("Codex");
		await userEvent.click(targetAgent);
		expect(screen.getAllByRole("menuitem")).toHaveLength(AGENT_OPTIONS.length);
		expect(screen.getByRole("menuitem", { name: /Cursor,\s*Coming soon/ })).toHaveAttribute("data-disabled");
		await userEvent.keyboard("{Escape}");
		expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
		await userEvent.click(within(dialog).getByRole("button", { name: "Switch" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/switch-agent", {
			params: { path: { sessionId: "sess-1" } },
			body: {
				idempotencyKey: expect.any(String),
				targetHarness: "codex",
			},
		});
		expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("keeps the open selector stable when the toolbar button is reactivated", async () => {
		const openChanges = vi.fn();
		renderControl(worker, undefined, openChanges);
		const button = await screen.findByRole("button", { name: "Switch agent" });

		await userEvent.click(button);
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();
		openChanges.mockClear();

		await userEvent.click(button);

		expect(openChanges).not.toHaveBeenCalled();
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();
	});

	it("closes after accepted admission without waiting for durable observer invalidations", async () => {
		postMock.mockResolvedValue({
			data: { switch: switchRecord() },
			error: undefined,
			response: { status: 202 },
		});
		const { queryClient } = renderControl();
		vi.spyOn(queryClient, "invalidateQueries").mockReturnValue(new Promise(() => {}));

		await userEvent.click(await screen.findByRole("button", { name: "Switch agent" }));
		await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Switch" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	it("keeps the selector locked during admission", async () => {
		postMock.mockReturnValue(new Promise(() => {}));
		renderControl();

		await userEvent.click(await screen.findByRole("button", { name: "Switch agent" }));
		await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Switch" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		const dialog = screen.getByRole("dialog", { name: "Switch agent" });
		expect(within(dialog).getByRole("button", { name: "Starting..." })).toBeDisabled();
		expect(within(dialog).getByRole("button", { name: "Close switch agent dialog" })).toBeDisabled();
	});

	it("keeps the dialog open when the daemon does not accept the switch", async () => {
		postMock.mockResolvedValue({
			data: { switch: switchRecord() },
			error: undefined,
			response: { status: 200 },
		});
		renderControl();

		await userEvent.click(await screen.findByRole("button", { name: "Switch agent" }));
		await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Switch" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("Failed to switch agent (200)");
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();
	});

	it("renders durable progress as a non-interactive status", async () => {
		const activeSwitch = switchRecord({ state: "delivering_context" });
		const exitedSession = {
			...worker,
			activeAgentSwitch: activeSwitch,
			activity: { state: "exited", lastActivityAt: "2026-06-10T00:00:02Z" },
			status: "exited",
		} satisfies WorkspaceSession;
		renderControl(exitedSession, switchPresentation(activeSwitch, exitedSession));

		const button = await screen.findByRole("button", { name: "Switching to Codex" });
		expect(button).toHaveAttribute("aria-busy", "true");
		expect(button).toBeDisabled();
		expect(button.querySelector(".lucide-loader-circle")).toBeInTheDocument();
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("opens recovery details when target startup is unconfirmed", async () => {
		const recoverySwitch = switchRecord({ errorCode: "target_start_unconfirmed" });
		const exitedSession = {
			...worker,
			activeAgentSwitch: recoverySwitch,
			activity: { state: "exited", lastActivityAt: "2026-06-10T00:00:02Z" },
			status: "exited",
		} satisfies WorkspaceSession;
		renderControl(exitedSession, switchPresentation(recoverySwitch, exitedSession));

		const button = await screen.findByRole("button", { name: "Agent switch needs recovery" });
		expect(button).not.toHaveAttribute("aria-busy");
		expect(button).not.toBeDisabled();
		expect(button.querySelector(".lucide-triangle-alert")).toBeInTheDocument();
		await userEvent.click(button);
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toHaveTextContent(
			"Target startup could not be confirmed",
		);
	});

	it("keeps a terminal failure available for a fresh switch attempt", async () => {
		const failedSwitch = switchRecord({ errorCode: "target_binary_missing", state: "failed" });
		renderControl(worker, switchPresentation(failedSwitch));

		const button = await screen.findByRole("button", { name: "Agent switch failed" });
		expect(button).not.toBeDisabled();
		expect(button.querySelector(".lucide-triangle-alert")).toBeInTheDocument();
		await userEvent.click(button);
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();
	});

	it("reopens a rejected switch and retries with a fresh idempotency key", async () => {
		postMock
			.mockResolvedValueOnce({
				data: undefined,
				error: { message: "target agent is unavailable" },
				response: { status: 409 },
			})
			.mockResolvedValueOnce({
				data: { switch: switchRecord({ id: "switch-2" }) },
				error: undefined,
				response: { status: 202 },
			});
		renderControl();

		await userEvent.click(await screen.findByRole("button", { name: "Switch agent" }));
		await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Switch" }));

		const reopenedDialog = await screen.findByRole("dialog", { name: "Switch agent" });
		expect(within(reopenedDialog).getByRole("alert")).toHaveTextContent("target agent is unavailable");
		const firstIdempotencyKey = postMock.mock.calls[0]?.[1]?.body?.idempotencyKey;
		await userEvent.click(within(reopenedDialog).getByRole("button", { name: "Switch" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(2));
		const retryIdempotencyKey = postMock.mock.calls[1]?.[1]?.body?.idempotencyKey;
		expect(firstIdempotencyKey).toEqual(expect.any(String));
		expect(retryIdempotencyKey).toEqual(expect.any(String));
		expect(retryIdempotencyKey).not.toBe(firstIdempotencyKey);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});
});
