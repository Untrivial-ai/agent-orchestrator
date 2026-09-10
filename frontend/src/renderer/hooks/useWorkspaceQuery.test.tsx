import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { WorkspaceSummary } from "../types/workspace";

const { captureRendererEventMock, cloudState, getMock, hasTrustedApiBaseUrlMock, listProjectsMock, setQueryHealthyMock } = vi.hoisted(
	() => ({
		captureRendererEventMock: vi.fn().mockResolvedValue(undefined),
		cloudState: { ready: false, org: undefined as { id: string } | undefined },
		getMock: vi.fn(),
		hasTrustedApiBaseUrlMock: vi.fn(() => true),
		listProjectsMock: vi.fn(),
		setQueryHealthyMock: vi.fn(),
	}),
);

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: captureRendererEventMock }));
vi.mock("../lib/agent-switch-visibility", () => ({ agentSwitchVisibility: { setQueryHealthy: setQueryHealthyMock } }));

vi.mock("./useCloudCp", () => ({
	useCloudCp: () => ({
		client: { listProjects: listProjectsMock },
		ready: cloudState.ready,
		baseUrl: "https://cp.example.com",
	}),
}));

vi.mock("./useCloudOrg", () => ({
	useCloudOrg: () => ({ org: cloudState.org, isLoading: false, error: undefined, ready: cloudState.ready }),
}));

import { useWorkspaceQuery, useWorkspaceSession, useWorkspaceTraySessions, workspaceQueryKey } from "./useWorkspaceQuery";

