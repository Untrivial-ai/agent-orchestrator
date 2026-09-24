import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionFileExplorer } from "./SessionFileExplorer";
import { TooltipProvider } from "./ui/tooltip";
import { useUiStore } from "../stores/ui-store";
import type { SessionArtifact } from "../types/workspace";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

type MockTreeFile = { path: string; type: "file" };
type MockTreeNode = MockTreeFile | { children?: MockTreeNode[]; path: string; type: "directory" };

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	getApiBaseUrl: () => "",
	hasTrustedApiBaseUrl: () => false,
	subscribeApiBaseUrl: () => () => undefined,
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		return fallback;
	},
}));

vi.mock("./FileTree", () => ({
	FileTree: ({
		changedOnly,
		changedOnlyData,
		forceChangedOnly = false,
		filterText,
		onSelectPath,
	}: {
		changedOnly: boolean;
		changedOnlyData?: MockTreeNode[];
		forceChangedOnly?: boolean;
		filterText: string;
		onSelectPath: (node: { path: string; type: "file" }) => void;
	}) => {
		const [expanded, setExpanded] = useState(false);
		const collectFiles = (nodes: MockTreeNode[]): MockTreeFile[] =>
			nodes.flatMap((node) => (node.type === "file" ? [node] : collectFiles(node.children ?? [])));
		const flattenedFiles = collectFiles(changedOnlyData ?? []);
		const selectablePath = flattenedFiles[0]?.path ?? "src/App.tsx";
		return <div>
			<span data-testid="tree-changed-only">{String(changedOnly || forceChangedOnly)}</span>
			<span data-testid="tree-filter">{filterText}</span>
			<button onClick={() => setExpanded((current) => !current)} type="button">expand src</button>
			{expanded ? <span>src directory expanded</span> : null}
			<button onClick={() => onSelectPath({ path: selectablePath, type: "file" })} type="button">
				select {selectablePath}
			</button>
		</div>;
	},
}));

vi.mock("./ArtifactFileView", () => ({
	ArtifactFileView: ({ artifactName, path }: { artifactName: string; path: string }) => (
		<div data-testid="artifact-view">{`${artifactName}:${path}`}</div>
	),
}));

vi.mock("./FileContentPane", () => ({
	FileContentPane: ({ initialEditing, initialMode, path, previousPath }: { initialEditing?: boolean; initialMode?: string; path: string | null; previousPath?: string }) => <div data-editing={String(Boolean(initialEditing))} data-mode={initialMode ?? "default"} data-previous-path={previousPath} data-testid="content-pane">{path ?? "none"}</div>,
}));

vi.mock("./diffs/WorkspaceReviewPane", () => ({
	WorkspaceReviewPane: ({ filter, onBrowseAll, onOpenFile }: { filter: string; onBrowseAll: () => void; onOpenFile?: (path: string, options?: { editing?: boolean; mode?: "diff" | "file" | "rendered" }) => void }) => (
		<div data-testid="review-pane">
			<span data-testid="review-filter">{filter}</span>
			<button onClick={onBrowseAll} type="button">Browse all files</button>
			<button onClick={() => onOpenFile?.("src/App.tsx", { mode: "file" })} type="button">Open full file</button>
			<button onClick={() => onOpenFile?.("README.md", { mode: "rendered" })} type="button">Render README.md</button>
			<button onClick={() => onOpenFile?.("src/App.tsx", { editing: true, mode: "file" })} type="button">Edit src/App.tsx</button>
			<button onClick={() => onOpenFile?.("src/App.tsx", { mode: "diff" })} type="button">Open diff in center</button>
		</div>
	),
}));

function renderWithQuery(children: ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return {
		client,
		...render(
			<QueryClientProvider client={client}>
				<TooltipProvider>{children}</TooltipProvider>
			</QueryClientProvider>,
		),
	};
}

