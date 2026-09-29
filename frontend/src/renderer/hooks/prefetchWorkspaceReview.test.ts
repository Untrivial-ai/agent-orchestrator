import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceFilesResponse, WorkspaceFileSummary } from "./useSessionWorkspaceFiles";
import { prefetchDefaultWorkspaceReviewDiffs, sessionWorkspaceDiffsQueryKey } from "./useSessionWorkspaceFiles";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock, GET: getMock },
	apiErrorMessage: (_error: unknown, fallback = "Request failed") => fallback,
}));

function file(path: string, overrides: Partial<WorkspaceFileSummary> = {}): WorkspaceFileSummary {
	return { path, status: "modified", additions: 1, deletions: 1, size: 40, binary: false, fileFingerprint: `fp:${path}`, ...overrides };
}

function workspace(unstaged: WorkspaceFileSummary[], extra: Partial<WorkspaceFilesResponse> = {}): WorkspaceFilesResponse {
	return {
		sessionId: "sess-1",
		workspaceVersion: "workspace-1",
		files: unstaged,
		sections: { committed: [], staged: [], unstaged, untracked: [] },
		commits: [],
		summary: { additions: 1, deletions: 1, files: unstaged.length },
		truncated: false,
		...extra,
	};
}

const eofPatch = [
	"diff --git a/README.md b/README.md",
	"index 1111111..2222222 100644",
	"--- a/README.md",
	"+++ b/README.md",
	"@@ -3,3 +3,5 @@",
	" line3",
	" line4",
	" line5",
	"+added6",
	"+added7",
	"",
].join("\n");

describe("prefetchDefaultWorkspaceReviewDiffs", () => {
	beforeEach(() => {
		postMock.mockReset();
		getMock.mockReset();
		postMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				workspaceVersion: "workspace-1",
				groups: [{ repository: "", patch: eofPatch, truncated: false, includedPaths: ["README.md"], deferred: [], errors: [] }],
			},
		});
		getMock.mockImplementation(async (_path: string, init: { params: { query: { side: "before" | "after" } } }) => ({
			data: {
				binary: false,
				content: init.params.query.side === "before" ? "line3\nline4\nline5\n" : "line3\nline4\nline5\nadded6\nadded7\n",
				exists: true,
				path: "README.md",
				revision: init.params.query.side,
				sessionId: "sess-1",
				side: init.params.query.side,
				size: 20,
				truncated: false,
				workspaceVersion: "workspace-1",
			},
		}));
	});

	it("skips a files summary that has no review sections", async () => {
		const queryClient = new QueryClient();
		await prefetchDefaultWorkspaceReviewDiffs(queryClient, "sess-partial", { sessionId: "sess-partial", files: [file("README.md")] } as WorkspaceFilesResponse);
		expect(postMock).not.toHaveBeenCalled();
	});

	it("warms the default unstaged diff and the end-of-file contents the review pane waits on", async () => {
		const queryClient = new QueryClient();
		const sessionId = "sess-prefetch-unstaged";
		await prefetchDefaultWorkspaceReviewDiffs(queryClient, sessionId, workspace([file("README.md")]));

		expect(postMock).toHaveBeenCalledTimes(1);
		const body = postMock.mock.calls[0]?.[1]?.body;
		expect(body).toMatchObject({ scope: "unstaged", paths: ["README.md"], contextLines: 3, workspaceVersion: "workspace-1" });
		expect(queryClient.getQueryData(sessionWorkspaceDiffsQueryKey(sessionId, "unstaged", ["README.md"], 3, false, "workspace-1"))).toBeTruthy();
		const endOfFile = queryClient.getQueryCache().findAll({ queryKey: ["files-review-end-of-file", sessionId] });
		expect(endOfFile).toHaveLength(1);
		expect(endOfFile[0]?.state.data).toMatchObject({
			oldFile: { contents: "line3\nline4\nline5\n" },
			newFile: { contents: "line3\nline4\nline5\nadded6\nadded7\n" },
		});

		await prefetchDefaultWorkspaceReviewDiffs(queryClient, sessionId, workspace([file("README.md")]));
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it("skips lockfiles the review pane defers", async () => {
		const queryClient = new QueryClient();
		await prefetchDefaultWorkspaceReviewDiffs(queryClient, "sess-prefetch-lock", workspace([file("package-lock.json", { size: 600_000 })]));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("retries after a failed diff fetch", async () => {
		postMock.mockResolvedValueOnce({ error: { message: "unavailable" } });
		const queryClient = new QueryClient();
		const data = workspace([file("README.md")]);
		await prefetchDefaultWorkspaceReviewDiffs(queryClient, "sess-prefetch-retry", data);
		await prefetchDefaultWorkspaceReviewDiffs(queryClient, "sess-prefetch-retry", data);
		expect(postMock).toHaveBeenCalledTimes(2);
	});
});
