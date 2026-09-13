import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, test, vi } from "vitest";
import type { CloudCpSessionChild } from "../lib/cloud-cp";
import type { WorkspaceSession } from "../types/workspace";

const { listSessionChildren, previewMode } = vi.hoisted(() => ({
	listSessionChildren: vi.fn(),
	previewMode: { value: false },
}));

vi.mock("./useCloudCp", () => ({
	useCloudCp: () => ({
		client: { listSessionChildren },
		ready: true,
		baseUrl: "https://cp.test",
	}),
}));
vi.mock("./useCloudOrg", () => ({
	useCloudOrg: () => ({ org: { id: "org-1" }, ready: true }),
}));
vi.mock("../lib/preview-mode", () => ({
	get usesPreviewWorkspaceData() {
		return previewMode.value;
	},
}));

import { useOrchestratorChildren } from "./useOrchestratorChildren";

const child = (overrides: Partial<CloudCpSessionChild>): CloudCpSessionChild => ({
	id: "11111111-1111-4111-8111-111111111111",
	orgId: "org-1",
	projectId: "project-1",
	kind: "worker",
	harness: "claude-code",
	displayName: "Fix CI",
	branch: "ao/11111111",
	mode: "trusted",
	deniedCommands: [],
	activityState: "active",
	status: "working",
	runtimeConnected: true,
	isTerminated: false,
	createdAt: "2026-08-30T10:00:00Z",
	updatedAt: "2026-08-30T12:00:00Z",
	prs: [],
	...overrides,
});

const session = (overrides: Partial<WorkspaceSession>): WorkspaceSession =>
	({
		id: "orch-1",
		terminalHandleId: "orch-1",
		workspaceId: "project-1",
		workspaceName: "demo",
		title: "Orchestrator",
		provider: "claude-code",
		kind: "orchestrator",
		status: "idle",
		isTerminated: false,
		createdAt: "2026-08-30T10:00:00Z",
		updatedAt: "2026-08-30T10:00:00Z",
		activity: null,
		prs: [],
		cloud: { orgId: "org-1" },
		...overrides,
	}) as WorkspaceSession;

function wrapper({ children }: { children: ReactNode }) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe("useOrchestratorChildren", () => {
	beforeEach(() => {
		listSessionChildren.mockReset();
		previewMode.value = false;
	});

	test("maps children with pull requests and sorts terminated last", async () => {
		listSessionChildren.mockResolvedValue({
			items: [
				child({
					id: "22222222-2222-4222-8222-222222222222",
					displayName: "Done worker",
					status: "merged",
					isTerminated: true,
				}),
				child({
					prs: [
						{
							url: "https://github.com/o/r/pull/42",
							number: 42,
							state: "open",
							ci: "failing",
							review: "none",
							mergeability: "unstable",
							reviewComments: false,
							updatedAt: "2026-08-30T12:00:00Z",
						},
					],
				}),
			],
			page: { hasMore: false },
		});
		const { result } = renderHook(() => useOrchestratorChildren(session({})), { wrapper });
		await waitFor(() => expect(result.current.data).toBeDefined());
		const children = result.current.data!;
		expect(children).toHaveLength(2);
		// Live worker first, terminated history last.
		expect(children[0].title).toBe("Fix CI");
		expect(children[1].isTerminated).toBe(true);
		expect(children[0].prs).toEqual([
			expect.objectContaining({ number: 42, state: "open", ci: "failing" }),
		]);
		expect(listSessionChildren).toHaveBeenCalledWith("org-1", "orch-1", { limit: 100 });
	});

	test("disabled for local sessions and non-orchestrators", async () => {
		const local = renderHook(() => useOrchestratorChildren(session({ cloud: undefined })), { wrapper });
		const worker = renderHook(() => useOrchestratorChildren(session({ kind: "worker" })), { wrapper });
		await Promise.resolve();
		expect(local.result.current.fetchStatus).toBe("idle");
		expect(worker.result.current.fetchStatus).toBe("idle");
		expect(listSessionChildren).not.toHaveBeenCalled();
	});

	test("preview mode serves mock children without a control plane", async () => {
		previewMode.value = true;
		const { result } = renderHook(
			() => useOrchestratorChildren(session({ id: "ao-demo-orchestrator", cloud: undefined })),
			{ wrapper },
		);
		await waitFor(() => expect(result.current.data).toBeDefined());
		expect(result.current.data!.length).toBeGreaterThan(0);
		expect(listSessionChildren).not.toHaveBeenCalled();
	});
});
