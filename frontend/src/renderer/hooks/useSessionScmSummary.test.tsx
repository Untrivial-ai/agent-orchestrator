import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { createClientMock, getMock, listSessionPullRequestsMock } = vi.hoisted(() => ({
	createClientMock: vi.fn(),
	getMock: vi.fn(),
	listSessionPullRequestsMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({ apiClient: { GET: getMock } }));

vi.mock("./useCloudCp", () => ({ createRendererCloudCpClient: createClientMock }));

import { settingsQueryKey } from "./useSettings";
import { useSessionScmSummary } from "./useSessionScmSummary";

let controlPlaneUrl = "https://cp.example.com";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	queryClient.setQueryData(settingsQueryKey, { cloudControlPlaneUrl: controlPlaneUrl });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const cloudPR = {
	url: "https://github.com/acme/app/pull/7",
	htmlUrl: "https://github.com/acme/app/pull/7",
	number: 7,
	title: "Fix login",
	state: "open",
	provider: "github",
	repository: "acme/app",
	author: "octocat",
	sourceBranch: "ao/session-1",
	targetBranch: "main",
	headSha: "abc123",
	additions: 12,
	deletions: 3,
	changedFiles: 2,
	ci: { state: "failing", failingChecks: [] },
	review: { decision: "none", hasUnresolvedHumanComments: false, unresolvedBy: [], reviews: [] },
	mergeability: {
		state: "blocked",
		reasons: [],
		pullRequestUrl: "https://github.com/acme/app/pull/7",
		conflictFiles: [],
	},
	updatedAt: "2026-08-01T00:00:00Z",
};

beforeEach(() => {
	controlPlaneUrl = "https://cp.example.com";
	createClientMock.mockReset().mockReturnValue({ listSessionPullRequests: listSessionPullRequestsMock });
	getMock.mockReset();
	listSessionPullRequestsMock.mockReset().mockResolvedValue({ sessionId: "cloud-1", pullRequests: [cloudPR] });
});

describe("useSessionScmSummary", () => {
	it("reads a local session's PRs from the daemon", async () => {
		getMock.mockResolvedValue({ data: { prs: [{ number: 3 }] }, error: undefined });
		const { result } = renderHook(() => useSessionScmSummary({ id: "local-1" }), { wrapper });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/pr", {
			params: { path: { sessionId: "local-1" } },
		});
		expect(listSessionPullRequestsMock).not.toHaveBeenCalled();
		expect(result.current.data).toEqual([{ number: 3 }]);
	});

	it("reads a cloud session's PRs from the control plane in the daemon's shape", async () => {
		const { result } = renderHook(() => useSessionScmSummary({ id: "cloud-1", cloud: { orgId: "org-1" } }), {
			wrapper,
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(createClientMock).toHaveBeenCalledWith("https://cp.example.com");
		expect(listSessionPullRequestsMock).toHaveBeenCalledWith("org-1", "cloud-1");
		expect(getMock).not.toHaveBeenCalled();
		expect(result.current.data).toEqual([
			expect.objectContaining({
				number: 7,
				title: "Fix login",
				repo: "acme/app",
				provider: "github",
				headSha: "abc123",
				additions: 12,
				ci: { state: "failing", autoInjectCI: false, failingChecks: [] },
				review: { decision: "none", hasUnresolvedHumanComments: false, unresolvedBy: [] },
				mergeability: { state: "blocked", reasons: [], prUrl: "https://github.com/acme/app/pull/7" },
			}),
		]);
	});

	it("fails a cloud session's query without a configured control plane", async () => {
		controlPlaneUrl = "";
		const { result } = renderHook(() => useSessionScmSummary({ id: "cloud-1", cloud: { orgId: "org-1" } }), {
			wrapper,
		});
		// The hook pins one retry, so the failure settles after its retry delay.
		await waitFor(() => expect(result.current.isError).toBe(true), { timeout: 3_000 });
		expect(listSessionPullRequestsMock).not.toHaveBeenCalled();
		expect(getMock).not.toHaveBeenCalled();
	});
});
