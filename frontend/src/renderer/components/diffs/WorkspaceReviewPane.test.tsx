import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceFilesResponse } from "../../hooks/useSessionWorkspaceFiles";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { TooltipProvider } from "../ui/tooltip";
import { WorkspaceReviewPane } from "./WorkspaceReviewPane";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

vi.mock("../../lib/api-client", () => ({
	apiClient: { POST: postMock, GET: vi.fn() },
	apiErrorMessage: (error: unknown, fallback = "Request failed") => error instanceof Error ? error.message : fallback,
}));

vi.mock("@pierre/diffs", () => ({
	parsePatchFiles: (patch: string) => [{ files: [{ name: patch.includes("README.md") ? "README.md" : "src/App.tsx", type: "changed" }] }],
}));

vi.mock("@pierre/diffs/react", () => ({
	CodeView: ({ className, items, options, renderCustomHeader, renderGutterUtility }: {
		className: string;
		items: Array<{ id: string; collapsed?: boolean }>;
		options: { enableGutterUtility?: boolean; overflow?: string; unsafeCSS?: string };
		renderCustomHeader: (item: { id: string }) => ReactNode;
		renderGutterUtility?: (getHoveredLine: () => { lineNumber: number; side: "additions" }, item: { id: string }) => ReactNode;
	}) => (
		<div className={className} data-gutter-enabled={String(Boolean(options.enableGutterUtility))} data-overflow={options.overflow} data-surface-css={options.unsafeCSS} data-testid="code-view">
			{items.map((item) => <div data-collapsed={String(Boolean(item.collapsed))} key={item.id}>{renderCustomHeader(item)}</div>)}
			{items[0] ? renderGutterUtility?.(() => ({ lineNumber: 7, side: "additions" }), items[0]) : null}
		</div>
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
	return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><TooltipProvider>{children}</TooltipProvider></QueryClientProvider>);
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
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, editable: true, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		expect(await screen.findByTestId("code-view")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/diffs", expect.objectContaining({
			body: expect.objectContaining({ paths: ["src/App.tsx"], scope: "unstaged", workspaceVersion: "workspace-1" }),
		}));
		expect(screen.getByText("0 of 1 viewed")).toBeInTheDocument();
		expect(screen.getByTestId("code-view")).toHaveClass("overflow-y-auto");
		expect(screen.getByTestId("code-view")).toHaveAttribute("data-overflow", "wrap");
		expect(screen.getByTestId("code-view")).toHaveAttribute("data-surface-css", expect.stringContaining("--diffs-bg: var(--color-bg-primary)"));
		await userEvent.click(screen.getByRole("checkbox", { name: "Mark src/App.tsx as viewed" }));
		expect(screen.getByText("1 of 1 viewed")).toBeInTheDocument();
		expect(screen.getByRole("checkbox", { name: "Mark src/App.tsx as not viewed" })).toHaveClass("size-4");
		expect(screen.getByRole("checkbox", { name: "Mark src/App.tsx as not viewed" })).toHaveStyle({
			backgroundColor: "#fff",
			borderColor: "#fff",
			color: "#000",
		});
	});

	it("collapses and expands file items through controlled CodeView state", async () => {
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getAllByRole("button", { name: "Collapse src/App.tsx" })[1]);
		expect(screen.getByTestId("code-view").querySelector("[data-collapsed]"))?.toHaveAttribute("data-collapsed", "true");
		await userEvent.click(screen.getAllByRole("button", { name: "Expand src/App.tsx" })[1]);
		expect(screen.getByTestId("code-view").querySelector("[data-collapsed]"))?.toHaveAttribute("data-collapsed", "false");
	});

	it("closes a file's feedback composer when that file is collapsed", async () => {
		const model = annotation();
		model.target = { path: "src/App.tsx", side: "file", scope: "unstaged", surface: "review" };
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={model} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);
		expect(await screen.findByRole("textbox", { name: /Feedback for src\/App\.tsx/ })).toBeInTheDocument();

		await userEvent.click(screen.getAllByRole("button", { name: "Collapse src/App.tsx" })[1]);
		expect(model.cancel).toHaveBeenCalledOnce();
	});

	it("routes the gutter plus and file actions without opening an external pane", async () => {
		const model = annotation();
		const onOpenFile = vi.fn();
		postMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				workspaceVersion: "workspace-1",
				groups: [{ repository: "", patch: "diff --git a/README.md b/README.md\n", truncated: false, includedPaths: ["README.md"], deferred: [] }],
			},
		});
		const data = workspace([{ path: "README.md", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={model} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		const inlineFeedback = screen.getAllByRole("button", { name: "Add feedback" })[1];
		expect(screen.getByTestId("code-view")).toHaveAttribute("data-gutter-enabled", "true");
		expect(inlineFeedback).not.toHaveClass("opacity-0");
		await userEvent.click(inlineFeedback);
		expect(model.begin).toHaveBeenCalledWith(expect.objectContaining({ path: "README.md", side: "new", line: 7 }));
		await userEvent.click(screen.getByRole("button", { name: "Open rich preview" }));
		expect(onOpenFile).toHaveBeenCalledWith("README.md", "rendered");
	});

	it("opens a file diff in the center pane through the dedicated action", async () => {
		const onOpenFileInCenter = vi.fn();
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFileInCenter={onOpenFileInCenter} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Open diff in center" }));
		expect(onOpenFileInCenter).toHaveBeenCalledWith("src/App.tsx");
	});

	it("opens a changed diff directly in syntax-aware edit mode", async () => {
		const onEditFile = vi.fn();
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, editable: true, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onEditFile={onEditFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Edit file" }));
		expect(onEditFile).toHaveBeenCalledWith("src/App.tsx");
	});

	it("does not advertise editing for a file the daemon marks read-only", async () => {
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, editable: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onEditFile={vi.fn()} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		expect(screen.queryByRole("button", { name: "Edit file" })).not.toBeInTheDocument();
	});

	it("anchors whole-file feedback directly below the matching file header", async () => {
		const model = annotation();
		model.target = { path: "src/App.tsx", side: "file", scope: "unstaged" };
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={model} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		const composer = await screen.findByRole("textbox", { name: /Feedback for src\/App\.tsx/ });
		expect(composer.closest(".relative.bg-surface")).toContainElement(screen.getAllByRole("button", { name: "Collapse src/App.tsx" })[1]);
	});

	it("opens deleted markdown as source because no current rendered revision exists", async () => {
		const onOpenFile = vi.fn();
		postMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				workspaceVersion: "workspace-1",
				groups: [{ repository: "", patch: "diff --git a/README.md b/README.md\n", truncated: false, includedPaths: ["README.md"], deferred: [] }],
			},
		});
		const data = workspace([{ path: "README.md", status: "deleted", additions: 0, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Open full file" }));
		expect(onOpenFile).toHaveBeenCalledWith("README.md", "file");
	});

	it("offers the full file browser when there are no changes", async () => {
		const onBrowseAll = vi.fn();
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={workspace([])} filter="" onBrowseAll={onBrowseAll} sessionId="sess-1" split={false} />);

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
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={workspace(files)} filter="app" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(postMock.mock.calls[0]?.[1]?.body.paths).toEqual(["src/App.tsx"]);
	});

	it("defers lockfile patches until the user explicitly loads them", async () => {
		const data = workspace([{ path: "package-lock.json", status: "modified", additions: 800, deletions: 700, size: 600_000, binary: false, fileFingerprint: "lock-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		expect(screen.getByText(/diff is deferred/i)).toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole("button", { name: "Load diff" }));
		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(postMock.mock.calls[0]?.[1]?.body.paths).toEqual(["package-lock.json"]);
	});
});
