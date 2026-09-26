import { describe, expect, it } from "vitest";
import {
	sessionSourceFileQueryOptions,
	sessionSourceFileRevisionQueryOptions,
	sessionSourceFilesQueryOptions,
	workspaceFilesRefetchInterval,
} from "./useSessionWorkspaceFiles";

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
});