function wrapper({ children }: { children: ReactNode }) {
	// The hook pins its own retry policy; retryDelay 0 keeps the error tests fast.
	const queryClient = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function respondWith(payload: {
	projects?: { data?: unknown; error?: unknown };
	sessions?: { data?: unknown; error?: unknown };
}) {
	getMock.mockImplementation(async (url: string) => {
		if (url === "/api/v1/projects") return payload.projects ?? { data: { projects: [] }, error: undefined };
		if (url === "/api/v1/sessions") return payload.sessions ?? { data: { sessions: [] }, error: undefined };
		throw new Error(`unexpected GET ${url}`);
	});
}

beforeEach(() => {
	captureRendererEventMock.mockClear();
	getMock.mockReset();
	hasTrustedApiBaseUrlMock.mockReset().mockReturnValue(true);
	cloudState.ready = false;
	cloudState.org = undefined;
	listProjectsMock.mockReset();
	setQueryHealthyMock.mockReset();
});

describe("useWorkspaceQuery", () => {
	it("rejects workspace reads while the daemon base URL is untrusted", async () => {
		hasTrustedApiBaseUrlMock.mockReturnValue(false);

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });

		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.error).toEqual(new Error("AO daemon API is not ready"));
		expect(getMock).not.toHaveBeenCalled();
	});

	it("maps projects and their sessions, applying provider/status/title fallbacks", async () => {
		respondWith({
			projects: {
				data: {
					projects: [
						{
							id: "proj-1",
							name: "my-app",
							path: "/home/me/my-app",
							orchestratorAgent: "codex",
						},
					],
				},
				error: undefined,
			},
			sessions: {
				data: {
					sessions: [
						{
							id: "sess-1",
							projectId: "proj-1",
							terminalHandleId: "term-1",
							terminalGeneration: "launch-2",
							displayName: "fix-bug",
							issueId: "github:acme/project-one#42",
							harness: "claude-code",
							reviewerHarness: "qwen",
							branch: "qa/modal-worker",
							status: "mergeable",
							scmStatus: "review_pending",
							kanbanColumn: "ready",
							displayStatus: "Mergeable",
							isTerminated: false,
							autoInjectReview: false,
							autoInjectCI: false,
							activity: { state: "idle", lastActivityAt: "2026-06-10T15:30:00Z" },
							activeAgentSwitch: {
								agentHandoffStatus: "received",
								errorCode: "delivery_unconfirmed",
								fromHarness: "claude-code",
								id: "switch-1",
								privateFutureField: "must-not-leak",
								requestedAt: "2026-06-10T15:31:00Z",
								semanticHandoffIncluded: true,
								sessionId: "sess-1",
								sourceTranscriptStatus: "available",
								state: "delivering_context",
								targetHarness: "codex",
								targetStartMode: "resumed",
								updatedAt: "2026-06-10T15:32:00Z",
							},
							lastUserMessageAt: "2026-06-10T16:10:00Z",
							updatedAt: "2026-06-10T16:15:04Z",
						},
						{
							// Unknown harness/status and no displayName/issueId: falls back
							// to codex / unknown / the session id.
							id: "sess-2",
							projectId: "proj-1",
							harness: "mystery-agent",
							reviewerHarness: "mystery-reviewer",
							status: "bogus",
							isTerminated: false,
							updatedAt: "2026-06-10T16:15:04Z",
						},
						// Belongs to another project; must not leak into proj-1.
						{ id: "sess-3", projectId: "proj-2", isTerminated: false, updatedAt: "2026-06-10T16:15:04Z" },
					],
				},
				error: undefined,
			},
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		const [workspace] = result.current.data ?? [];
		expect(workspace).toMatchObject({
			id: "proj-1",
			name: "my-app",
			path: "/home/me/my-app",
			orchestratorAgent: "codex",
		});
		expect(workspace.sessions).toHaveLength(2);
		expect(workspace.sessions[0]).toMatchObject({
			id: "sess-1",
			terminalHandleId: "term-1",
			terminalGeneration: "launch-2",
			title: "fix-bug",
			issueId: "github:acme/project-one#42",
			provider: "claude-code",
			reviewerHarness: "qwen",
			branch: "qa/modal-worker",
			status: "mergeable",
			scmStatus: "review_pending",
			kanbanColumn: "ready",
			displayStatus: "Mergeable",
			activity: { state: "idle", lastActivityAt: "2026-06-10T15:30:00Z" },
			lastUserMessageAt: "2026-06-10T16:10:00Z",
			autoInjectReview: false,
			autoInjectCI: false,
		});
		expect(workspace.sessions[0].activeAgentSwitch).toEqual({
			agentHandoffStatus: "received",
			errorCode: "delivery_unconfirmed",
			fromHarness: "claude-code",
			id: "switch-1",
			state: "delivering_context",
			targetHarness: "codex",
			updatedAt: "2026-06-10T15:32:00Z",
		});
		expect(workspace.sessions[1]).toMatchObject({
			id: "sess-2",
			title: "sess-2",
			provider: "codex",
			reviewerHarness: undefined,
			status: "unknown",
			branch: undefined,
			autoInjectReview: true,
			autoInjectCI: true,
		});
		expect(captureRendererEventMock).toHaveBeenCalledWith("ao.renderer.session_state_unknown", {
			field: "status",
			reason: "unrecognized",
		});
		expect(captureRendererEventMock).toHaveBeenCalledWith("ao.renderer.session_state_unknown", {
			field: "activity",
			reason: "missing",
		});
	});

	it("preserves scratch projects and leaves branchless scratch sessions branchless", async () => {
		respondWith({
			projects: {
				data: {
					projects: [
						{
							id: "scratch",
							name: "Scratch",
							kind: "scratch",
							path: "/home/me/.ao/scratch/default",
						},
					],
				},
				error: undefined,
			},
			sessions: {
				data: {
					sessions: [
						{
							id: "scratch-worker-1",
							projectId: "scratch",
							harness: "codex",
							status: "working",
							isTerminated: false,
							updatedAt: "2026-06-10T16:15:04Z",
						},
					],
				},
				error: undefined,
			},
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(result.current.data?.[0]).toMatchObject({
			id: "scratch",
			kind: "scratch",
		});
		expect(result.current.data?.[0].sessions[0]).toMatchObject({
			id: "scratch-worker-1",
			branch: undefined,
		});
	});

	it("falls back to the direct session read while the workspace list has not caught up", async () => {
		getMock.mockImplementation(async (url: string, options?: { params?: { path?: { sessionId?: string } } }) => {
			if (url === "/api/v1/projects") {
				return {
					data: {
						projects: [{ id: "proj-1", name: "workspace3", path: "/tmp/workspace3", orchestratorAgent: "codex" }],
					},
					error: undefined,
				};
			}
			if (url === "/api/v1/sessions") {
				return { data: { sessions: [] }, error: undefined };
			}
			if (url === "/api/v1/sessions/{sessionId}") {
				expect(options?.params?.path?.sessionId).toBe("sess-orch");
				return {
					data: {
						session: {
							id: "sess-orch",
							projectId: "proj-1",
							displayName: "orchestrate",
							harness: "codex",
							kind: "orchestrator",
							mode: "tui",
							status: "working",
							kanbanColumn: "building",
							displayStatus: "Working",
							autoInjectReview: true,
							autoInjectCI: true,
							autoReviewEnabled: false,
							isPinned: false,
							isTerminated: false,
							terminateOnPrMerge: false,
							prs: [],
							activity: { state: "idle", lastActivityAt: "2026-09-04T10:00:00Z" },
							createdAt: "2026-09-04T10:00:00Z",
							updatedAt: "2026-09-04T10:00:01Z",
						},
					},
					error: undefined,
				};
			}
			throw new Error(`unexpected GET ${url}`);
		});

		const queryClient = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
		const localWrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);

		const { result } = renderHook(() => useWorkspaceSession("sess-orch"), { wrapper: localWrapper });

		await waitFor(() => expect(result.current.data?.id).toBe("sess-orch"));
		expect(result.current.data).toMatchObject({
			id: "sess-orch",
			workspaceId: "proj-1",
			workspaceName: "workspace3",
			title: "orchestrate",
			provider: "codex",
			kind: "orchestrator",
		});
		await waitFor(() => {
			const cached = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
			expect(Array.isArray(cached)).toBe(true);
			expect(cached?.[0]?.sessions.some((session: { id: string }) => session.id === "sess-orch")).toBe(true);
		});
	});

	it("maps each session's prs straight from the session list", async () => {
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
			sessions: {
				data: {
					sessions: [
						{
							id: "sess-1",
							projectId: "proj-1",
							status: "pr_open",
							isTerminated: false,
							updatedAt: "2026-06-10T16:15:04Z",
							prs: [
								{
									number: 278,
									state: "open",
									url: "u",
									ci: "passing",
									review: "approved",
									mergeability: "clean",
									reviewComments: false,
									updatedAt: "2026-06-10T16:15:04Z",
								},
							],
						},
						{
							id: "sess-2",
							projectId: "proj-1",
							status: "working",
							isTerminated: false,
							updatedAt: "2026-06-10T16:15:04Z",
						},
					],
				},
				error: undefined,
			},
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		const sessions = result.current.data?.[0].sessions ?? [];
		expect(sessions[0].prs).toEqual([
			{
				number: 278,
				state: "open",
				url: "u",
				ci: "passing",
				review: "approved",
				mergeability: "clean",
				reviewComments: false,
				updatedAt: "2026-06-10T16:15:04Z",
			},
		]);
		// A session with no PRs maps to an empty stack, so the empty states render.
		expect(sessions[1].prs).toEqual([]);
	});

	it("preserves backend merged status for terminated merged sessions", async () => {
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
			sessions: {
				data: {
					sessions: [
						{
							id: "sess-1",
							projectId: "proj-1",
							status: "merged",
							isTerminated: true,
							updatedAt: "2026-06-10T16:15:04Z",
						},
					],
				},
				error: undefined,
			},
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(result.current.data?.[0].sessions[0].status).toBe("merged");
		expect(result.current.data?.[0].sessions[0].isTerminated).toBe(true);
	});

	it("falls back to terminated for terminated sessions without a known backend status", async () => {
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
			sessions: {
				data: {
					sessions: [
						{
							id: "sess-1",
							projectId: "proj-1",
							status: "bogus",
							isTerminated: true,
							updatedAt: "2026-06-10T16:15:04Z",
						},
					],
				},
				error: undefined,
			},
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(result.current.data?.[0].sessions[0].status).toBe("terminated");
		expect(result.current.data?.[0].sessions[0].isTerminated).toBe(true);
	});

	it("surfaces a projects fetch error", async () => {
		const failure = new TypeError("Failed to fetch");
		respondWith({ projects: { data: undefined, error: failure } });

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });

		await waitFor(() => expect(result.current.isError).toBe(true), { timeout: 3_000 });
		expect(result.current.error).toBe(failure);
		expect(setQueryHealthyMock).toHaveBeenCalledWith("history", false, "workspaces");
	});

	it("surfaces a sessions fetch error even when projects load", async () => {
		const failure = new Error("sessions backend down");
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
			sessions: { data: undefined, error: failure },
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });

		await waitFor(() => expect(result.current.isError).toBe(true), { timeout: 3_000 });
		expect(result.current.error).toBe(failure);
	});

	it("merges control-plane projects after local ones with kind cloud", async () => {
		cloudState.ready = true;
		cloudState.org = { id: "org-1" };
		listProjectsMock.mockResolvedValue({
			items: [
				{
					id: "cp-1",
					orgId: "org-1",
					displayName: "cloud-app",
					repositoryUrl: "https://github.com/acme/cloud-app",
					defaultBranch: "main",
					config: {},
					createdAt: "2026-08-01T00:00:00Z",
					updatedAt: "2026-08-01T00:00:00Z",
				},
			],
			page: { hasMore: false },
		});
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.data).toHaveLength(2));

		expect(result.current.data?.[0]).toMatchObject({ id: "proj-1", name: "my-app", path: "/p" });
		expect(result.current.data?.[1]).toEqual({
			id: "cp-1",
			name: "cloud-app",
			kind: "cloud",
			path: "",
			sessions: [],
		});
		expect(listProjectsMock).toHaveBeenCalledWith("org-1", { limit: 100 });
	});

	it("keeps local projects when the cloud fetch fails", async () => {
		cloudState.ready = true;
		cloudState.org = { id: "org-1" };
		listProjectsMock.mockRejectedValue(new Error("control plane down"));
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		await waitFor(() => expect(listProjectsMock).toHaveBeenCalled());

		expect(result.current.data).toHaveLength(1);
		expect(result.current.data?.[0]).toMatchObject({ id: "proj-1" });
		expect(result.current.isError).toBe(false);
	});

	it("does not call the control plane while cloud is not ready", async () => {
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
		});

		const { result } = renderHook(() => useWorkspaceQuery(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(listProjectsMock).not.toHaveBeenCalled();
	});

	it("selects only attention-worthy worker sessions for the always-mounted tray", async () => {
		respondWith({
			projects: { data: { projects: [{ id: "proj-1", name: "my-app", path: "/p" }] }, error: undefined },
			sessions: {
				data: {
					sessions: [
						{ id: "needs-input", projectId: "proj-1", displayName: "Needs input", harness: "codex", status: "needs_input", updatedAt: "2026-08-01T00:00:00Z" },
						{ id: "mergeable", projectId: "proj-1", displayName: "Mergeable", harness: "codex", status: "mergeable", updatedAt: "2026-08-01T00:00:00Z" },
						{ id: "working", projectId: "proj-1", displayName: "Working", harness: "codex", status: "working", updatedAt: "2026-08-01T00:00:00Z" },
						{ id: "merged", projectId: "proj-1", displayName: "Merged", harness: "codex", status: "merged", updatedAt: "2026-08-01T00:00:00Z" },
						{ id: "orchestrator", projectId: "proj-1", displayName: "Orchestrator", harness: "codex", kind: "orchestrator", status: "needs_input", updatedAt: "2026-08-01T00:00:00Z" },
					],
				},
				error: undefined,
			},
		});

		const { result } = renderHook(() => useWorkspaceTraySessions(), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([
			{ projectId: "proj-1", projectName: "my-app", sessionId: "needs-input", title: "Needs input", zone: "action" },
			{ projectId: "proj-1", projectName: "my-app", sessionId: "mergeable", title: "Mergeable", zone: "merge" },
		]);
	});
});
