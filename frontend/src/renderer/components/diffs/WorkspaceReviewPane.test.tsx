import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceFilesResponse } from "../../hooks/useSessionWorkspaceFiles";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { WorkspaceReviewPane } from "./WorkspaceReviewPane";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

vi.mock("../../lib/api-client", () => ({
	apiClient: { POST: postMock, GET: vi.fn() },
	apiErrorMessage: (error: unknown, fallback = "Request failed") => error instanceof Error ? error.message : fallback,
}));

vi.mock("@pierre/diffs", () => ({
	parsePatchFiles: (_patch: string) => [{ files: [{ name: "src/App.tsx", type: "changed" }] }],
}));

vi.mock("@pierre/diffs/react", () => ({
	CodeView: ({ items, renderCustomHeader }: { items: Array<{ id: string }>; renderCustomHeader: (item: { id: string }) => ReactNode }) => (
		<div data-testid="code-view">{items.map((item) => <div key={item.id}>{renderCustomHeader(item)}</div>)}</div>
	),
}));

function annotation(): FileAnnotationModel {
	return { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn() };
}

function workspace(files: WorkspaceFilesResponse["files"]): WorkspaceFilesResponse {
	return {
		sessionId: "sess-1",
		workspaceVersion: "workspace-1",
		files,
		sections: { committed: [], staged: [], unstaged: files, untracked: [] },
		commits: [],
		summary: { additions: 1, deletions: 1, files: files.length },
		truncated: false,
	};
}

function renderWithQuery(children: ReactNode) {
	return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>);
}

describe("WorkspaceReviewPane", () => {
	beforeEach(() => {
		window.localStorage.clear();
		postMock.mockReset().mockResolvedValue({
			data: {
				sessionId: "sess-1",
				workspaceVersion: "workspace-1",
				groups: [{ repository: "", patch: "diff --git a/src/App.tsx b/src/App.tsx\n", truncated: false, includedPaths: ["src/App.tsx"], deferred: [] }],
			},
		});
	});

	it("requests grouped patches and renders a continuous review with viewed progress", async () => {
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} wrap />);

		expect(await screen.findByTestId("code-view")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/diffs", expect.objectContaining({
			body: expect.objectContaining({ paths: ["src/App.tsx"], scope: "unstaged", workspaceVersion: "workspace-1" }),
		}));
		expect(screen.getByText("0 of 1 viewed")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Mark src/App.tsx as viewed" }));
		expect(screen.getByText("1 of 1 viewed")).toBeInTheDocument();
	});

	it("offers the full file browser when there are no changes", async () => {
		const onBrowseAll = vi.fn();
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={workspace([])} filter="" onBrowseAll={onBrowseAll} sessionId="sess-1" split={false} wrap />);

		expect(screen.getByText("No changed files found.")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Browse all files" }));
		expect(onBrowseAll).toHaveBeenCalledOnce();
		expect(postMock).not.toHaveBeenCalled();
	});

	it("filters the review without requesting unrelated files", async () => {
		const files = [
			{ path: "src/App.tsx", status: "modified" as const, additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" },
			{ path: "docs/guide.md", status: "modified" as const, additions: 1, deletions: 0, size: 20, binary: false, fileFingerprint: "file-2" },
		];
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={workspace(files)} filter="app" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} wrap />);

		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(postMock.mock.calls[0]?.[1]?.body.paths).toEqual(["src/App.tsx"]);
	});

	it("defers lockfile patches until the user explicitly loads them", async () => {
		const data = workspace([{ path: "package-lock.json", status: "modified", additions: 800, deletions: 700, size: 600_000, binary: false, fileFingerprint: "lock-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} wrap />);

		expect(screen.getByText(/diff is deferred/i)).toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole("button", { name: "Load diff" }));
		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(postMock.mock.calls[0]?.[1]?.body.paths).toEqual(["package-lock.json"]);
	});
});
