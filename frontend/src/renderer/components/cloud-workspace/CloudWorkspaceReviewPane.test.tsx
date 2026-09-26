// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CloudCpClient, CloudCpWorkspaceReviewFileSummary, CloudCpWorkspaceReviewResponse } from "../../lib/cloud-cp";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { TooltipProvider } from "../ui/tooltip";
import { CloudWorkspaceReviewPane } from "./CloudWorkspaceReviewPane";

vi.mock("./CloudDiffFile", () => ({ CloudDiffFile: ({ file, split }: { file: CloudCpWorkspaceReviewFileSummary; split: boolean }) => <div data-split={String(split)} data-testid={`diff:${file.path}`} /> }));

const changed = (path: string): CloudCpWorkspaceReviewFileSummary => ({ path, status: "modified", additions: 2, deletions: 1, size: 20, binary: false, editable: true, fileFingerprint: `fp:${path}` });
const annotation: FileAnnotationModel = { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn() };

function data(): CloudCpWorkspaceReviewResponse {
	const unstaged = changed("src/App.tsx");
	const staged = changed("src/staged.ts");
	const untracked = { ...changed("notes.txt"), status: "untracked" as const };
	const committed = changed("src/old.ts");
	return {
		workspaceVersion: "v1", files: [unstaged, staged, untracked, committed], truncated: false,
		sections: { unstaged: [unstaged], staged: [staged], untracked: [untracked], committed: [committed] },
		commits: [{ sha: "abcdef123", subject: "Previous work", author: "Ada", timestamp: "2026-09-20T00:00:00Z", files: [committed] }],
		summary: { files: 4, additions: 8, deletions: 4 },
	};
}

function renderPane(client: CloudCpClient) {
	return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><TooltipProvider><CloudWorkspaceReviewPane annotation={annotation} baseUrl="https://cloud.test" client={client} data={data()} filter="" onBrowseAll={vi.fn()} orgId="org-1" sessionId="session-1" split /></TooltipProvider></QueryClientProvider>);
}

describe("CloudWorkspaceReviewPane", () => {
	beforeEach(() => { window.localStorage.clear(); });

	it("starts with unstaged changes and switches across working and committed sources", async () => {
		const getWorkspaceReviewDiffs = vi.fn().mockResolvedValue({ workspaceVersion: "v1", groups: [{ patch: "diff", truncated: false, includedPaths: ["src/App.tsx"], deferred: [], errors: [] }] });
		renderPane({ getWorkspaceReviewDiffs } as unknown as CloudCpClient);
		expect(await screen.findByTestId("diff:src/App.tsx")).toHaveAttribute("data-split", "true");
		expect(getWorkspaceReviewDiffs).toHaveBeenCalledWith("org-1", "session-1", expect.objectContaining({ scope: "unstaged", paths: ["src/App.tsx"] }));
		await userEvent.click(screen.getByRole("button", { name: /Staged/ }));
		await waitFor(() => expect(getWorkspaceReviewDiffs).toHaveBeenLastCalledWith("org-1", "session-1", expect.objectContaining({ scope: "staged", paths: ["src/staged.ts"] })));
		await userEvent.click(screen.getByRole("button", { name: /Commits/ }));
		await userEvent.click(screen.getByRole("button", { name: /Previous work/ }));
		await waitFor(() => expect(getWorkspaceReviewDiffs).toHaveBeenLastCalledWith("org-1", "session-1", expect.objectContaining({ scope: "committed", commitSha: "abcdef123" })));
	});

	it("shows aggregate counts and persists viewed state", async () => {
		const getWorkspaceReviewDiffs = vi.fn().mockResolvedValue({ workspaceVersion: "v1", groups: [{ patch: "diff", truncated: false, includedPaths: ["src/App.tsx"], deferred: [], errors: [] }] });
		renderPane({ getWorkspaceReviewDiffs } as unknown as CloudCpClient);
		expect(await screen.findByText("+2")).toBeInTheDocument();
		expect(screen.getByText("−1")).toBeInTheDocument();
		expect(await screen.findByTestId("diff:src/App.tsx")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("checkbox", { name: /Mark src\/App\.tsx as viewed/ }));
		expect(screen.getByText("1 of 1 viewed")).toBeInTheDocument();
		expect(screen.queryByTestId("diff:src/App.tsx")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Expand src/App.tsx" })).toBeInTheDocument();
	});
});
