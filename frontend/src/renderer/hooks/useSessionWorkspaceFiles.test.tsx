import { renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

import {
	sessionWorkspaceFilesQueryKey,
	useSessionWorkspaceChangedFiles,
	workspaceFilesRefetchInterval,
	type WorkspaceFilesResponse,
	type WorkspaceFileSummary,
} from "./useSessionWorkspaceFiles";

describe("workspaceFilesRefetchInterval", () => {
	it("polls only while workspace SSE is degraded", () => {
		expect(workspaceFilesRefetchInterval("connecting")).toBe(false);
		expect(workspaceFilesRefetchInterval("connected")).toBe(false);
		expect(workspaceFilesRefetchInterval("degraded")).toBe(30_000);
	});
});

function file(path: string, status: WorkspaceFileSummary["status"]): WorkspaceFileSummary {
	return { path, status, additions: 1, deletions: 0 } as WorkspaceFileSummary;
}

function filesResponse(files: WorkspaceFileSummary[]): WorkspaceFilesResponse {
	return {
		sessionId: "session-1",
		files,
		truncated: false,
		sections: { staged: [], unstaged: [], untracked: [], committed: [] },
		commits: [],
		summary: { files: files.length, additions: 0, deletions: 0 },
	} as WorkspaceFilesResponse;
}

describe("useSessionWorkspaceChangedFiles", () => {
	let queryClient: QueryClient;

	beforeEach(() => {
		getMock.mockReset();
		queryClient = new QueryClient();
	});

	function wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	}

	// The whole point of the passive reader: it never triggers its own request, so
	// rendering a turn card cannot re-open the workspace query for a session that
	// deliberately keeps it off (browser-only). With nothing cached it returns [].
	it("returns an empty list and never fetches when the cache is cold", () => {
		const { result } = renderHook(() => useSessionWorkspaceChangedFiles("session-1"), { wrapper });
		expect(result.current).toEqual([]);
		expect(getMock).not.toHaveBeenCalled();
	});

	it("returns only changed files from the warm cache, dropping unmodified entries", () => {
		queryClient.setQueryData(
			sessionWorkspaceFilesQueryKey("session-1"),
			filesResponse([
				file("alpha/workspace-test.txt", "added"),
				file("alpha/untouched.ts", "unmodified"),
				file("beta/changed.ts", "modified"),
			]),
		);
		const { result } = renderHook(() => useSessionWorkspaceChangedFiles("session-1"), { wrapper });
		expect(result.current.map((entry) => entry.path)).toEqual([
			"alpha/workspace-test.txt",
			"beta/changed.ts",
		]);
		expect(getMock).not.toHaveBeenCalled();
	});

	it("returns an empty list for an undefined session", () => {
		const { result } = renderHook(() => useSessionWorkspaceChangedFiles(undefined), { wrapper });
		expect(result.current).toEqual([]);
		expect(getMock).not.toHaveBeenCalled();
	});
});
