import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { CloudCpClient } from "../lib/cloud-cp";
import {
	cloudWorkspaceReviewDiffsQueryOptions,
	cloudWorkspaceReviewFileQueryOptions,
	cloudWorkspaceReviewQueryKey,
	invalidateCloudWorkspaceReviewEvent,
} from "./useCloudWorkspaceReview";

describe("Cloud workspace review queries", () => {
	it("keys every review request by control plane, tenant, session, scope, commit, and path", () => {
		const client = {} as CloudCpClient;
		const file = cloudWorkspaceReviewFileQueryOptions({
			client, baseUrl: "https://cloud.example", orgId: "org-1", sessionId: "session-1",
			path: "src/App.tsx", scope: "committed", commitSha: "abc",
		});
		const diffs = cloudWorkspaceReviewDiffsQueryOptions({
			client, baseUrl: "https://cloud.example", orgId: "org-1", sessionId: "session-1",
			paths: ["src/App.tsx"], scope: "staged", contextLines: 5, ignoreWhitespace: true,
			workspaceVersion: "v1",
		});
		expect(file.queryKey).toEqual([
			"cloud-workspace-review", "https://cloud.example", "org-1", "session-1", "file", "committed", "abc", "src/App.tsx",
		]);
		expect(diffs.queryKey).toEqual([
			"cloud-workspace-review", "https://cloud.example", "org-1", "session-1", "diffs", "staged", "", ["src/App.tsx"], 5, true, "v1",
		]);
	});

	it("invalidates only the matching Cloud session on workspace.changed", async () => {
		const queryClient = new QueryClient();
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const key = cloudWorkspaceReviewQueryKey("https://cloud.example", "org-1", "session-1");
		await invalidateCloudWorkspaceReviewEvent(queryClient, key, {
			sessionId: "session-1", sequence: 2, type: "workspace.changed", payload: { path: "README.md" }, createdAt: "2026-09-20T00:00:00Z",
		});
		expect(invalidate).toHaveBeenCalledWith({ queryKey: key });
		invalidate.mockClear();
		await invalidateCloudWorkspaceReviewEvent(queryClient, key, {
			sessionId: "session-1", sequence: 3, type: "chat.message", payload: {}, createdAt: "2026-09-20T00:00:01Z",
		});
		expect(invalidate).not.toHaveBeenCalled();
	});
});
