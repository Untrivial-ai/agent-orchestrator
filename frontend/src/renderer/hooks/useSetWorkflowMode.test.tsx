import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";

const { patchMock } = vi.hoisted(() => ({
	patchMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { PATCH: patchMock },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

import { useSetWorkflowMode } from "./useSetWorkflowMode";
import { workspaceQueryKey } from "./useWorkspaceQuery";

const session: WorkspaceSession = {
	id: "sess-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "opencode",
	branch: "ao/sess-1",
	status: "working",
	kanbanColumn: "building",
	workflowMode: "planning",
	updatedAt: "2026-06-10T00:00:00Z",
	prs: [],
};

const workspaces: WorkspaceSummary[] = [
	{ id: "proj-1", kind: "single_repo", name: "my-app", path: "/repos/my-app", sessions: [session] },
];

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

function newQueryClient() {
	const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
	queryClient.setQueryData(workspaceQueryKey, workspaces);
	return queryClient;
}

beforeEach(() => {
	patchMock.mockReset().mockResolvedValue({ data: { workflowMode: "building" }, error: undefined, response: { status: 200 } });
});

describe("useSetWorkflowMode", () => {
	it("persists the toggled stage through the workflow-mode route", async () => {
		const { result } = renderHook(() => useSetWorkflowMode(), { wrapper: wrapper(newQueryClient()) });

		await act(async () => result.current.mutateAsync({ sessionId: "sess-1", workflowMode: "building" }));

		expect(patchMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workflow-mode", {
			params: { path: { sessionId: "sess-1" } },
			body: { workflowMode: "building" },
		});
	});

	it("moves the cached board card to the new stage before the round trip", async () => {
		const queryClient = newQueryClient();
		const { result } = renderHook(() => useSetWorkflowMode(), { wrapper: wrapper(queryClient) });

		result.current.mutate({ sessionId: "sess-1", workflowMode: "building" });

		await waitFor(() => {
			const cached = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
			expect(cached?.[0]?.sessions[0]?.workflowMode).toBe("building");
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
	});

	it("restores the previous stage when the write fails", async () => {
		patchMock.mockResolvedValue({ data: undefined, error: { message: "nope" }, response: { status: 500 } });
		const queryClient = newQueryClient();
		const { result } = renderHook(() => useSetWorkflowMode(), { wrapper: wrapper(queryClient) });

		result.current.mutate({ sessionId: "sess-1", workflowMode: "building" });

		await waitFor(() => expect(result.current.isError).toBe(true));
		const cached = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
		expect(cached?.[0]?.sessions[0]?.workflowMode).toBe("planning");
	});
});
