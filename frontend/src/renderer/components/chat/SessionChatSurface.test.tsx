import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ChatConfigOption, ConversationMessage, ConversationSnapshot } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";
import { useUiStore } from "../../stores/ui-store";
import { workspaceQueryKey } from "../../hooks/useWorkspaceQuery";
import { useConversationConfigOptions, useConversationModels, useConversationSkills } from "../../hooks/useConversation";

const LINK = "http://localhost:5173";

function snapshotFor(sessionId: string): ConversationSnapshot & { capabilities: string[] } {
	return {
		activeBranchId: "branch-root",
		branchPoints: [],
		capabilities: [],
		conversationId: `conv-${sessionId}`,
		sessionId,
		harness: "opencode",
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
	getMock,
	postMock,
	workspacePathsState,
	conversationState,
	conversationCommandState,
} = vi.hoisted(() => ({
	catalogObserverState: { enabled: [] as boolean[] },
	getMock: vi.fn(),
	postMock: vi.fn(),
	workspacePathsState: { paths: [] as string[] },
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

const configState = vi.hoisted(() => ({
	options: [] as ChatConfigOption[], loaded: false, error: undefined as string | undefined,
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	getApiBaseUrl: () => "",
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("../../hooks/useConversation", () => ({
	conversationQueryKey: (sessionId: string) => ["conversation", sessionId],
	useConversation: (sessionId: string) => ({
		...conversationState,
		snapshot: conversationState.snapshot
			? { ...snapshotFor(sessionId), ...conversationState.snapshot }
			: undefined,
	}),
	useConversationCommands: () => conversationCommandState,
	useConversationConfigOptions: vi.fn((_sessionId: string, enabled: boolean) => {
		catalogObserverState.enabled.push(enabled);
		return configState;
	}),
	useConversationModels: vi.fn(() => ({ models: [] })),
	useConversationSkills: vi.fn(() => ({ skills: [] })),
	useStageAttachments: () => undefined,
	useWorkspaceFilePaths: () => ({ paths: workspacePathsState.paths, truncated: false }),
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
			onRememberPermissions,
			snapshot,
			shellTarget,
		}: {
			agentInputDisabled?: boolean;
			headerActions?: ReactNode;
			sessionTabAction?: ReactNode;
			newWorkDisabled?: boolean;
			onLinkOpen?: (url: string) => void;
			onRememberPermissions?: unknown;
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
					<div data-testid="remember-available">{String(Boolean(onRememberPermissions))}</div>
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
	provider: "opencode",
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
	workspacePathsState.paths = [];
	configState.options = [];
	configState.loaded = false;
	configState.error = undefined;
	getMock.mockReset();
	postMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
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

	it("opens a plain Chat link from an active orchestrator in its Browser panel", async () => {
		const user = userEvent.setup();
		const openInNewTab = vi.fn().mockResolvedValue(undefined);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const orchestratorSession = {
			...session,
			id: "proj-1-orchestrator",
			title: "orchestrator",
			kind: "orchestrator",
		} satisfies WorkspaceSession;

		try {
			render(
				<Wrapper client={queryClient}>
					<SessionChatSurface session={orchestratorSession} onOpenLinkInBrowser={openInNewTab} />
				</Wrapper>,
			);
			await user.click(screen.getByRole("button", { name: "Open chat link" }));

			expect(useUiStore.getState().inspectorSessions[orchestratorSession.id]).toMatchObject({ isOpen: true, view: "browser" });
			expect(openInNewTab).toHaveBeenCalledWith(LINK);
			expect(postMock).not.toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/preview", expect.anything());
		} finally {
			queryClient.clear();
		}
	});

	it("automatically opens the first link in a newly completed agent response once", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const openInBrowser = vi.fn().mockResolvedValue(undefined);
		const view = render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} onOpenLinkInBrowser={openInBrowser} />
			</Wrapper>,
		);

		conversationState.snapshot = {
			capabilities: [],
			items: [{
				kind: "message",
				id: "assistant-1",
				sequence: 1,
				revision: 1,
				role: "assistant",
				origin: "provider",
				text: "Done — see `https://example.com/result`.",
				streaming: false,
				createdAt: "2026-08-08T00:00:01Z",
			}],
		};
		// The surface is memoized; the app's conversation subscription re-renders
		// it with a fresh session identity. Model that through the memo boundary
		// so the auto-open effect re-reads the new snapshot.
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...session }} onOpenLinkInBrowser={openInBrowser} />
			</Wrapper>,
		);

		await waitFor(() => expect(openInBrowser).toHaveBeenCalledWith("https://example.com/result"));
		expect(openInBrowser).toHaveBeenCalledTimes(1);
		conversationState.snapshot = {
			capabilities: [],
			items: [{
				kind: "message", id: "assistant-2", sequence: 2, revision: 1,
				role: "assistant", origin: "provider", text: "Also see https://example.com/second",
				streaming: false, createdAt: "2026-08-08T00:00:02Z",
			}],
		};
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...session }} onOpenLinkInBrowser={openInBrowser} />
			</Wrapper>,
		);
		expect(openInBrowser).toHaveBeenCalledTimes(1);
	});

	it("does not reopen an old assistant link when an unrelated snapshot field changes", async () => {
		const localSession = { ...session, id: "session-link-baseline" };
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		const openInBrowser = vi.fn().mockResolvedValue(undefined);
		const oldAssistant = {
			kind: "message",
			id: "assistant-old",
			sequence: 1,
			revision: 1,
			role: "assistant",
			origin: "provider",
			text: "Old result: https://example.com/old",
			streaming: false,
			createdAt: "2026-08-08T00:00:01Z",
		} satisfies ConversationMessage;
		const currentUser = {
			kind: "message",
			id: "user-current",
			sequence: 2,
			revision: 1,
			role: "user",
			origin: "human",
			text: "Create a new result",
			streaming: false,
			createdAt: "2026-08-08T00:00:02Z",
		} satisfies ConversationMessage;
		conversationState.snapshot = {
			capabilities: [],
			items: [oldAssistant, currentUser],
			latestSequence: 2,
		};
		const view = render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={localSession} onOpenLinkInBrowser={openInBrowser} />
			</Wrapper>,
		);

		conversationState.snapshot = {
			capabilities: ["config_options"],
			items: [oldAssistant, currentUser],
			latestSequence: 2,
		};
		// The surface is memoized; the app's conversation subscription re-renders
		// it with a fresh session identity. Model that through the memo boundary
		// so the snapshots above are re-read.
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...localSession }} onOpenLinkInBrowser={openInBrowser} />
			</Wrapper>,
		);
		expect(openInBrowser).not.toHaveBeenCalled();

		conversationState.snapshot = {
			capabilities: ["config_options"],
			items: [
				oldAssistant,
				currentUser,
				{
					kind: "message",
					id: "assistant-current",
					sequence: 3,
					revision: 1,
					role: "assistant",
					origin: "provider",
					text: "New result: https://example.com/new",
					streaming: false,
					createdAt: "2026-08-08T00:00:03Z",
				},
			],
			latestSequence: 3,
		};
		view.rerender(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...localSession }} onOpenLinkInBrowser={openInBrowser} />
			</Wrapper>,
		);

		await waitFor(() => expect(openInBrowser).toHaveBeenCalledWith("https://example.com/new"));
		expect(openInBrowser).toHaveBeenCalledTimes(1);
	});

	it("automatically previews a newly completed workspace HTML link", async () => {
		workspacePathsState.paths = ["test-ui.html"];
		const localSession = { ...session, id: "session-local-html" };
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		const openInBrowser = vi.fn().mockResolvedValue(undefined);
		const view = render(<Wrapper client={queryClient}><SessionChatSurface session={localSession} onOpenLinkInBrowser={openInBrowser} /></Wrapper>);
		conversationState.snapshot = {
			capabilities: [],
			items: [{ kind: "message", id: "assistant-html", sequence: 1, revision: 1, role: "assistant", origin: "provider", text: "Done: [`test-ui.html`](/tmp/worktree/test-ui.html)", streaming: false, createdAt: "2026-08-08T00:00:01Z" }],
		};
		view.rerender(<Wrapper client={queryClient}><SessionChatSurface session={{ ...localSession }} onOpenLinkInBrowser={openInBrowser} /></Wrapper>);
		await waitFor(() => expect(openInBrowser).toHaveBeenCalledWith(
			expect.stringContaining("/api/v1/sessions/session-local-html/preview/files/test-ui.html"),
		));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("opens each plain Chat link in a new AO Browser tab", async () => {
		const user = userEvent.setup();
		const openInNewTab = vi.fn().mockResolvedValue(undefined);
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});

		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} onOpenLinkInBrowser={openInNewTab} />
			</Wrapper>,
		);
		await user.click(screen.getByRole("button", { name: "Open chat link" }));

		expect(openInNewTab).toHaveBeenCalledWith(LINK);
		expect(postMock).not.toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/preview", expect.anything());
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


