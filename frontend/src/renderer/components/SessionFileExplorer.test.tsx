import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
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
	}) => (
		<div>
			<span data-testid="tree-changed-only">{String(changedOnly)}</span>
			<span data-testid="tree-filter">{filterText}</span>
			<button onClick={() => onSelectPath({ path: "src/App.tsx", type: "file" })} type="button">
				select src/App.tsx
			</button>
		</div>
	),
}));

vi.mock("./FileContentPane", () => ({
	FileContentPane: ({ path }: { path: string | null }) => <div data-testid="content-pane">{path ?? "none"}</div>,
}));

vi.mock("./diffs/WorkspaceReviewPane", () => ({
	WorkspaceReviewPane: ({ filter, onBrowseAll }: { filter: string; onBrowseAll: () => void }) => (
		<div data-testid="review-pane">
			<span data-testid="review-filter">{filter}</span>
			<button onClick={onBrowseAll} type="button">Browse all files</button>
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
				files: [],
				sections: { committed: [], staged: [], unstaged: [], untracked: [] },
				commits: [],
				summary: { additions: 0, deletions: 0, files: 0 },
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
		await userEvent.click(screen.getByRole("switch", { name: "Changed only" }));

		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("false");
		expect(useUiStore.getState().inspectorSessions[sessionId]?.filesChangedOnly).toBe(false);
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
