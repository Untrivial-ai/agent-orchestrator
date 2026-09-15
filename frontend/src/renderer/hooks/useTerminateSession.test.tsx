import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { settingsQueryKey } from "./useSettings";

const { createCloudClientMock, deleteSessionMock, postMock } = vi.hoisted(() => ({
	createCloudClientMock: vi.fn(),
	deleteSessionMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("./useCloudCp", () => ({
	createRendererCloudCpClient: createCloudClientMock,
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));

import { useTerminateSession } from "./useTerminateSession";
import { workspaceQueryKey } from "./useWorkspaceQuery";

const localSession: WorkspaceSession = {
	id: "session-1",
	workspaceId: "project-1",
	workspaceName: "Project",
	title: "Local worker",
	provider: "claude-code",
	status: "working",
	updatedAt: "2026-09-01T00:00:00Z",
	prs: [],
};

const session = {
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	branch: "ao/sess-1",
	id: "sess-1",
	kanbanColumn: "building",
	kind: "worker",
	provider: "codex",
	prs: [],
	status: "working",
	terminalHandleId: "sess-1-terminal",
	title: "do the thing",
	updatedAt: "2026-06-10T00:00:00Z",
	workspaceId: "proj-1",
	workspaceName: "my-app",
} satisfies WorkspaceSession;

const workspaces: WorkspaceSummary[] = [
	{ id: "proj-1", kind: "single_repo", name: "my-app", path: "/repos/my-app", sessions: [session] },
];

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

function newQueryClient() {
	const queryClient = new QueryClient({
		defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
	});
	queryClient.setQueryData(workspaceQueryKey, workspaces);
	return queryClient;
}

beforeEach(() => {
	createCloudClientMock.mockReset();
	deleteSessionMock.mockReset().mockResolvedValue({ session: { id: "session-1", desiredState: "deleted" } });
	createCloudClientMock.mockReturnValue({ deleteSession: deleteSessionMock });
	postMock.mockReset().mockResolvedValue({ data: { ok: true }, error: undefined });
});

describe("useTerminateSession", () => {
	it("routes local sessions to the local daemon", async () => {
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		const { result } = renderHook(() => useTerminateSession(), { wrapper: wrapper(queryClient) });

		await act(async () => result.current.mutateAsync(localSession));

		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/kill", {
			params: { path: { sessionId: "session-1" } },
		});
		expect(createCloudClientMock).not.toHaveBeenCalled();
	});

	it("routes cloud sessions to their control-plane organization", async () => {
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		queryClient.setQueryData(settingsQueryKey, { cloudControlPlaneUrl: "https://cp.example.com" });
		const cloudSession: WorkspaceSession = { ...localSession, cloud: { orgId: "org-1" } };
		const { result } = renderHook(() => useTerminateSession(), { wrapper: wrapper(queryClient) });

		await act(async () => result.current.mutateAsync(cloudSession));

		expect(createCloudClientMock).toHaveBeenCalledWith("https://cp.example.com");
		expect(deleteSessionMock).toHaveBeenCalledWith("org-1", "session-1");
		expect(postMock).not.toHaveBeenCalled();
	});

	it("fails closed when a cloud session has no configured control plane", async () => {
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		const cloudSession: WorkspaceSession = { ...localSession, cloud: { orgId: "org-1" } };
		const { result } = renderHook(() => useTerminateSession(), { wrapper: wrapper(queryClient) });

		await expect(act(async () => result.current.mutateAsync(cloudSession))).rejects.toThrow(
			"The cloud control plane is not configured.",
		);
		expect(deleteSessionMock).not.toHaveBeenCalled();
		expect(postMock).not.toHaveBeenCalled();
	});

	// The delete control is disabled while the mutation is pending, and a
	// mutation stays pending until its onSuccess settles. Waiting on the
	// workspace refetch there kept the spinner up for an extra round trip after
	// the daemon had already finished the kill.
	it("settles without waiting for the workspace refetch", async () => {
		postMock.mockResolvedValue({ data: { ok: true }, error: undefined, response: { status: 200 } });
		const queryClient = newQueryClient();
		let refetchResolved = false;
		// An observer on the workspace query, as the real board always has: it is
		// what makes the post-kill invalidation actually refetch.
		const { result } = renderHook(
			() => ({
				terminate: useTerminateSession(),
				workspaces: useQuery({
					queryKey: workspaceQueryKey,
					queryFn: async () => {
						await new Promise((resolve) => setTimeout(resolve, 100));
						refetchResolved = true;
						return workspaces;
					},
					initialData: workspaces,
					staleTime: Number.POSITIVE_INFINITY,
				}),
			}),
			{ wrapper: wrapper(queryClient) },
		);

		result.current.terminate.mutate(session);

		// isSuccess flips only once onSuccess has settled, so a refetch that is
		// still in flight here is one the delete control never waited on.
		await waitFor(() => expect(result.current.terminate.isSuccess).toBe(true));
		expect(refetchResolved).toBe(false);
		// The refresh is still requested, just not blocking.
		await waitFor(() => expect(refetchResolved).toBe(true));
	});

	it("marks the killed session terminated in the cached board", async () => {
		postMock.mockResolvedValue({ data: { ok: true }, error: undefined, response: { status: 200 } });
		const queryClient = newQueryClient();
		const { result } = renderHook(() => useTerminateSession(), { wrapper: wrapper(queryClient) });

		result.current.mutate(session);

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		const cached = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
		expect(cached?.[0]?.sessions[0]).toMatchObject({
			id: "sess-1",
			isTerminated: true,
			kanbanColumn: "archive",
			status: "terminated",
		});
	});

	it("leaves the board untouched when the kill fails", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { message: "nope" }, response: { status: 500 } });
		const queryClient = newQueryClient();
		const { result } = renderHook(() => useTerminateSession(), { wrapper: wrapper(queryClient) });

		result.current.mutate(session);

		await waitFor(() => expect(result.current.isError).toBe(true));
		const cached = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
		expect(cached?.[0]?.sessions[0]?.isTerminated).toBeUndefined();
	});
});