describe("controller catalogs during an interface handoff", () => {
	it.each(["stopped", "connecting", "ready"] as const)("waits through handoff with a %s snapshot, then loads catalogs", (state) => {
		conversationState.snapshot = { capabilities: ["config_options"], controller: { state } };
		const client = new QueryClient();
		const { rerender } = render(<Wrapper client={client}><SessionChatSurface session={session} controllerTransitioning /></Wrapper>);
		for (const hook of [useConversationConfigOptions, useConversationModels, useConversationSkills]) {
			expect(hook).toHaveBeenLastCalledWith(session.id, false);
		}

		conversationState.snapshot = { capabilities: ["config_options"], controller: { state: "ready" } };
		rerender(<Wrapper client={client}><SessionChatSurface session={session} /></Wrapper>);
		for (const hook of [useConversationConfigOptions, useConversationModels, useConversationSkills]) {
			expect(hook).toHaveBeenLastCalledWith(session.id, true);
		}
	});

	it("does not poll an unavailable controller after a failed handoff", () => {
		conversationState.snapshot = { capabilities: ["config_options"], controller: { state: "stopped" } };
		render(<Wrapper client={new QueryClient()}><SessionChatSurface session={session} /></Wrapper>);
		for (const hook of [useConversationConfigOptions, useConversationModels, useConversationSkills]) {
			expect(hook).toHaveBeenLastCalledWith(session.id, false);
		}
	});
});

describe("project remembering waits for provider permissions", () => {
	it.each([undefined, "Catalog unavailable"])("withholds Remember when provider catalog is not known (%s)", (error) => {
		conversationState.snapshot = { capabilities: ["config_options"] };
		configState.error = error;
		render(<Wrapper client={new QueryClient()}><SessionChatSurface session={session} /></Wrapper>);
		expect(screen.getByTestId("remember-available")).toHaveTextContent("false");
	});

	it("allows remembering after a model-only catalog successfully loads", () => {
		conversationState.snapshot = { capabilities: ["config_options"] };
		const client = new QueryClient();
		const { rerender } = render(<Wrapper client={client}><SessionChatSurface session={session} /></Wrapper>);
		expect(screen.getByTestId("remember-available")).toHaveTextContent("false");
		configState.loaded = true;
		configState.options = [{ id: "model", name: "Model", category: "model", type: "select", choices: [] }];
		// The real query observer schedules this component when catalog data lands.
		// The lightweight hook mock has no subscription, so change the parent
		// session identity to model that notification through the memo boundary.
		rerender(<Wrapper client={client}><SessionChatSurface session={{ ...session }} /></Wrapper>);
		expect(screen.getByTestId("remember-available")).toHaveTextContent("true");
	});
});
