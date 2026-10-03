import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";

const { postMock, postMockA, postMockB } = vi.hoisted(() => ({
	postMock: vi.fn(),
	postMockA: vi.fn(),
	postMockB: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: () => "request failed",
}));

vi.mock("../lib/host-clients", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/host-clients")>()),
	clientForSessionHost: (hostId?: string) => ({
		POST: hostId === "host-a" ? postMockA : hostId === "host-b" ? postMockB : postMock,
	}),
}));

import { agentSwitchesQueryKey } from "./useAgentSwitches";
import { conversationQueryKey } from "./useConversation";
import { sessionUiKey } from "../lib/hosts";
import { clearSwitchAgentState, useRecoverAgentSwitch, useSwitchAgent, useSwitchAgentState } from "./useSwitchAgent";

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
	postMockA.mockReset();
	postMockB.mockReset();
});

function acceptedSwitch(id: string) {
	return {
		data: {
			switch: {
				agentHandoffStatus: "not_attempted",
				fromHarness: "codex",
				id,
				state: "preparing_handoff",
				targetHarness: "claude-code",
			},
		},
		error: undefined,
		response: { status: 202 },
	};
}

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
		const removeQueries = vi.spyOn(queryClient, "removeQueries");
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
			expect(removeQueries).toHaveBeenCalledWith({ queryKey: ["conversation-models", "sess-1"] });
			expect(removeQueries).toHaveBeenCalledWith({
				queryKey: ["conversation-config-options", "sess-1"],
			});
			expect(removeQueries).toHaveBeenCalledWith({ queryKey: ["conversation-skills", "sess-1"] });
		});
	});

	it("routes equal session IDs to their owning hosts and isolates cache updates", async () => {
		postMockA.mockResolvedValue(acceptedSwitch("switch-a"));
		postMockB.mockResolvedValue(acceptedSwitch("switch-b"));
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
		const localKey = agentSwitchesQueryKey(session.id);
		const aKey = agentSwitchesQueryKey(session.id, "host-a");
		const bKey = agentSwitchesQueryKey(session.id, "host-b");
		queryClient.setQueryData(localKey, ["local"]);
		queryClient.setQueryData(aKey, ["a"]);
		queryClient.setQueryData(bKey, ["b"]);
		const invalidate = vi.spyOn(queryClient, "invalidateQueries");
		const { result } = renderHook(() => useSwitchAgent(), { wrapper: wrapper(queryClient) });

		await result.current.mutateAsync({ session: { ...session, hostId: "host-a" }, targetHarness: "claude-code", model: "", idempotencyKey: "a" });
		expect(postMockA).toHaveBeenCalledOnce();
		expect(postMockB).not.toHaveBeenCalled();
		expect(postMock).not.toHaveBeenCalled();
		expect(queryClient.getQueryData(aKey)).toEqual([expect.objectContaining({ id: "switch-a" }), "a"]);
		expect(queryClient.getQueryData(bKey)).toEqual(["b"]);
		expect(queryClient.getQueryData(localKey)).toEqual(["local"]);
		expect(invalidate).toHaveBeenCalledWith({ queryKey: conversationQueryKey(session.id, "host-a") });
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["remote-workspaces", "host-a"] });
		expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["workspaces"] });

		await result.current.mutateAsync({ session: { ...session, hostId: "host-b" }, targetHarness: "claude-code", model: "", idempotencyKey: "b" });
		expect(postMockB).toHaveBeenCalledOnce();
		expect(queryClient.getQueryData(bKey)).toEqual([expect.objectContaining({ id: "switch-b" }), "b"]);
		expect(queryClient.getQueryData(aKey)).toEqual([expect.objectContaining({ id: "switch-a" }), "a"]);
		expect(queryClient.getQueryData(localKey)).toEqual(["local"]);
	});

	it("keeps pending state and dismissal scoped to the host", async () => {
		postMockA.mockReturnValue(new Promise(() => {}));
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
		const { result } = renderHook(() => ({
			switchAgent: useSwitchAgent(),
			a: useSwitchAgentState(session.id, "host-a"),
			b: useSwitchAgentState(session.id, "host-b"),
			local: useSwitchAgentState(session.id),
			chatSurfaceA: useSwitchAgentState(sessionUiKey(session.id, "host-a")),
		}), { wrapper: wrapper(queryClient) });
		act(() => result.current.switchAgent.mutate({ session: { ...session, hostId: "host-a" }, targetHarness: "claude-code", model: "", idempotencyKey: "a" }));
		await waitFor(() => expect(result.current.a.isPending).toBe(true));
		expect(result.current.b.isPending).toBe(false);
		expect(result.current.local.isPending).toBe(false);
		expect(result.current.chatSurfaceA.isPending).toBe(true);
		clearSwitchAgentState(queryClient, session.id, "host-b");
		expect(result.current.a.isPending).toBe(true);
	});

	it("routes recovery and invalidates only the owning host", async () => {
		postMockA.mockResolvedValue(acceptedSwitch("switch-a"));
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
		const invalidate = vi.spyOn(queryClient, "invalidateQueries");
		const { result } = renderHook(() => useRecoverAgentSwitch(), { wrapper: wrapper(queryClient) });
		await result.current.mutateAsync({ sessionId: session.id, switchId: "switch-a", hostId: "host-a" });
		expect(postMockA).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/agent-switches/{switchId}/recover",
			{ params: { path: { sessionId: session.id, switchId: "switch-a" } } },
		);
		expect(queryClient.getQueryData(agentSwitchesQueryKey(session.id, "host-a"))).toEqual([expect.objectContaining({ id: "switch-a" })]);
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["remote-workspaces", "host-a"] });
		expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["workspaces"] });
	});
});
