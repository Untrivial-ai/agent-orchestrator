import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { useSessionScmSummary } from "./useSessionScmSummary";

const { listSessionPullRequestsMock } = vi.hoisted(() => ({
	listSessionPullRequestsMock: vi.fn(),
}));

vi.mock("../lib/cloud-cp/renderer-client", () => ({
	createRendererCloudCpClient: () => ({ listSessionPullRequests: listSessionPullRequestsMock }),
}));

vi.mock("./useSettings", () => ({
	useSettings: () => ({ settings: { cloudControlPlaneUrl: "https://cloud.example.test" } }),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: vi.fn(async () => ({ data: { prs: [] } })) },
}));

describe("useSessionScmSummary cloud source", () => {
	it("maps Cloud PR details into the exact local inspector model", async () => {
		listSessionPullRequestsMock.mockResolvedValue({
			sessionId: "session-1",
			pullRequests: [{
				url: "https://api.github.com/repos/acme/widgets/pulls/7",
				htmlUrl: "https://github.com/acme/widgets/pull/7",
				number: 7,
				title: "Cloud details",
				state: "open",
				provider: "github",
				repository: "acme/widgets",
				author: "octocat",
				sourceBranch: "feature",
				targetBranch: "main",
				headSha: "abc123",
				additions: 4,
				deletions: 1,
				changedFiles: 2,
				ci: { state: "passing", failingChecks: [] },
				review: {
					decision: "none",
					hasUnresolvedHumanComments: false,
					unresolvedBy: [],
					reviews: [],
				},
				mergeability: {
					state: "mergeable",
					reasons: [],
					pullRequestUrl: "https://github.com/acme/widgets/pull/7",
					conflictFiles: [],
				},
				updatedAt: "2026-09-22T00:00:00Z",
				observedAt: "2026-09-22T00:00:00Z",
				ciObservedAt: "2026-09-22T00:00:00Z",
				reviewObservedAt: "2026-09-22T00:00:00Z",
			}],
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);

		const { result } = renderHook(
			() => useSessionScmSummary("session-1", true, "org-1"),
			{ wrapper },
		);

		await waitFor(() => expect(result.current.data?.[0]?.title).toBe("Cloud details"));
		expect(result.current.data?.[0]).toMatchObject({
			repo: "acme/widgets",
			mergeability: { prUrl: "https://github.com/acme/widgets/pull/7" },
		});
	});
});
