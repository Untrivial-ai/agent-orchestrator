// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../../types/workspace";
import { TooltipProvider } from "../ui/tooltip";
import { CloudWorkspaceExplorer } from "./CloudWorkspaceExplorer";

const { getWorkspaceReview, getWorkspaceReviewTree } = vi.hoisted(() => ({
	getWorkspaceReview: vi.fn(),
	getWorkspaceReviewTree: vi.fn(),
}));

vi.mock("../../hooks/useCloudCp", () => ({
	useCloudCp: () => ({ baseUrl: "https://cloud.test", ready: true, client: { getWorkspaceReview, getWorkspaceReviewTree } }),
}));
vi.mock("../../hooks/useCloudWorkspaceReview", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../hooks/useCloudWorkspaceReview")>();
	return { ...actual, useCloudWorkspaceReviewEvents: () => undefined };
});
const session: WorkspaceSession = {
	branch: "ao/cloud", cloud: { orgId: "org-1", sandboxProvider: "nodeops" }, id: "session-1", prs: [],
	provider: "codex", status: "working", title: "Cloud", updatedAt: "2026-09-20T00:00:00Z",
	workspaceId: "workspace-1", workspaceName: "Cloud workspace",
};

describe("CloudWorkspaceExplorer", () => {
	it("switches from categorized changes to the complete repository tree", async () => {
		getWorkspaceReview.mockResolvedValue({
			workspaceVersion: "v1", files: [{ path: "README.md", status: "unmodified", additions: 0, deletions: 0, size: 5, binary: false, editable: true, fileFingerprint: "fp" }],
			truncated: false, sections: { staged: [], unstaged: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 0, size: 10, binary: false, editable: true, fileFingerprint: "fp2" }], untracked: [], committed: [] }, commits: [],
			summary: { files: 1, additions: 1, deletions: 0 },
		});
		getWorkspaceReviewTree.mockResolvedValue({ path: "", truncated: false, entries: [{ name: "README.md", path: "README.md", type: "file", status: "unmodified", size: 5 }] });
		render(
			<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
				<TooltipProvider><CloudWorkspaceExplorer session={session} /></TooltipProvider>
			</QueryClientProvider>,
		);
		expect(await screen.findByRole("button", { name: /src\/App\.tsx/ })).toBeInTheDocument();
		await userEvent.click(screen.getByRole("tab", { name: "Files" }));
		expect(await screen.findByRole("treeitem", { name: /README\.md/ })).toBeInTheDocument();
	});
});
