import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileTree } from "./FileTree";
import type { TreeNode } from "../hooks/useSessionWorkspaceTree";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		return fallback;
	},
}));

function renderWithQuery(children: ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
	// FileTree only mounts react-arborist's <Tree> once its container has a
	// measured size; the test setup's ResizeObserver stub never invokes its
	// callback, so drive it here the same way XtermTerminal.test.tsx does.
	act(() => {
		for (const callback of resizeCallbacks) callback([{ contentRect: { width: 400, height: 400 } }] as never, {} as ResizeObserver);
	});
	return view;
}

const resizeCallbacks: ResizeObserverCallback[] = [];

class CapturingResizeObserver implements ResizeObserver {
	constructor(callback: ResizeObserverCallback) {
		resizeCallbacks.push(callback);
	}
	disconnect() {}
	observe() {}
	unobserve() {}
}

function treeResponse(path: string, entries: unknown[]) {
	return { data: { sessionId: "sess-1", path, entries, truncated: false } };
}

describe("FileTree", () => {
	beforeEach(() => {
		resizeCallbacks.length = 0;
		getMock.mockReset();
		Object.defineProperty(window, "ResizeObserver", { configurable: true, writable: true, value: CapturingResizeObserver });
	});

	it("lists the root directory's entries", async () => {
		getMock.mockResolvedValue(
			treeResponse("", [
				{ name: "src", path: "src", type: "dir", hasChanges: true },
				{ name: "README.md", path: "README.md", type: "file", status: "unmodified" },
			]),
		);

		renderWithQuery(
			<FileTree changedOnly={false} changedOnlyData={[]} onSelectPath={vi.fn()} selectedPath={null} sessionId="sess-1" filterText="" />,
		);

		expect(await screen.findByText("src")).toBeInTheDocument();
		expect(screen.getByText("README.md")).toBeInTheDocument();
		expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/tree", {
			params: { path: { sessionId: "sess-1" }, query: {} },
		});
		expect(screen.getByRole("tree", { name: "File tree" }).firstElementChild).toHaveClass("board-scrollbar");
	});

	it("renders distinct technology icons from file and folder names", async () => {
		getMock.mockResolvedValue(
			treeResponse("", [
				{ name: "src", path: "src", type: "dir", hasChanges: false },
				{ name: "App.tsx", path: "App.tsx", type: "file", status: "unmodified" },
				{ name: "README.md", path: "README.md", type: "file", status: "unmodified" },
			]),
		);

		renderWithQuery(
			<FileTree changedOnly={false} changedOnlyData={[]} onSelectPath={vi.fn()} selectedPath={null} sessionId="sess-1" filterText="" />,
		);

		const sourceFolderIcon = await screen.findByTestId("folder-icon-src");
		const reactFileIcon = screen.getByTestId("file-icon-App.tsx");
		const markdownFileIcon = screen.getByTestId("file-icon-README.md");
		expect(sourceFolderIcon.tagName).toBe("svg");
		expect(reactFileIcon.innerHTML).not.toBe(markdownFileIcon.innerHTML);
	});

	it("lazily fetches a directory's children on expand", async () => {
		getMock.mockImplementation(async (_path: string, options: unknown) => {
			const query = (options as { params?: { query?: { path?: string } } }).params?.query?.path;
			if (!query) return treeResponse("", [{ name: "src", path: "src", type: "dir", hasChanges: false }]);
			if (query === "src") {
				return treeResponse("src", [{ name: "app.go", path: "src/app.go", type: "file", status: "unmodified" }]);
			}
			return treeResponse(query, []);
		});

		renderWithQuery(
			<FileTree changedOnly={false} changedOnlyData={[]} onSelectPath={vi.fn()} selectedPath={null} sessionId="sess-1" filterText="" />,
		);

		await userEvent.click(await screen.findByText("src"));

		await waitFor(() =>
			expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/tree", {
				params: { path: { sessionId: "sess-1" }, query: { path: "src" } },
			}),
		);
		expect(await screen.findByText("app.go")).toBeInTheDocument();
	});

	it("calls onSelectPath when a file row is activated", async () => {
		getMock.mockResolvedValue(treeResponse("", [{ name: "README.md", path: "README.md", type: "file", status: "modified" }]));
		const onSelectPath = vi.fn();

		renderWithQuery(
			<FileTree changedOnly={false} changedOnlyData={[]} onSelectPath={onSelectPath} selectedPath={null} sessionId="sess-1" filterText="" />,
		);

		await userEvent.click(await screen.findByText("README.md"));
		expect(onSelectPath).toHaveBeenCalledWith(expect.objectContaining({ path: "README.md", type: "file" }));
	});

	it("loads nested directories while searching before they have been expanded", async () => {
		getMock.mockImplementation(async (_path: string, options: unknown) => {
			const query = (options as { params?: { query?: { path?: string } } }).params?.query?.path;
			if (!query) return treeResponse("", [{ name: "src", path: "src", type: "dir", hasChanges: false }]);
			if (query === "src") {
				return treeResponse("src", [{ name: "nested", path: "src/nested", type: "dir", hasChanges: false }]);
			}
			if (query === "src/nested") {
				return treeResponse("src/nested", [{ name: "target.ts", path: "src/nested/target.ts", type: "file", status: "unmodified" }]);
			}
			return treeResponse(query, []);
		});

		renderWithQuery(
			<FileTree changedOnly={false} changedOnlyData={[]} onSelectPath={vi.fn()} selectedPath={null} sessionId="sess-1" filterText="target" />,
		);

		expect(await screen.findByText("target.ts")).toBeInTheDocument();
		expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/tree", {
			params: { path: { sessionId: "sess-1" }, query: { path: "src/nested" } },
		});
	});

	it("renders the precomputed changed-only tree without calling the tree endpoint", async () => {
		const changedOnlyData: TreeNode[] = [{ name: "notes.txt", path: "notes.txt", type: "file", status: "added" }];

		renderWithQuery(
			<FileTree
				changedOnly={true}
				changedOnlyData={changedOnlyData}
				onSelectPath={vi.fn()}
				selectedPath={null}
				sessionId="sess-1"
				filterText=""
			/>,
		);

		expect(await screen.findByText("notes.txt")).toBeInTheDocument();
		expect(getMock).not.toHaveBeenCalled();
	});
});
