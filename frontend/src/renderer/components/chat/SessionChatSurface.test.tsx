import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { agentSwitchesQueryKey } from "../../hooks/useAgentSwitches";
import type { ConversationSnapshot } from "../../types/conversation";
import type { AgentSwitchSummary, WorkspaceSession } from "../../types/workspace";
import { useUiStore } from "../../stores/ui-store";
import { workspaceQueryKey } from "../../hooks/useWorkspaceQuery";

const LINK = "http://localhost:5173";

function snapshotFor(sessionId: string): ConversationSnapshot & { capabilities: string[] } {
	return {
		activeBranchId: "branch-root",
		branchPoints: [],
		capabilities: [],
		conversationId: `conv-${sessionId}`,
		sessionId,
		harness: "codex",
		mode: "chat",
		controller: { state: "ready" },
		items: [],
		turns: [],
		settings: {},
		mcpServers: [],
		oldestSequence: 0,
		latestSequence: 0,
		hasMoreBefore: false,
	};
}

const {
	catalogObserverState,
	clearCatalogsMock,
	getMock,
	invalidateCatalogsMock,
	postMock,
	conversationState,
	conversationCommandState,
	agentSwitchState,
} = vi.hoisted(() => ({
	catalogObserverState: { enabled: [] as boolean[] },
	clearCatalogsMock: vi.fn(),
	getMock: vi.fn(),
	invalidateCatalogsMock: vi.fn(),
	postMock: vi.fn(),
	agentSwitchState: { data: [] as AgentSwitchSummary[] },
	conversationCommandState: {
		busy: false,
		pendingAcceptedTurnId: undefined as string | undefined,
		acknowledgeAcceptedTurn: vi.fn(),
	},
	conversationState: {
		snapshot: { capabilities: [] } as
			| (Partial<ConversationSnapshot> & { capabilities: string[] })
			| undefined,
		isLoading: false,
		unavailable: undefined as { message: string } | undefined,
		error: undefined as string | undefined,
		hasOlder: false,
		isLoadingOlder: false,
		loadOlder: vi.fn(),
	},
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("../../hooks/useConversation", () => ({
	clearConversationProviderCatalogs: clearCatalogsMock,
	conversationQueryKey: (sessionId: string) => ["conversation", sessionId],
	invalidateConversationProviderCatalogs: invalidateCatalogsMock,
	useConversation: (sessionId: string) => ({
		...conversationState,
		snapshot: conversationState.snapshot
			? { ...snapshotFor(sessionId), ...conversationState.snapshot }
			: undefined,
	}),
	useConversationCommands: () => conversationCommandState,
	useConversationConfigOptions: (_sessionId: string, enabled: boolean) => {
		catalogObserverState.enabled.push(enabled);
		return { options: [] };
	},
	useConversationModels: () => ({ models: [] }),
	useConversationSkills: () => ({ skills: [] }),
	useStageAttachments: () => undefined,
	useWorkspaceFilePaths: () => ({ paths: [], truncated: false }),
}));

vi.mock("./ChatWorkspace", async () => {
	const { useState } = await vi.importActual<typeof import("react")>("react");
	return {
		ChatWorkspace: ({
			agentInputDisabled,
			headerActions,
			sessionTabAction,
			newWorkDisabled,
			onLinkOpen,
			snapshot,
			shellTarget,
		}: {
			agentInputDisabled?: boolean;
			headerActions?: ReactNode;
			sessionTabAction?: ReactNode;
			newWorkDisabled?: boolean;
			onLinkOpen?: (url: string) => void;
			snapshot: { sessionId?: string };
			shellTarget?: { handleId: string };
		}) => {
			const [mountedSessionId] = useState(snapshot.sessionId);
			return (
				<div>
					<div
						data-testid="chat-agent-input"
						data-disabled={agentInputDisabled ? "true" : "false"}
					/>
					<div
						data-testid="chat-new-work"
						data-disabled={newWorkDisabled ? "true" : "false"}
					/>
					{snapshot.sessionId ? <div>Mounted {mountedSessionId}</div> : null}
					{snapshot.sessionId ? <div>Rendered {snapshot.sessionId}</div> : null}
					{headerActions}
					{sessionTabAction}
					<button type="button" onClick={() => onLinkOpen?.(LINK)}>
						Open chat link
					</button>
					{shellTarget ? <div data-testid="shell-target">{shellTarget.handleId}</div> : null}
				</div>
			);
		},
	};
});

import { SessionChatSurface } from "./SessionChatSurface";

const session = {
	id: "sess-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "chat worker",
	provider: "codex",
	kind: "worker",
	mode: "chat",
	status: "working",
	updatedAt: "2026-08-08T00:00:00Z",
	prs: [],
} satisfies WorkspaceSession;

function Wrapper({ client, children }: { client: QueryClient; children: ReactNode }) {
	return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset().mockImplementation(async () => ({
		data: { switches: agentSwitchState.data },
		error: undefined,
		response: { status: 200 },
	}));
	postMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
	clearCatalogsMock.mockReset();
	invalidateCatalogsMock.mockReset();
	conversationState.snapshot = { capabilities: [] };
	conversationState.isLoading = false;
	conversationState.unavailable = undefined;
	conversationState.error = undefined;
	conversationState.hasOlder = false;
	conversationState.isLoadingOlder = false;
	conversationState.loadOlder = vi.fn();
	conversationCommandState.busy = false;
	conversationCommandState.pendingAcceptedTurnId = undefined;
	conversationCommandState.acknowledgeAcceptedTurn.mockReset();
	agentSwitchState.data = [];
	catalogObserverState.enabled = [];
	useUiStore.setState({ inspectorSessions: {} });
});

afterEach(() => {
	vi.useRealTimers();
});

describe("SessionChatSurface link routing", () => {
	it("does not report idle work before the conversation snapshot loads", () => {
		conversationState.snapshot = undefined;
		conversationState.isLoading = true;
		const onConversationWorkChange = vi.fn();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					onConversationWorkChange={onConversationWorkChange}
				/>
			</Wrapper>,
		);

		expect(onConversationWorkChange).not.toHaveBeenCalled();
	});

	it("does not attribute a previous session snapshot's work to the destination", () => {
		conversationState.snapshot = {
			...snapshotFor("sess-previous"),
			controller: { state: "busy" },
			turns: [
				{
					id: "turn-previous",
					state: "running",
					requestedAt: "2026-08-25T09:00:00Z",
				},
			],
		};
		const onConversationWorkChange = vi.fn();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					onConversationWorkChange={onConversationWorkChange}
				/>
			</Wrapper>,
		);

		expect(onConversationWorkChange).not.toHaveBeenCalled();
	});

	it("reports live and queued Chat work to the interface-switch owner", async () => {
		conversationState.snapshot = {
			capabilities: [],
			controller: { state: "busy" },
			turns: [
				{ id: "turn-running", state: "running", requestedAt: "2026-08-25T09:00:00Z" },
				{ id: "turn-queued", state: "queued", requestedAt: "2026-08-25T09:00:01Z" },
			],
		};
		const onConversationWorkChange = vi.fn();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					onConversationWorkChange={onConversationWorkChange}
				/>
			</Wrapper>,
		);

		await waitFor(() => {
			expect(onConversationWorkChange).toHaveBeenLastCalledWith({
				controllerBusy: true,
				hasRunningTurn: true,
				queuedTurnCount: 1,
			});
		});
	});

	it("reports pending local work while the cached conversation snapshot is idle", async () => {
		conversationState.snapshot = {
			capabilities: [],
			controller: { state: "ready" },
			turns: [],
		};
		conversationCommandState.busy = true;
		const onConversationWorkChange = vi.fn();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					onConversationWorkChange={onConversationWorkChange}
				/>
			</Wrapper>,
		);

		await waitFor(() => {
			expect(onConversationWorkChange).toHaveBeenLastCalledWith({
				controllerBusy: true,
				hasRunningTurn: false,
				queuedTurnCount: 0,
			});
		});
	});

	it("reports an accepted local turn while the conversation snapshot is still stale", async () => {
		conversationState.snapshot = {
			capabilities: [],
			controller: { state: "ready" },
			turns: [],
		};
		conversationCommandState.pendingAcceptedTurnId = "turn-accepted";
		const onConversationWorkChange = vi.fn();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					onConversationWorkChange={onConversationWorkChange}
				/>
			</Wrapper>,
		);

		await waitFor(() => {
			expect(onConversationWorkChange).toHaveBeenLastCalledWith({
				controllerBusy: true,
				hasRunningTurn: false,
				queuedTurnCount: 0,
			});
		});
	});

	it("returns to idle after the accepted turn appears in the conversation snapshot", async () => {
		conversationState.snapshot = {
			capabilities: [],
			controller: { state: "ready" },
			turns: [
				{
					id: "turn-accepted",
					state: "completed",
					requestedAt: "2026-08-25T09:00:00Z",
				},
			],
		};
		conversationCommandState.pendingAcceptedTurnId = "turn-accepted";
		const onConversationWorkChange = vi.fn();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					onConversationWorkChange={onConversationWorkChange}
				/>
			</Wrapper>,
		);

		await waitFor(() => {
			expect(conversationCommandState.acknowledgeAcceptedTurn).toHaveBeenCalledWith(
				"turn-accepted",
			);
			expect(onConversationWorkChange).toHaveBeenLastCalledWith({
				controllerBusy: false,
				hasRunningTurn: false,
				queuedTurnCount: 0,
			});
		});
	});

	it("opens a plain Chat link in the active worker AO Browser", async () => {
		const user = userEvent.setup();
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} />
			</Wrapper>,
		);
		await user.click(screen.getByRole("button", { name: "Open chat link" }));

		expect(useUiStore.getState().inspectorSessions[session.id]).toMatchObject({
			isOpen: true,
			view: "browser",
		});
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/preview", {
			params: { path: { sessionId: session.id } },
			body: { url: LINK },
		});
		await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: workspaceQueryKey }));
	});

	// SessionView owns the switch-agent control on the primary session tab; the chat
	// surface forwards it into ChatWorkspace.
	it("forwards session tab actions into the chat workspace", () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					sessionTabAction={<button type="button">Session actions</button>}
				/>
			</Wrapper>,
		);

		expect(screen.getByRole("button", { name: "Session actions" })).toBeInTheDocument();
	});

	it("fences new work without applying the decision-blocking agent lock", () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} newWorkDisabled />
			</Wrapper>,
		);

		expect(screen.getByTestId("chat-agent-input")).toHaveAttribute("data-disabled", "false");
		expect(screen.getByTestId("chat-new-work")).toHaveAttribute("data-disabled", "true");
	});

	it.each([
		[
			"nonterminal progress",
			{ id: "switch-progress", state: "starting_target" },
			"in_progress",
			true,
		],
		[
			"restart recovery",
			{ id: "switch-recovery", state: "starting_target", errorCode: "target_start_unconfirmed" },
			"recovery",
			false,
		],
	] as const)("restores durable %s presentation and locks Chat input after reload", async (_name, overrides, outcome, _buttonDisabled) => {
		agentSwitchState.data = [
			{
				agentHandoffStatus: "not_attempted",
				fromHarness: "claude-code",
				targetHarness: "codex",
				...overrides,
			},
		];
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} />
			</Wrapper>,
		);

		await waitFor(() => {
			expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute("data-outcome", outcome);
		});
		expect(screen.getByTestId("chat-agent-input")).toHaveAttribute("data-disabled", "true");
		expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute("data-outcome", outcome);
		if (outcome === "in_progress") {
			const progress = screen.getByRole("list", { name: "Switching…" });
			expect(progress.querySelector('[aria-current="step"]')).toHaveTextContent("Starting target agent");
		} else {
			expect(screen.queryByRole("list", { name: "Switching…" })).not.toBeInTheDocument();
		}
	});

	it("uses a ready Chat controller as the completed takeover proof", async () => {
		const completedSwitch = {
			agentHandoffStatus: "not_attempted",
			fromHarness: "claude-code",
			id: "switch-completed",
			state: "completed",
			targetHarness: "codex",
		} satisfies AgentSwitchSummary;
		agentSwitchState.data = [completedSwitch];
		conversationState.snapshot = {
			capabilities: [],
			controller: { state: "ready" },
			harness: "codex",
		};
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={{
						...session,
						activeAgentSwitch: { ...completedSwitch, state: "target_ready" },
					}}
				/>
			</Wrapper>,
		);

		await waitFor(() => {
			expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
				"data-outcome",
				"success",
			);
		});
		expect(screen.getByTestId("chat-agent-input")).toHaveAttribute("data-disabled", "false");
		expect(screen.queryByRole("list", { name: "Switching…" })).not.toBeInTheDocument();
	});

	it("reconciles catalogs when the first fetched switch state is already completed", async () => {
		const completedSwitch = {
			agentHandoffStatus: "received",
			fromHarness: "claude-code",
			id: "switch-terminal-first",
			state: "completed",
			targetHarness: "codex",
		} satisfies AgentSwitchSummary;
		agentSwitchState.data = [completedSwitch];
		conversationState.snapshot = {
			capabilities: ["config_options"],
			controller: { state: "ready" },
			harness: "codex",
		};
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} />
			</Wrapper>,
		);

		await waitFor(() => {
			expect(clearCatalogsMock).toHaveBeenCalledWith(queryClient, session.id);
			expect(invalidateCatalogsMock).toHaveBeenCalledWith(queryClient, session.id);
		});
		expect(catalogObserverState.enabled).toContain(false);
		await waitFor(() => expect(catalogObserverState.enabled.at(-1)).toBe(true));
		expect(screen.queryByTestId("chat-agent-switch-status")).not.toBeInTheDocument();
	});

	it("waits for a ready or busy controller owned by the target harness", async () => {
		const completedSwitch = {
			agentHandoffStatus: "received",
			fromHarness: "claude-code",
			id: "switch-target-controller-proof",
			state: "completed",
			targetHarness: "codex",
		} satisfies AgentSwitchSummary;
		agentSwitchState.data = [completedSwitch];
		conversationState.snapshot = {
			capabilities: ["config_options"],
			controller: { state: "ready" },
			harness: "claude-code",
		};
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		queryClient.setQueryData(agentSwitchesQueryKey(session.id), [completedSwitch]);
		const targetSession = {
			...session,
			activeAgentSwitch: { ...completedSwitch, state: "target_ready" as const },
		};
		const view = render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={targetSession} />
			</Wrapper>,
		);

		await waitFor(() => {
			expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
				"data-outcome",
				"in_progress",
			);
		});
		expect(catalogObserverState.enabled.at(-1)).toBe(false);

		conversationState.snapshot = {
			capabilities: ["config_options"],
			controller: { state: "busy" },
			harness: "codex",
		};
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={targetSession} />
			</Wrapper>,
		);

		await waitFor(() => {
			expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
				"data-outcome",
				"success",
			);
		});
		expect(catalogObserverState.enabled.at(-1)).toBe(true);
	});

	it("ignores completed switch history when a stopped Chat controller reloads", () => {
		const historicalSwitch = {
			agentHandoffStatus: "received",
			fromHarness: "claude-code",
			id: "switch-historical-completion",
			state: "completed",
			targetHarness: "codex",
		} satisfies AgentSwitchSummary;
		agentSwitchState.data = [historicalSwitch];
		conversationState.snapshot = { capabilities: [], controller: { state: "stopped" } };
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		queryClient.setQueryData(agentSwitchesQueryKey(session.id), [historicalSwitch]);

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} />
			</Wrapper>,
		);

		expect(screen.queryByTestId("chat-agent-switch-status")).not.toBeInTheDocument();
		expect(screen.getByTestId("chat-agent-input")).toHaveAttribute("data-disabled", "false");
	});

	it("keeps failure visible until a retry settles, then ignores a later controller stop", async () => {
		const user = userEvent.setup();
		const activeSwitch = {
			agentHandoffStatus: "not_attempted",
			fromHarness: "claude-code",
			id: "switch-failed-after-admission",
			state: "starting_target",
			targetHarness: "codex",
		} satisfies AgentSwitchSummary;
		const failedSwitch = {
			...activeSwitch,
			errorCode: "target_binary_missing",
			state: "failed",
		} satisfies AgentSwitchSummary;
		agentSwitchState.data = [activeSwitch];
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const view = render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...session, activeAgentSwitch: activeSwitch }} />
			</Wrapper>,
		);

		await waitFor(() => {
			expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
				"data-outcome",
				"in_progress",
			);
		});

		agentSwitchState.data = [failedSwitch];
		act(() => {
			queryClient.setQueryData(agentSwitchesQueryKey(session.id), [failedSwitch]);
		});
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} />
			</Wrapper>,
		);

		expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
			"data-outcome",
			"failure",
		);
		expect(screen.getByTestId("chat-agent-switch-status")).toHaveTextContent(
			"Target agent is not installed",
		);

		await user.click(screen.getByRole("button", { name: "Close" }));
		expect(screen.queryByTestId("chat-agent-switch-status")).not.toBeInTheDocument();

		const retrySwitch = {
			agentHandoffStatus: "not_attempted",
			fromHarness: "codex",
			id: "switch-successful-retry",
			state: "starting_target",
			targetHarness: "claude-code",
		} satisfies AgentSwitchSummary;
		agentSwitchState.data = [retrySwitch, failedSwitch];
		act(() => {
			queryClient.setQueryData(agentSwitchesQueryKey(session.id), [retrySwitch, failedSwitch]);
		});
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...session, activeAgentSwitch: retrySwitch }} />
			</Wrapper>,
		);
		expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
			"data-outcome",
			"in_progress",
		);

		const completedRetry = {
			...retrySwitch,
			state: "completed",
		} satisfies AgentSwitchSummary;
		vi.useFakeTimers();
		conversationState.snapshot = {
			capabilities: [],
			controller: { state: "ready" },
			harness: "claude-code",
		};
		agentSwitchState.data = [completedRetry, failedSwitch];
		act(() => {
			queryClient.setQueryData(agentSwitchesQueryKey(session.id), [completedRetry, failedSwitch]);
		});
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={{ ...session, activeAgentSwitch: completedRetry, provider: "claude-code" }}
				/>
			</Wrapper>,
		);

		expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
			"data-outcome",
			"success",
		);

		conversationState.snapshot = { capabilities: [], controller: { state: "stopped" } };
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={{ ...session, activeAgentSwitch: completedRetry, provider: "claude-code" }}
				/>
			</Wrapper>,
		);
		expect(screen.getByTestId("chat-agent-switch-status")).toHaveAttribute(
			"data-outcome",
			"success",
		);
		expect(screen.getByTestId("chat-agent-input")).toHaveAttribute("data-disabled", "false");

		act(() => vi.advanceTimersByTime(3_000));
		expect(screen.queryByTestId("chat-agent-switch-status")).not.toBeInTheDocument();
		expect(screen.getByTestId("chat-agent-input")).toHaveAttribute("data-disabled", "false");
	});

	it("keeps a selected shell renderable when the conversation is unavailable", () => {
		conversationState.snapshot = undefined;
		conversationState.unavailable = { message: "Controller is unavailable" };
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface
					session={session}
					shellTarget={{
						kind: "shell",
						handleId: "shell-1",
						sessionId: session.id,
						title: "shell",
						generation: "2026-08-16T00:00:00Z",
					}}
				/>
			</Wrapper>,
		);

		expect(screen.getByTestId("shell-target")).toHaveTextContent("shell-1");
		expect(screen.queryByText("Conversation unavailable")).not.toBeInTheDocument();
	});

	it("remounts the chat workspace when switching between chat sessions", () => {
		const first = { ...session, id: "proj-orchestrator-1", kind: "orchestrator" as const };
		const second = { ...session, id: "proj-orchestrator-2", kind: "orchestrator" as const };
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		conversationState.snapshot = snapshotFor(first.id);

		const view = render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={first} />
			</Wrapper>,
		);

		expect(screen.getByText("Mounted proj-orchestrator-1")).toBeInTheDocument();
		expect(screen.getByText("Rendered proj-orchestrator-1")).toBeInTheDocument();

		conversationState.snapshot = snapshotFor(second.id);
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={second} />
			</Wrapper>,
		);

		expect(screen.getByText("Mounted proj-orchestrator-2")).toBeInTheDocument();
		expect(screen.getByText("Rendered proj-orchestrator-2")).toBeInTheDocument();
		expect(screen.queryByText("Mounted proj-orchestrator-1")).not.toBeInTheDocument();
	});
});
