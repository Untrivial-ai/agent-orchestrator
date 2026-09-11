import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionFileExplorer } from "./SessionFileExplorer";
import { TooltipProvider } from "./ui/tooltip";
import { useUiStore } from "../stores/ui-store";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

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
		filterText,
		onSelectPath,
	}: {
		changedOnly: boolean;
		filterText: string;
		onSelectPath: (node: { path: string; type: "file" }) => void;
	}) => {
		const [expanded, setExpanded] = useState(false);
		return <div>
			<span data-testid="tree-changed-only">{String(changedOnly)}</span>
			<span data-testid="tree-filter">{filterText}</span>
			<button onClick={() => setExpanded((current) => !current)} type="button">expand src</button>
			{expanded ? <span>src directory expanded</span> : null}
			<button onClick={() => onSelectPath({ path: "src/App.tsx", type: "file" })} type="button">
				select src/App.tsx
			</button>
		</div>;
	},
}));

vi.mock("./FileContentPane", () => ({
	FileContentPane: ({ initialEditing, initialMode, path }: { initialEditing?: boolean; initialMode?: string; path: string | null }) => <div data-editing={String(Boolean(initialEditing))} data-mode={initialMode ?? "default"} data-testid="content-pane">{path ?? "none"}</div>,
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
