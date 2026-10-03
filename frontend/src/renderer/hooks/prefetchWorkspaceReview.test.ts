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

	it("warms the default combined diff and the end-of-file contents the review pane waits on", async () => {
		const queryClient = new QueryClient();
		const sessionId = "sess-prefetch-combined";
		await prefetchDefaultWorkspaceReviewDiffs(queryClient, sessionId, workspace([file("README.md")]));

		expect(postMock).toHaveBeenCalledTimes(1);
		const body = postMock.mock.calls[0]?.[1]?.body;
		expect(body).toMatchObject({ scope: "combined", paths: ["README.md"], contextLines: 3, workspaceVersion: "workspace-1" });
		expect(queryClient.getQueryData(sessionWorkspaceDiffsQueryKey(sessionId, "combined", ["README.md"], 3, false, "workspace-1"))).toBeTruthy();
		const endOfFile = queryClient.getQueryCache().findAll({ queryKey: ["files-review-end-of-file", sessionId] });
		expect(endOfFile).toHaveLength(1);
		expect(endOfFile[0]?.state.data).toMatchObject({
			oldFile: { contents: "line3\nline4\nline5\n" },
			newFile: { contents: "line3\nline4\nline5\nadded6\nadded7\n" },
		});

		await prefetchDefaultWorkspaceReviewDiffs(queryClient, sessionId, workspace([file("README.md")]));
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it("warms all combined changes including staged, committed and untracked paths", async () => {
		const files = [file("unstaged.ts"), file("staged.ts"), file("committed.ts"), file("new.ts", { status: "added" }), file("unchanged.ts", { status: "unmodified" })];
		const data = workspace([files[0]!], {
			files,
			sections: { unstaged: [files[0]!], staged: [files[1]!], committed: [files[2]!], untracked: [files[3]!] },
		});
		await prefetchDefaultWorkspaceReviewDiffs(new QueryClient(), "sess-prefetch-mixed", data);
		expect(postMock.mock.calls[0]?.[1]?.body).toMatchObject({ scope: "combined", paths: files.slice(0, 4).map((file) => file.path) });
	});

	it("warms the latest commit when there are no combined changes", async () => {
		const files = [{ ...file("README.md"), editable: false, fileFingerprint: "commit:README.md" }];
		const data = workspace([], { commits: [{ sha: "commit-1", subject: "Change", author: "Ada", timestamp: "2026-10-03T00:00:00Z", files }] });
		await prefetchDefaultWorkspaceReviewDiffs(new QueryClient(), "sess-prefetch-commit", data);
		expect(postMock.mock.calls[0]?.[1]?.body).toMatchObject({ scope: "committed", commitSha: "commit-1", paths: ["README.md"] });
	});

	it("warms matching 24-file batches and refills an evicted later batch", async () => {
		const client = new QueryClient();
		const sessionId = "sess-prefetch-batches";
		const files = Array.from({ length: 57 }, (_, index) => file(`file-${index}.ts`));
		await prefetchDefaultWorkspaceReviewDiffs(client, sessionId, workspace(files));
		expect(postMock.mock.calls.map((call) => call[1].body.paths.length)).toEqual([24, 24, 9]);
		client.removeQueries({ queryKey: sessionWorkspaceDiffsQueryKey(sessionId, "combined", files.slice(24, 48).map((file) => file.path), 3, false, "workspace-1"), exact: true });
		await prefetchDefaultWorkspaceReviewDiffs(client, sessionId, workspace(files));
		expect(postMock).toHaveBeenCalledTimes(4);
		expect(postMock.mock.calls[3]?.[1]?.body.paths).toEqual(files.slice(24, 48).map((file) => file.path));
	});

	it("refetches invalidated diffs and full contents even when the summary version and patch stay the same", async () => {
		const client = new QueryClient();
		const sessionId = "sess-prefetch-invalidated";
		const data = workspace([file("README.md")]);
		await prefetchDefaultWorkspaceReviewDiffs(client, sessionId, data);
		await client.invalidateQueries({ queryKey: ["session-workspace-diffs", sessionId], refetchType: "none" });
		await client.invalidateQueries({ queryKey: ["files-review-end-of-file", sessionId], refetchType: "none" });
		getMock.mockImplementation(async (_path: string, init: { params: { query: { side: string } } }) => ({ data: {
			binary: false, truncated: false, content: "updated unchanged context", revision: `updated-${init.params.query.side}`,
		} }));
		await prefetchDefaultWorkspaceReviewDiffs(client, sessionId, data);
		expect(postMock).toHaveBeenCalledTimes(2);
		expect(getMock).toHaveBeenCalledTimes(4);
		const cached = client.getQueryCache().findAll({ queryKey: ["files-review-end-of-file", sessionId] });
		expect(cached[0]?.state.data).toMatchObject({ newFile: { contents: "updated unchanged context" } });
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