describe("SessionFileExplorer", () => {
	const artifacts: SessionArtifact[] = [
		{ kind: "markdown", name: "notes.md", path: "notes.md", size: 12, updatedAt: "2026-09-22T00:00:00Z" },
		{ kind: "file", name: "plan.md", path: "reports/plan.md", size: 20, updatedAt: "2026-09-22T00:00:00Z" },
	];

	beforeEach(() => {
		window.localStorage.clear();
		useUiStore.setState({ inspectorSessions: {} });
		getMock.mockReset().mockResolvedValue({
			data: {
				sessionId: "sess-1",
				files: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 0, size: 10, binary: false }],
				sections: { committed: [], staged: [], unstaged: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 0, size: 10, binary: false }], untracked: [] },
				commits: [],
				summary: { additions: 1, deletions: 0, files: 1 },
				truncated: false,
				workspaceVersion: "version-1",
			},
		});
		postMock.mockReset();
	});

	it("keeps the filtered tree visible and opens a selected file in the center", async () => {
		const onOpenFile = vi.fn();
		useUiStore.getState().setFilesChangedOnly("sess-explorer-1", false);
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-explorer-1" />);

		const input = screen.getByRole("textbox", { name: "Filter files" });
		fireEvent.change(input, { target: { value: "app" } });
		expect(screen.getByTestId("tree-filter")).toHaveTextContent("app");

		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		expect(screen.getByTestId("tree-changed-only")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Split diff view" })).not.toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { mode: "file" });
	});

	it("preserves expanded parent directories while files open in the center", async () => {
		const onOpenFile = vi.fn();
		useUiStore.getState().setFilesChangedOnly("sess-explorer-parent", false);
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-explorer-parent" />);

		await userEvent.click(screen.getByRole("button", { name: "expand src" }));
		expect(screen.getByText("src directory expanded")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));

		expect(screen.getByText("src directory expanded")).toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledOnce();
	});

	it("keeps the tree visible and opens an externally requested file in the center", () => {
		const onOpenFile = vi.fn();
		const { client, rerender } = renderWithQuery(
			<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-explorer-reveal" revealRequest={null} />,
		);

		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		rerender(
			<QueryClientProvider client={client}>
				<TooltipProvider>
					<SessionFileExplorer
						onOpenFile={onOpenFile}
						revealRequest={{ path: "docs/notes.txt", key: 1 }}
						sessionId="sess-explorer-reveal"
					/>
				</TooltipProvider>
			</QueryClientProvider>,
		);

		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		expect(screen.getByTestId("tree-changed-only")).toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledWith("docs/notes.txt", { mode: "file" });
	});

	it("keeps the tree and content side by side when maximized", async () => {
		const widthSpy = vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(500);
		useUiStore.getState().setFilesChangedOnly("sess-explorer-maximized", false);
		const { container } = renderWithQuery(<SessionFileExplorer isMaximized sessionId="sess-explorer-maximized" />);

		// Maximized: both are mounted at once, with no back button.
		expect(screen.getByTestId("tree-changed-only")).toBeInTheDocument();
		expect(screen.getByTestId("content-pane")).toHaveTextContent("none");
		expect(screen.queryByRole("button", { name: "Back to file tree" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
		expect(screen.getByTestId("tree-changed-only")).toBeInTheDocument();

		const panels = container.querySelectorAll('[data-slot="resizable-panel"]');
		expect(panels).toHaveLength(2);
		expect(panels[0]).toHaveStyle({ flexGrow: "26" });
		expect(panels[1]).toHaveStyle({ flexGrow: "74" });
		widthSpy.mockRestore();
	});

	it("renders multiple artifacts as a selectable Files source", async () => {
		renderWithQuery(<SessionFileExplorer artifacts={artifacts} sessionId="sess-artifacts" />);

		await userEvent.click(screen.getByRole("combobox", { name: "File source" }));
		await userEvent.click(screen.getByRole("option", { name: "Artifacts (2)" }));

		expect(screen.getByText("Artifacts (2)", { selector: "div" })).toBeInTheDocument();
		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("true");
		await userEvent.click(screen.getByRole("button", { name: "select reports/plan.md" }));

		expect(screen.getByTestId("artifact-view")).toHaveTextContent("plan.md:reports/plan.md");
		expect(useUiStore.getState().inspectorSessions["sess-artifacts"]?.filesSource).toEqual({ kind: "artifact" });
	});

	it("keeps artifact and workspace views available through the source switcher", async () => {
		const sessionId = "sess-artifact-switch";
		useUiStore.getState().setFilesChangedOnly(sessionId, false);
		renderWithQuery(<SessionFileExplorer artifacts={artifacts} isMaximized sessionId={sessionId} />);

		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");

		act(() => {
			useUiStore.getState().setFilesSource(sessionId, { kind: "artifact" });
		});
		await userEvent.click(screen.getByRole("button", { name: "select reports/plan.md" }));
		expect(screen.getByTestId("artifact-view")).toHaveTextContent("plan.md:reports/plan.md");

		act(() => {
			useUiStore.getState().setFilesSource(sessionId, { kind: "workspace" });
		});
		expect(screen.queryByTestId("artifact-view")).not.toBeInTheDocument();
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");

		act(() => {
			useUiStore.getState().setFilesSource(sessionId, { kind: "artifact" });
		});
		expect(screen.getByTestId("artifact-view")).toHaveTextContent("plan.md:reports/plan.md");
	});

	it("opens an externally requested artifact inside the Artifacts source", async () => {
		renderWithQuery(
			<SessionFileExplorer
				artifacts={artifacts}
				isMaximized
				revealRequest={{ path: "reports/plan.md", key: 1, source: "artifact" }}
				sessionId="sess-artifact-reveal"
			/>,
		);

		expect(await screen.findByTestId("artifact-view")).toHaveTextContent("plan.md:reports/plan.md");
		expect(useUiStore.getState().inspectorSessions["sess-artifact-reveal"]?.filesSource).toEqual({ kind: "artifact" });
	});

	it("switches back to Workspace when a workspace file is revealed after viewing artifacts", async () => {
		const sessionId = "sess-workspace-reveal";
		useUiStore.getState().setFilesSource(sessionId, { kind: "artifact" });
		const onOpenFile = vi.fn();
		renderWithQuery(
			<SessionFileExplorer
				artifacts={artifacts}
				onOpenFile={onOpenFile}
				revealRequest={{ path: "src/App.tsx", key: 1 }}
				sessionId={sessionId}
			/>,
		);

		await waitFor(() => {
			expect(useUiStore.getState().inspectorSessions[sessionId]?.filesSource).toEqual({ kind: "workspace" });
		});
		expect(screen.queryByTestId("artifact-view")).not.toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { mode: "file" });
	});

	it("defaults to the continuous changes review and can switch to the full file tree", async () => {
		const sessionId = "sess-explorer-2";
		renderWithQuery(<SessionFileExplorer sessionId={sessionId} />);

		expect(await screen.findByTestId("review-pane")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("tab", { name: "Files" }));

		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("false");
		expect(useUiStore.getState().inspectorSessions[sessionId]?.filesChangedOnly).toBe(false);
	});

	it("defaults to the file tree when the workspace has no changes", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-clean",
				files: [],
				sections: { committed: [], staged: [], unstaged: [], untracked: [] },
				commits: [],
				summary: { additions: 0, deletions: 0, files: 0 },
				truncated: false,
				workspaceVersion: "clean-1",
			},
		});
		renderWithQuery(<SessionFileExplorer sessionId="sess-clean" />);

		expect(await screen.findByTestId("tree-changed-only")).toHaveTextContent("false");
		expect(screen.queryByTestId("review-pane")).not.toBeInTheDocument();
		expect(screen.queryByRole("tab", { name: "Changes" })).not.toBeInTheDocument();
		expect(screen.queryByRole("tab", { name: "Files" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Split diff view" })).not.toBeInTheDocument();
	});

	it("switches to an associated PR without changing the workspace", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return { data: { sessionId: "sess-pr", prs: [{ number: 42, url: "https://example.test/pr/42", sourceBranch: "feature/files", title: "Files" }] } };
			}
			return {
				data: {
					sessionId: "sess-pr",
					files: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 0, size: 10, binary: false }],
					truncated: false,
				},
			};
		});
		renderWithQuery(<SessionFileExplorer sessionId="sess-pr" />);

		await userEvent.click(screen.getByRole("combobox", { name: "File source" }));
		await userEvent.click(await screen.findByRole("option", { name: "PR #42 · feature/files" }));

		expect(screen.getByText("PR #42 · feature/files", { selector: "div" })).toBeInTheDocument();
		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("true");
		expect(getMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/pr/{prNumber}/files",
			expect.objectContaining({
				params: {
					path: { sessionId: "sess-pr", prNumber: 42 },
					query: { sourceUrl: "https://example.test/pr/42" },
				},
			}),
		);
	});

	it("refreshes PR files when the observed head changes", async () => {
		const sessionId = "sess-pr-refresh";
		const url = "https://example.test/pr/42";
		useUiStore.getState().setFilesSource(sessionId, { kind: "pull_request", number: 42, url, label: "PR #42 · files" });
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return { data: { sessionId, prs: [{ headSha: "head-1", number: 42, url, sourceBranch: "files", title: "Files" }] } };
			}
			return { data: { sessionId, files: [], truncated: false } };
		});
		const { client } = renderWithQuery(<SessionFileExplorer sessionId={sessionId} />);

		await waitFor(() => expect(client.getQueryData(["session-source-files", sessionId, "pull_request", url, "head-1"])).toBeDefined());
		client.setQueryData(["session-scm-summary", sessionId], [{ headSha: "head-2", number: 42, url, sourceBranch: "files", title: "Files" }]);
		await waitFor(() => expect(client.getQueryData(["session-source-files", sessionId, "pull_request", url, "head-2"])).toBeDefined());
	});

	it("passes a renamed file's previous path to the PR detail request", async () => {
		const sessionId = "sess-pr-rename";
		const url = "https://example.test/pr/42";
		useUiStore.getState().setFilesSource(sessionId, { kind: "pull_request", number: 42, url, label: "PR #42 · files" });
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return { data: { sessionId, prs: [{ headSha: "head-1", number: 42, url, sourceBranch: "files", title: "Files" }] } };
			}
			return { data: { sessionId, files: [{ path: "src/App.tsx", previousPath: "src/OldApp.tsx", status: "renamed", additions: 0, deletions: 0, size: 10, binary: false }], truncated: false } };
		});
		renderWithQuery(<SessionFileExplorer isMaximized sessionId={sessionId} />);

		await userEvent.click(await screen.findByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-previous-path", "src/OldApp.tsx");
	});

	it("selects duplicate PR numbers by URL and preserves the source across remounts", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return { data: { sessionId: "sess-duplicate-pr", prs: [
					{ number: 42, url: "https://github.example/acme/app/pull/42", sourceBranch: "upstream", title: "Upstream" },
					{ number: 42, url: "https://gitlab.example/acme/app/-/merge_requests/42", sourceBranch: "canonical", title: "Canonical" },
				] } };
			}
			return { data: { sessionId: "sess-duplicate-pr", files: [], truncated: false } };
		});
		const first = renderWithQuery(<SessionFileExplorer sessionId="sess-duplicate-pr" />);

		await userEvent.click(screen.getByRole("combobox", { name: "File source" }));
		await userEvent.click(await screen.findByRole("option", { name: "PR #42 · canonical" }));
		await waitFor(() => expect(getMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/pr/{prNumber}/files",
			expect.objectContaining({ params: { path: { sessionId: "sess-duplicate-pr", prNumber: 42 }, query: { sourceUrl: "https://gitlab.example/acme/app/-/merge_requests/42" } } }),
		));

		first.unmount();
		renderWithQuery(<SessionFileExplorer isMaximized sessionId="sess-duplicate-pr" />);
		expect((await screen.findAllByText("PR #42 · canonical")).length).toBeGreaterThan(0);
		expect(useUiStore.getState().inspectorSessions["sess-duplicate-pr"]?.filesSource).toEqual({
			kind: "pull_request",
			label: "PR #42 · canonical",
			number: 42,
			url: "https://gitlab.example/acme/app/-/merge_requests/42",
		});
	});

	it("keeps the continuous right-side diff visible when opening the full file in center", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-review-navigation" />);

		await userEvent.click(await screen.findByRole("button", { name: "Open full file" }));
		expect(screen.getByTestId("review-pane")).toBeInTheDocument();
		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { mode: "file" });
	});

	it("opens a changed file diff in the center workspace", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-review-center" />);

		await userEvent.click(await screen.findByRole("button", { name: "Open diff in center" }));
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { mode: "diff" });
	});

	it("opens a review diff action in the syntax-aware file editor", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-review-edit" />);

		await userEvent.click(await screen.findByRole("button", { name: "Edit src/App.tsx" }));
		expect(screen.getByTestId("review-pane")).toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { editing: true, mode: "file" });
	});

	it("opens the direct rendered action in the center while retaining the diff", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-review-rendered" />);
		await userEvent.click(await screen.findByRole("button", { name: "Render README.md" }));
		expect(screen.getByTestId("review-pane")).toBeInTheDocument();
		expect(onOpenFile).toHaveBeenCalledWith("README.md", { mode: "rendered" });
	});

	it("always wraps file content and does not expose a wrap toggle", async () => {
		renderWithQuery(<SessionFileExplorer sessionId="sess-wrap" />);
		expect(await screen.findByTestId("review-pane")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Wrap lines" })).not.toBeInTheDocument();
	});

	it("toggles between unified and split diff layout", async () => {
		renderWithQuery(<SessionFileExplorer sessionId="sess-explorer-3" />);

		const toggle = screen.getByRole("button", { name: "Split diff view" });
		expect(toggle).toHaveAttribute("aria-pressed", "false");
		await userEvent.click(toggle);
		expect(screen.getByRole("button", { name: "Unified diff view" })).toHaveAttribute("aria-pressed", "true");
	});

	it("lets the caller toggle between rail and maximized layouts", async () => {
		const onToggleMaximized = vi.fn();
		renderWithQuery(<SessionFileExplorer onToggleMaximized={onToggleMaximized} sessionId="sess-explorer-4" />);

		await userEvent.click(screen.getByRole("button", { name: "Maximize files" }));
		expect(onToggleMaximized).toHaveBeenCalledWith(true);
	});
});
