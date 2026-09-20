// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { CloudCpClient, CloudCpWorkspaceReviewFileSummary } from "../../lib/cloud-cp";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { CloudDiffFile } from "./CloudDiffFile";

vi.mock("@pierre/diffs", () => ({
	parsePatchFiles: () => [{ files: [{ name: "src/App.tsx", type: "changed" }] }],
}));

vi.mock("@pierre/diffs/react", () => ({
	FileDiff: ({ options, renderGutterUtility }: {
		options: { diffStyle: string; loadDiffFiles: (metadata: unknown) => Promise<unknown> };
		renderGutterUtility?: (getLine: () => { lineNumber: number; side: "additions" }) => ReactNode;
	}) => {
		void options.loadDiffFiles({ name: "src/App.tsx", type: "changed" });
		return <div data-testid="file-diff" data-style={options.diffStyle}>{renderGutterUtility?.(() => ({ lineNumber: 2, side: "additions" }))}</div>;
	},
}));

const file: CloudCpWorkspaceReviewFileSummary = {
	path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20,
	binary: false, editable: true, fileFingerprint: "fp-1",
};

const annotation: FileAnnotationModel = {
	target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn(),
};

describe("CloudDiffFile", () => {
	it("renders split diffs and loads before and after revisions through Cloud", async () => {
		const getWorkspaceReviewRevision = vi.fn().mockResolvedValue({
			path: file.path, side: "after", revision: "r1", workspaceVersion: "v1", size: 4,
			exists: true, binary: false, truncated: false, content: "text",
		});
		render(<CloudDiffFile annotation={annotation} baseUrl="https://cloud.test" client={{ getWorkspaceReviewRevision } as unknown as CloudCpClient} commitSha="abc" file={file} onActiveSelectionChange={vi.fn()} orgId="org-1" patch="diff" scope="committed" sessionId="session-1" split workspaceVersion="v1" />);
		expect(screen.getByTestId("file-diff")).toHaveAttribute("data-style", "split");
		await waitFor(() => expect(getWorkspaceReviewRevision).toHaveBeenCalledTimes(2));
		expect(getWorkspaceReviewRevision).toHaveBeenCalledWith("org-1", "session-1", expect.objectContaining({ path: file.path, side: "before", scope: "committed", commitSha: "abc" }));
	});

	it("does not render another file's patch when the requested file has no diff", () => {
		const unchanged = { ...file, path: "README.md", status: "unmodified" as const, additions: 0, deletions: 0 };
		const revision = { path: unchanged.path, side: "after", revision: "r1", workspaceVersion: "v1", size: 4, exists: true, binary: false, truncated: false, content: "text" };
		render(<CloudDiffFile annotation={annotation} baseUrl="https://cloud.test" client={{ getWorkspaceReviewRevision: vi.fn().mockResolvedValue(revision) } as unknown as CloudCpClient} fallback={<div>No diff for this file</div>} file={unchanged} onActiveSelectionChange={vi.fn()} orgId="org-1" patch="multi-file diff" scope="combined" sessionId="session-1" split={false} workspaceVersion="v1" />);
		expect(screen.getByText("No diff for this file")).toBeInTheDocument();
		expect(screen.queryByTestId("file-diff")).not.toBeInTheDocument();
	});
});
