import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: () => "request failed",
}));

import { useSwitchAgent } from "./useSwitchAgent";

const session = {
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	branch: "ao/sess-1",
	id: "sess-1",
	kind: "worker",
	provider: "codex",
	prs: [],
	status: "working",
	terminalHandleId: "source-terminal",
	title: "do the thing",
	updatedAt: "2026-06-10T00:00:00Z",
	workspaceId: "proj-1",
	workspaceName: "my-app",
} satisfies WorkspaceSession;

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

beforeEach(() => {
	postMock.mockReset();
});

describe("useSwitchAgent", () => {
	it("omits the default model and refreshes target conversation state", async () => {
		postMock.mockResolvedValue({
			data: {
				switch: {
					agentHandoffStatus: "not_attempted",
					fromHarness: "codex",
					id: "switch-1",
					state: "preparing_handoff",
					targetHarness: "claude-code",
				},
			},
			error: undefined,
			response: { status: 202 },
		});
		const queryClient = new QueryClient({
			defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
		});
		const invalidate = vi.spyOn(queryClient, "invalidateQueries");
		const { result } = renderHook(() => useSwitchAgent(), { wrapper: wrapper(queryClient) });

		await result.current.mutateAsync({
			session,
			targetHarness: "claude-code",
			model: " ",
			idempotencyKey: "switch-request-1",
		});

		expect(postMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/switch-agent",
			{
				params: { path: { sessionId: "sess-1" } },
				body: { targetHarness: "claude-code", idempotencyKey: "switch-request-1" },
			},
		);
		await waitFor(() => {
			expect(invalidate).toHaveBeenCalledWith({ queryKey: ["conversation", "sess-1"] });
			expect(invalidate).toHaveBeenCalledWith({ queryKey: ["conversation-models", "sess-1"] });
			expect(invalidate).toHaveBeenCalledWith({ queryKey: ["conversation-config-options", "sess-1"] });
		});
	});
});
