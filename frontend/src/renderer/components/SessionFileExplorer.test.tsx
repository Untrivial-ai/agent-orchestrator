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
	WorkspaceReviewPane: ({ filter, onBrowseAll, onEditFile, onOpenFile, onOpenFileInCenter }: { filter: string; onBrowseAll: () => void; onEditFile?: (path: string) => void; onOpenFile: (path: string, mode?: "diff" | "file" | "rendered") => void; onOpenFileInCenter?: (path: string) => void }) => (
		<div data-testid="review-pane">
			<span data-testid="review-filter">{filter}</span>
			<button onClick={onBrowseAll} type="button">Browse all files</button>
			<button onClick={() => onOpenFile("src/App.tsx", "diff")} type="button">Review src/App.tsx</button>
			<button onClick={() => onOpenFile("README.md", "rendered")} type="button">Render README.md</button>
			<button onClick={() => onEditFile?.("src/App.tsx")} type="button">Edit src/App.tsx</button>
			<button onClick={() => onOpenFileInCenter?.("src/App.tsx")} type="button">Open diff in center</button>
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

	it("passes the filter input down to the tree and shows the selected file in the content pane", async () => {
		useUiStore.getState().setFilesChangedOnly("sess-explorer-1", false);
		renderWithQuery(<SessionFileExplorer sessionId="sess-explorer-1" />);

		const input = screen.getByRole("textbox", { name: "Filter files" });
		fireEvent.change(input, { target: { value: "app" } });
		expect(screen.getByTestId("tree-filter")).toHaveTextContent("app");

		// Docked (non-maximized): tree and content are master/detail, not side by
		// side, so the content pane isn't mounted at all until a file is picked.
		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
	});

	it("returns to the tree when the back button is pressed, docked", async () => {
		useUiStore.getState().setFilesChangedOnly("sess-explorer-back", false);
		renderWithQuery(<SessionFileExplorer sessionId="sess-explorer-back" />);

		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Back to file tree" }));
		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		expect(screen.getByTestId("tree-changed-only")).toBeInTheDocument();
	});

	it("preserves expanded parent directories when returning from a docked file", async () => {
		useUiStore.getState().setFilesChangedOnly("sess-explorer-parent", false);
		renderWithQuery(<SessionFileExplorer sessionId="sess-explorer-parent" />);

		await userEvent.click(screen.getByRole("button", { name: "expand src" }));
		expect(screen.getByText("src directory expanded")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		await userEvent.click(screen.getByRole("button", { name: "Back to file tree" }));

		expect(screen.getByText("src directory expanded")).toBeInTheDocument();
	});

	it("previews docked files before explicitly opening them in the center workspace", async () => {
		const onOpenFile = vi.fn();
		useUiStore.getState().setFilesChangedOnly("sess-explorer-center", false);
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-explorer-center" />);

		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
		expect(onOpenFile).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole("button", { name: "Open in center: src/App.tsx" }));
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx");
	});

	it("reveals an externally requested file in the docked preview", () => {
		const { client, rerender } = renderWithQuery(
			<SessionFileExplorer sessionId="sess-explorer-reveal" revealRequest={null} />,
		);

		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
		rerender(
			<QueryClientProvider client={client}>
				<TooltipProvider>
					<SessionFileExplorer
						revealRequest={{ path: "docs/notes.txt", key: 1 }}
						sessionId="sess-explorer-reveal"
					/>
				</TooltipProvider>
			</QueryClientProvider>,
		);

		expect(screen.getByTestId("content-pane")).toHaveTextContent("docs/notes.txt");
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
		expect(screen.getByRole("tab", { name: "Changes" })).toBeDisabled();
		expect(screen.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-selected", "true");
	});

	it("focuses a review file locally and returns to the continuous diff", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-review-navigation" />);

		await userEvent.click(await screen.findByRole("button", { name: "Review src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-mode", "diff");
		expect(onOpenFile).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole("button", { name: "Back to changes" }));
		expect(screen.getByTestId("review-pane")).toBeInTheDocument();
		expect(screen.queryByTestId("content-pane")).not.toBeInTheDocument();
	});

	it("opens a changed file diff in the center workspace", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer onOpenFile={onOpenFile} sessionId="sess-review-center" />);

		await userEvent.click(await screen.findByRole("button", { name: "Open diff in center" }));
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx");
	});

	it("opens a review diff action in the syntax-aware file editor", async () => {
		renderWithQuery(<SessionFileExplorer sessionId="sess-review-edit" />);

		await userEvent.click(await screen.findByRole("button", { name: "Edit src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-mode", "file");
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-editing", "true");
	});

	it("opens the direct rendered action inside the review pane", async () => {
		renderWithQuery(<SessionFileExplorer sessionId="sess-review-rendered" />);
		await userEvent.click(await screen.findByRole("button", { name: "Render README.md" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("README.md");
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-mode", "rendered");
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
