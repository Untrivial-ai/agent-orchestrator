import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../../types/workspace";
import { useUiStore } from "../../stores/ui-store";
import { workspaceQueryKey } from "../../hooks/useWorkspaceQuery";

const LINK = "http://localhost:5173";

const { postMock, conversationState } = vi.hoisted(() => ({
	postMock: vi.fn(),
	conversationState: {
		snapshot: { capabilities: [] } as { capabilities: string[] } | undefined,
		isLoading: false,
		unavailable: undefined as { message: string } | undefined,
		error: undefined as string | undefined,
		hasOlder: false,
		isLoadingOlder: false,
		loadOlder: vi.fn(),
	},
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { POST: postMock },
}));

vi.mock("../../hooks/useConversation", () => ({
	useConversation: () => conversationState,
	useConversationCommands: () => ({}),
	useConversationConfigOptions: () => ({ options: [] }),
	useConversationModels: () => ({ models: [] }),
	useConversationSkills: () => ({ skills: [] }),
	useStageAttachments: () => undefined,
	useWorkspaceFilePaths: () => ({ paths: [], truncated: false }),
}));

vi.mock("./ChatWorkspace", () => ({
	ChatWorkspace: ({
		onLinkOpen,
		switchAgentControl,
		shellTarget,
	}: {
		onLinkOpen?: (url: string) => void;
		switchAgentControl?: ReactNode;
		shellTarget?: { handleId: string };
	}) => (
		<div>
			<button type="button" onClick={() => onLinkOpen?.(LINK)}>
				Open chat link
			</button>
			{shellTarget ? <div data-testid="shell-target">{shellTarget.handleId}</div> : null}
			{switchAgentControl}
		</div>
	),
}));

vi.mock("../TerminalSwitchAgentButton", () => ({
	TerminalSwitchAgentButton: () => <button aria-label="Switch agent" type="button" />,
}));

import { SessionChatSurface } from "./SessionChatSurface";

const session = {
	id: "sess-1",
	terminalHandleId: "handle-1",
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
	postMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
	conversationState.snapshot = { capabilities: [] };
	conversationState.isLoading = false;
	conversationState.unavailable = undefined;
	conversationState.error = undefined;
	conversationState.hasOlder = false;
	conversationState.isLoadingOlder = false;
	conversationState.loadOlder = vi.fn();
	useUiStore.setState({ inspectorSessions: {} });
});

describe("SessionChatSurface link routing", () => {
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

	// The chat surface offers the same in-place agent switch the terminal pane's
	// tab strip does (#4033): the control must be reachable without leaving chat.
	it("offers the in-place agent switch inside the chat surface", () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={session} />
			</Wrapper>,
		);

		expect(screen.getByRole("button", { name: "Switch agent" })).toBeInTheDocument();
	});

	it("does not offer in-place agent switching before the runtime handle exists", () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
		});
		render(
			<Wrapper client={queryClient}>
				<SessionChatSurface session={{ ...session, terminalHandleId: undefined }} />
			</Wrapper>,
		);

		expect(screen.queryByRole("button", { name: "Switch agent" })).not.toBeInTheDocument();
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
});
