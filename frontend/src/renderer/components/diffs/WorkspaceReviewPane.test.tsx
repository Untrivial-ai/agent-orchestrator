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

function committedWorkspace(files: WorkspaceFilesResponse["files"]): WorkspaceFilesResponse {
	const data = workspace([]);
	const commitFiles = files.map((file) => ({
		...file,
		editable: file.editable ?? false,
		fileFingerprint: file.fileFingerprint ?? `commit:${file.path}`,
	}));
	data.files = files;
	data.sections.committed = files;
	data.summary.files = files.length;
	data.commits = [{
		author: "Ada Lovelace",
		files: commitFiles,
		sha: "commit-1",
		subject: "Test commit",
		timestamp: "2026-09-10T10:00:00Z",
	}];
	return data;
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

	it("requests grouped patches and renders a continuous review for a selected commit", async () => {
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, editable: true, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		expect(await screen.findByTestId("code-view")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/diffs", expect.objectContaining({
			body: expect.objectContaining({ commitSha: "commit-1", paths: ["src/App.tsx"], scope: "committed", workspaceVersion: "workspace-1" }),
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
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getAllByRole("button", { name: "Collapse src/App.tsx" })[1]);
		expect(screen.getByTestId("code-view").querySelector("[data-collapsed]"))?.toHaveAttribute("data-collapsed", "true");
		await userEvent.click(screen.getAllByRole("button", { name: "Expand src/App.tsx" })[1]);
		expect(screen.getByTestId("code-view").querySelector("[data-collapsed]"))?.toHaveAttribute("data-collapsed", "false");
	});

	it("uses one toggle for collapsing and expanding all files", async () => {
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		const toggle = screen.getByRole("button", { name: "Collapse all files" });
		await userEvent.click(toggle);
		expect(screen.getByRole("button", { name: "Expand all files" })).toBeInTheDocument();
		expect(screen.getByTestId("code-view").querySelectorAll('[data-collapsed="true"]')).toHaveLength(1);

		await userEvent.click(screen.getByRole("button", { name: "Expand all files" }));
		expect(screen.getByRole("button", { name: "Collapse all files" })).toBeInTheDocument();
		expect(screen.getByTestId("code-view").querySelectorAll('[data-collapsed="false"]')).toHaveLength(1);
	});

	it("closes a file's feedback composer when that file is collapsed", async () => {
		const model = annotation();
		model.target = { path: "src/App.tsx", side: "file", scope: "committed", surface: "review" };
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
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
		const data = committedWorkspace([{ path: "README.md", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={model} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		const inlineFeedback = screen.getAllByRole("button", { name: "Add feedback" })[1];
		expect(screen.getByTestId("code-view")).toHaveAttribute("data-gutter-enabled", "true");
		expect(inlineFeedback).not.toHaveClass("opacity-0");
		await userEvent.click(inlineFeedback);
		expect(model.begin).toHaveBeenCalledWith(expect.objectContaining({ path: "README.md", side: "new", line: 7 }));
		await userEvent.click(screen.getByRole("button", { name: "Open rich preview" }));
		expect(onOpenFile).toHaveBeenCalledWith("README.md", { commitSha: "commit-1", mode: "rendered", scope: "committed" });
	});

	it("opens a file diff in the center pane through the dedicated action", async () => {
		const onOpenFile = vi.fn();
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Open diff in center" }));
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { commitSha: "commit-1", mode: "diff", scope: "committed" });
	});

	it("opens a changed diff directly in syntax-aware edit mode", async () => {
		const onOpenFile = vi.fn();
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, editable: true, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Edit file" }));
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { editing: true, mode: "file", scope: "committed" });
	});

	it("does not advertise editing for a file the daemon marks read-only", async () => {
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, editable: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={vi.fn()} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		expect(screen.queryByRole("button", { name: "Edit file" })).not.toBeInTheDocument();
	});

	it("anchors whole-file feedback directly below the matching file header", async () => {
		const model = annotation();
		model.target = { path: "src/App.tsx", side: "file", scope: "committed" };
		const data = committedWorkspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
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
		const data = committedWorkspace([{ path: "README.md", status: "deleted", additions: 0, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Open full file" }));
		expect(onOpenFile).toHaveBeenCalledWith("README.md", { commitSha: "commit-1", mode: "file", scope: "committed" });
	});

	it("uses source tabs and shows diffs in the review pane for unstaged and staged changes", async () => {
		const onOpenFile = vi.fn();
		const unstaged = { path: "src/App.tsx", status: "modified" as const, additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "u-1" };
		const staged = { path: "README.md", status: "modified" as const, additions: 1, deletions: 0, size: 20, binary: false, fileFingerprint: "s-1" };
		const data = workspace([unstaged]);
		data.sections.staged = [staged];
		data.sections.untracked = [{ ...staged, path: "notes.txt", status: "added", fileFingerprint: "n-1" }];
		postMock.mockImplementation((_path, init) => Promise.resolve({
			data: {
				sessionId: "sess-1",
				workspaceVersion: "workspace-1",
				groups: [{
					repository: "",
					patch: init?.body?.scope === "staged"
						? "diff --git a/README.md b/README.md\n"
						: "diff --git a/src/App.tsx b/src/App.tsx\n",
					truncated: false,
					includedPaths: [init?.body?.scope === "staged" ? "README.md" : "src/App.tsx"],
					deferred: [],
				}],
			},
		}));
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);
		const sourceScope = screen.getByRole("button", { name: /Unstaged/ });
		const stagedScope = screen.getByRole("button", { name: /Staged/ });
		expect(screen.queryByText("Untracked")).not.toBeInTheDocument();
		await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/diffs", expect.objectContaining({
			body: expect.objectContaining({ commitSha: undefined, paths: ["src/App.tsx"], scope: "unstaged", workspaceVersion: "workspace-1" }),
		})));
	expect(await screen.findByTestId("code-view")).toBeInTheDocument();
		expect(sourceScope).toHaveAttribute("aria-pressed", "true");
		expect(stagedScope).toHaveAttribute("aria-pressed", "false");
		await userEvent.click(stagedScope);
		expect(stagedScope).toHaveAttribute("aria-pressed", "true");
		expect(await screen.findByTestId("code-view")).toBeInTheDocument();
		expect(onOpenFile).not.toHaveBeenCalled();
		await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/diffs", expect.objectContaining({
			body: expect.objectContaining({ paths: ["README.md"], scope: "staged", workspaceVersion: "workspace-1" }),
		})));
	});

	it("omits empty and untracked working-change sources", () => {
		const unstaged = { path: "src/App.tsx", status: "modified" as const, additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "u-1" };
		const staged = { ...unstaged, path: "README.md", fileFingerprint: "s-1" };
		const data = workspace([unstaged]);
		data.sections.staged = [staged];
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		expect(screen.getByRole("button", { name: /Unstaged/ })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Staged/ })).toBeInTheDocument();
		expect(screen.queryByText("Untracked")).not.toBeInTheDocument();
	});

	it("uses one direct source tab when only one source is available", async () => {
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "u-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		const sourceButton = screen.getByRole("button", { name: /Unstaged/ });
		expect(sourceButton).toHaveAttribute("aria-pressed", "true");
		await userEvent.click(sourceButton);
		expect(screen.getByTestId("code-view")).toBeInTheDocument();
	});

	it("browses GitHub-style commits and reviews the selected commit only", async () => {
		const onOpenFile = vi.fn();
		postMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				workspaceVersion: "workspace-1",
				groups: [{ repository: "", patch: "diff --git a/README.md b/README.md\n", truncated: false, includedPaths: ["README.md"], deferred: [] }],
			},
		});
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "u-1" }]);
		data.commits = [{
			author: "Ada Lovelace",
			files: [{ path: "README.md", status: "modified", additions: 2, deletions: 1, size: 40, binary: false, editable: false, fileFingerprint: "c-1" }],
			sha: "abcdef1234567890",
			subject: "Document the commit browser",
			timestamp: "2026-09-10T10:00:00Z",
		}];
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} onOpenFile={onOpenFile} sessionId="sess-1" split={false} />);

		await userEvent.click(screen.getByRole("button", { name: /Commits/ }));
		expect(screen.getByRole("list", { name: "Commit history" })).toBeInTheDocument();
		expect(screen.getByText("README.md")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: /Document the commit browser/ }));
		await waitFor(() => expect(postMock).toHaveBeenLastCalledWith("/api/v1/sessions/{sessionId}/workspace/diffs", expect.objectContaining({
			body: expect.objectContaining({ commitSha: "abcdef1234567890", paths: ["README.md"], scope: "committed" }),
		})));
		await userEvent.click(await screen.findByRole("button", { name: "Open diff in center" }));
		expect(onOpenFile).toHaveBeenCalledWith("README.md", { commitSha: "abcdef1234567890", mode: "diff", scope: "committed" });
	});

	it("offers the full file browser when there are no changes", async () => {
		const onBrowseAll = vi.fn();
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={workspace([])} filter="" onBrowseAll={onBrowseAll} sessionId="sess-1" split={false} />);

		expect(screen.getByText("No changed files found.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Unstaged|Staged|Review changes/ })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Browse all files" }));
		expect(onBrowseAll).toHaveBeenCalledOnce();
		expect(postMock).not.toHaveBeenCalled();
	});

	it("shows the active commit hash on the commits button even when commit is not the active scope", async () => {
		const data = workspace([{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "u-1" }]);
		data.commits = [{
			author: "Ada Lovelace",
			files: [{
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 1,
				size: 20,
				binary: false,
				fileFingerprint: "file-1",
				editable: false,
			}],
			sha: "abcdef1234567890",
			subject: "Document the commit browser",
			timestamp: "2026-09-10T10:00:00Z",
		}];

		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		const commitButton = screen.getByRole("button", { name: /Commits/ });
		expect(commitButton).toHaveTextContent("Commits");
		expect(screen.getByText("abcdef1")).toBeInTheDocument();
	});

	it("filters the review without requesting unrelated files", async () => {
		const files = [
			{ path: "src/App.tsx", status: "modified" as const, additions: 1, deletions: 1, size: 20, binary: false, fileFingerprint: "file-1" },
			{ path: "docs/guide.md", status: "modified" as const, additions: 1, deletions: 0, size: 20, binary: false, fileFingerprint: "file-2" },
		];
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={committedWorkspace(files)} filter="app" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(postMock.mock.calls[0]?.[1]?.body.paths).toEqual(["src/App.tsx"]);
	});

	it("defers lockfile patches until the user explicitly loads them", async () => {
		const data = committedWorkspace([{ path: "package-lock.json", status: "modified", additions: 800, deletions: 700, size: 600_000, binary: false, fileFingerprint: "lock-1" }]);
		renderWithQuery(<WorkspaceReviewPane annotation={annotation()} data={data} filter="" onBrowseAll={vi.fn()} sessionId="sess-1" split={false} />);

		expect(screen.getByText(/diff is deferred/i)).toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole("button", { name: "Load diff" }));
		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(postMock.mock.calls[0]?.[1]?.body.paths).toEqual(["package-lock.json"]);
	});
});
