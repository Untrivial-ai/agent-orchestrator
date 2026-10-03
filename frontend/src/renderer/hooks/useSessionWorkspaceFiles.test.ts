import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	sessionWorkspaceFileQueryOptions,
	sessionWorkspaceFilesQueryOptions,
	sessionWorkspaceSearchQueryOptions,
	sessionSourceFileQueryOptions,
	sessionSourceFileRevisionQueryOptions,
	sessionSourceFilesQueryOptions,
	updateSessionWorkspaceFile,
	workspaceFilesRefetchInterval,
} from "./useSessionWorkspaceFiles";

const { localGet, localPut, hostAGet, hostAPut, hostBGet, hostBPut } = vi.hoisted(() => ({
	localGet: vi.fn(), localPut: vi.fn(), hostAGet: vi.fn(), hostAPut: vi.fn(), hostBGet: vi.fn(), hostBPut: vi.fn(),
}));

vi.mock("../lib/host-clients", () => ({
	clientForSessionHost: (hostId?: string) => {
		if (hostId === "host-a") return { GET: hostAGet, PUT: hostAPut };
		if (hostId === "host-b") return { GET: hostBGet, PUT: hostBPut };
		if (hostId) throw new Error(`Host ${hostId} is not connected`);
		return { GET: localGet, PUT: localPut };
	},
}));

beforeEach(() => {
	vi.clearAllMocks();
});

describe("workspaceFilesRefetchInterval", () => {
	it("polls only while workspace SSE is degraded", () => {
		expect(workspaceFilesRefetchInterval("connecting")).toBe(false);
		expect(workspaceFilesRefetchInterval("connected")).toBe(false);
		expect(workspaceFilesRefetchInterval("degraded")).toBe(30_000);
	});

	it("polls while Git-state enrichment is degraded", () => {
		expect(workspaceFilesRefetchInterval("connected", true)).toBe(30_000);
	});
});

describe("pull request file query keys", () => {
	const source = { kind: "pull_request", number: 42, url: "https://example.test/pull/42", label: "PR #42", snapshot: "head-2" } as const;

	it("isolates list, detail, and revision caches by snapshot", () => {
		expect(sessionSourceFilesQueryOptions("session-1", source).queryKey).toContain("head-2");
		expect(sessionSourceFileQueryOptions("session-1", source, "README.md").queryKey).toContain("head-2");
		expect(sessionSourceFileRevisionQueryOptions({ path: "README.md", scope: "combined", sessionId: "session-1", side: "after", source }).queryKey).toContain("head-2");
	});

	it("isolates one commit's detail and revision caches from the whole pull request", () => {
		const wholeDetail = sessionSourceFileQueryOptions("session-1", source, "README.md").queryKey;
		const commitDetail = sessionSourceFileQueryOptions("session-1", source, "README.md", undefined, "combined", "abc123").queryKey;
		expect(commitDetail).toContain("abc123");
		expect(commitDetail).not.toEqual(wholeDetail);
		const wholeRevision = sessionSourceFileRevisionQueryOptions({ path: "README.md", scope: "combined", sessionId: "session-1", side: "after", source }).queryKey;
		const commitRevision = sessionSourceFileRevisionQueryOptions({ commitSha: "abc123", path: "README.md", scope: "combined", sessionId: "session-1", side: "after", source }).queryKey;
		expect(commitRevision).toContain("abc123");
		expect(commitRevision).not.toEqual(wholeRevision);
	});
});

describe("workspace files on selected hosts", () => {
	it("keys path searches by host and limit and forwards cancellation", async () => {
		localGet.mockResolvedValue({ data: { results: [], truncated: false } });
		const controller = new AbortController();
		const search = sessionWorkspaceSearchQueryOptions("same-id", "app", "failed", undefined, 20);
		expect(search.queryKey).toEqual(["session-workspace-search", "same-id", "app", 20]);
		await search.queryFn({ signal: controller.signal });
		expect(localGet).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/workspace/search",
			expect.objectContaining({
				params: { path: { sessionId: "same-id" }, query: { query: "app", limit: 20 } },
				signal: controller.signal,
			}),
		);
	});

	it("separates local, A, and B with the same session ID and reads from the selected host", async () => {
		const local = sessionWorkspaceFileQueryOptions("same-id", "src/app.ts");
		const a = sessionWorkspaceFileQueryOptions("same-id", "src/app.ts", "read failed", "combined", undefined, "host-a");
		const b = sessionWorkspaceFileQueryOptions("same-id", "src/app.ts", "read failed", "combined", undefined, "host-b");
		expect(new Set([JSON.stringify(local.queryKey), JSON.stringify(a.queryKey), JSON.stringify(b.queryKey)]).size).toBe(3);
		expect(sessionWorkspaceFilesQueryOptions("same-id", "load failed", "host-a").queryKey).toEqual(["session-workspace-files", "host-a", "same-id"]);
		expect(sessionWorkspaceFilesQueryOptions("same-id", "load failed", "host-b").queryKey).toEqual(["session-workspace-files", "host-b", "same-id"]);
		localGet.mockResolvedValue({ data: { path: "src/app.ts", content: "local" } });
		hostAGet.mockResolvedValue({ data: { path: "src/app.ts", content: "A" } });
		hostBGet.mockResolvedValue({ data: { path: "src/app.ts", content: "B" } });
		expect((await local.queryFn()).content).toBe("local");
		expect((await a.queryFn()).content).toBe("A");
		expect((await b.queryFn()).content).toBe("B");
		expect(localGet).toHaveBeenCalledTimes(1);
		expect(hostAGet).toHaveBeenCalledTimes(1);
		expect(hostBGet).toHaveBeenCalledTimes(1);
	});

	it("saves to A or B, never to local, and fails closed when the host is offline", async () => {
		hostAPut.mockResolvedValue({ data: { path: "src/app.ts", content: "A edit" } });
		hostBPut.mockResolvedValue({ data: { path: "src/app.ts", content: "B edit" } });
		const payload = { sessionId: "same-id", path: "src/app.ts", content: "edit", expectedFileFingerprint: "v1" };
		await updateSessionWorkspaceFile({ ...payload, hostId: "host-a" });
		await updateSessionWorkspaceFile({ ...payload, hostId: "host-b" });
		expect(hostAPut).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/file", expect.objectContaining({ params: { path: { sessionId: "same-id" } } }));
		expect(hostBPut).toHaveBeenCalledTimes(1);
		await expect(updateSessionWorkspaceFile({ ...payload, hostId: "offline" })).rejects.toThrow("not connected");
		await expect(sessionWorkspaceFileQueryOptions("same-id", "src/app.ts", "read failed", "combined", undefined, "offline").queryFn()).rejects.toThrow("not connected");
		expect(localPut).not.toHaveBeenCalled();
		expect(localGet).not.toHaveBeenCalled();
	});
});
