import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionFileExplorer } from "./SessionFileExplorer";
import { FilesTopbarHostContext } from "./files-topbar-host";
import { TooltipProvider } from "./ui/tooltip";
import { useUiStore } from "../stores/ui-store";
import { sessionScmSummaryQueryKey } from "../hooks/useSessionScmSummary";
import { sessionUiKey } from "../lib/hosts";
import type { TreeNode } from "../hooks/useSessionWorkspaceTree";

const { getMock, postMock, hostAGetMock, hostBGetMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn(), hostAGetMock: vi.fn(), hostBGetMock: vi.fn() }));

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

vi.mock("../lib/host-clients", () => ({
	clientForHost: (hostId: string) => {
		if (hostId === "host-a") return { GET: hostAGetMock };
		if (hostId === "host-b") return { GET: hostBGetMock };
		throw new Error(`Host ${hostId} is not connected`);
	},
	clientForSessionHost: (hostId?: string) => {
		if (!hostId) return { GET: getMock, POST: postMock };
		if (hostId === "host-a") return { GET: hostAGetMock };
		if (hostId === "host-b") return { GET: hostBGetMock };
		throw new Error(`Host ${hostId} is not connected`);
	},
	baseUrlForHost: (hostId: string) => hostId === "host-a" || hostId === "host-b" ? `http://127.0.0.1:4000/${hostId}` : undefined,
	connectedHost: (hostId: string) => hostId === "host-a" || hostId === "host-b" ? { base: `http://127.0.0.1:4000/${hostId}` } : undefined,
	isQuickTunnelHost: () => false,
	subscribeConnectedHosts: () => () => undefined,
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
		changedOnlyData: TreeNode[];
		forceChangedOnly?: boolean;
		filterText: string;
		onSelectPath: (node: { path: string; type: "file" }) => void;
	}) => {
		const [expanded, setExpanded] = useState(false);
		const filePaths = (nodes: TreeNode[]): string[] => nodes.flatMap((node) => node.children ? filePaths(node.children) : [node.path]);
		return <div>
			<span data-testid="tree-changed-only">{String(changedOnly || forceChangedOnly)}</span>
			<span data-testid="tree-files">{filePaths(changedOnlyData).join(" ")}</span>
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
	FileContentPane: ({ commitSha, initialEditing, initialMode, path, previousPath, split }: { commitSha?: string; initialEditing?: boolean; initialMode?: string; path: string | null; previousPath?: string; split?: boolean }) => <div data-commit-sha={commitSha ?? ""} data-editing={String(Boolean(initialEditing))} data-mode={initialMode ?? "default"} data-previous-path={previousPath} data-split={String(Boolean(split))} data-testid="content-pane">{path ?? "none"}</div>,
}));

vi.mock("./diffs/WorkspaceReviewPane", () => ({
	WorkspaceReviewPane: ({ canOpenInCenter = true, filter, onBrowseAll, onOpenFile }: { canOpenInCenter?: boolean; filter: string; onBrowseAll: () => void; onOpenFile?: (path: string, options?: { editing?: boolean; mode?: "diff" | "file" | "rendered" }) => void }) => (
		<div data-testid="review-pane">
			<span data-testid="review-filter">{filter}</span>
			<button onClick={onBrowseAll} type="button">Browse all files</button>
			<button onClick={() => onOpenFile?.("src/App.tsx", { mode: "file" })} type="button">Open full file</button>
			<button onClick={() => onOpenFile?.("README.md", { mode: "rendered" })} type="button">Render README.md</button>
			<button onClick={() => onOpenFile?.("src/App.tsx", { editing: true, mode: "file" })} type="button">Edit src/App.tsx</button>
			{canOpenInCenter ? <button onClick={() => onOpenFile?.("src/App.tsx", { mode: "diff" })} type="button">Open diff in center</button> : null}
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
		hostAGetMock.mockReset();
		hostBGetMock.mockReset();
	});

	it("keeps same-id remote file state on A and B separate while opening center tabs", async () => {
		const response = (content: string) => ({
			data: {
				sessionId: "same-id",
				files: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 0, size: 10, binary: false, content }],
				sections: { committed: [], staged: [], unstaged: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 0, size: 10, binary: false }], untracked: [] },
				commits: [], summary: { additions: 1, deletions: 0, files: 1 }, truncated: false, workspaceVersion: content,
			},
		});
		hostAGetMock.mockImplementation(async (path: string) => path.endsWith("/pr") ? { data: { prs: [] } } : response("A"));
		hostBGetMock.mockImplementation(async (path: string) => path.endsWith("/pr") ? { data: { prs: [] } } : response("B"));
		const openOnA = vi.fn();
		const openOnB = vi.fn();
		const { client } = renderWithQuery(<>
			<SessionFileExplorer hostId="host-a" onOpenFile={openOnA} sessionId="same-id" />
			<SessionFileExplorer hostId="host-b" onOpenFile={openOnB} sessionId="same-id" />
		</>);
		await waitFor(() => {
			expect(client.getQueryData(["session-workspace-files", "host-a", "same-id"])).toMatchObject({ workspaceVersion: "A" });
			expect(client.getQueryData(["session-workspace-files", "host-b", "same-id"])).toMatchObject({ workspaceVersion: "B" });
		});
		const [a, b] = screen.getAllByRole("region", { name: "Session files" });
		await userEvent.click(within(a).getByRole("tab", { name: "Files" }));
		expect(useUiStore.getState().inspectorSessions[sessionUiKey("same-id", "host-a")]?.filesChangedOnly).toBe(false);
		expect(useUiStore.getState().inspectorSessions[sessionUiKey("same-id", "host-b")]?.filesChangedOnly).toBeUndefined();
		await userEvent.click(within(a).getByRole("button", { name: "select src/App.tsx" }));
		expect(openOnA).toHaveBeenCalledWith("src/App.tsx", { mode: "file" });
		expect(openOnB).not.toHaveBeenCalled();
		expect(within(a).queryByTestId("content-pane")).not.toBeInTheDocument();
		expect(within(b).queryByTestId("content-pane")).not.toBeInTheDocument();
		expect(getMock).not.toHaveBeenCalled();
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

	it("reports a reveal handled once it opens the file, and keeps it pending while maximized", () => {
		const onOpenFile = vi.fn();
		const onRevealHandled = vi.fn();
		const reveal = { path: "docs/notes.txt", key: 3 };
		const { client, rerender } = renderWithQuery(
			<SessionFileExplorer isMaximized onOpenFile={onOpenFile} onRevealHandled={onRevealHandled} revealRequest={reveal} sessionId="sess-explorer-reveal-once" />,
		);
		// Maximized, the file shows in this view's own preview; the request stays pending.
		expect(onOpenFile).not.toHaveBeenCalled();
		expect(onRevealHandled).not.toHaveBeenCalled();

		rerender(
			<QueryClientProvider client={client}>
				<TooltipProvider>
					<SessionFileExplorer onOpenFile={onOpenFile} onRevealHandled={onRevealHandled} revealRequest={reveal} sessionId="sess-explorer-reveal-once" />
				</TooltipProvider>
			</QueryClientProvider>,
		);
		expect(onOpenFile).toHaveBeenCalledOnce();
		expect(onOpenFile).toHaveBeenCalledWith("docs/notes.txt", { mode: "file" });
		expect(onRevealHandled).toHaveBeenCalledOnce();
		expect(onRevealHandled).toHaveBeenCalledWith(3);
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

		// Preview first, tree on the right.
		const panels = container.querySelectorAll('[data-slot="resizable-panel"]');
		expect(panels).toHaveLength(2);
		expect(panels[0]).toHaveStyle({ flexGrow: "74" });
		expect(panels[1]).toHaveStyle({ flexGrow: "26" });

		// With the view tabs showing, the active Files tab doubles as the tree toggle.
		await userEvent.click(screen.getByRole("tab", { name: "Hide file tree" }));
		expect(screen.queryByTestId("tree-changed-only")).not.toBeInTheDocument();
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
		await userEvent.click(screen.getByRole("tab", { name: "Show file tree" }));
		expect(screen.getByTestId("tree-changed-only")).toBeInTheDocument();
		widthSpy.mockRestore();
	});

	it("keeps the header in place when switching between the Changes and Files views", async () => {
		renderWithQuery(<SessionFileExplorer isMaximized onToggleMaximized={vi.fn()} sessionId="sess-explorer-steady" />);

		expect(await screen.findByTestId("review-pane")).toBeInTheDocument();
		const header = screen.getByRole("tab", { name: "Changes" }).closest("header");
		const changesHeaderClass = header?.className;
		await userEvent.click(screen.getByRole("tab", { name: "Files" }));

		// The split's divider is drawn on the content, not by growing the header.
		expect(screen.getByTestId("content-pane")).toBeInTheDocument();
		expect(header?.className).toBe(changesHeaderClass);
		expect(header?.nextElementSibling).toHaveClass("border-t");
	});

	it("renders the maximized filter into the overlay titlebar it is given", () => {
		const titlebar = document.createElement("div");
		document.body.append(titlebar);
		renderWithQuery(
			<FilesTopbarHostContext.Provider value={titlebar}>
				<SessionFileExplorer isMaximized onToggleMaximized={vi.fn()} sessionId="sess-explorer-titlebar" />
			</FilesTopbarHostContext.Provider>,
		);

		const filter = screen.getByRole("textbox", { name: "Filter files" });
		expect(titlebar).toContainElement(filter);
		expect(screen.getByRole("button", { name: "Minimize files" }).closest("header")).not.toContainElement(filter);
		titlebar.remove();
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
		// Nothing to review and no PR: the picker's only entry would be Workspace.
		expect(screen.queryByRole("button", { name: "File source" })).not.toBeInTheDocument();
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
		expect(await screen.findByRole("tablist", { name: "File view" })).toBeInTheDocument();

		// On the workspace the trigger already names it, so there is no Workspace button,
		// and Branch › lists only PRs because Workspace is not a branch.
		expect(screen.queryByRole("button", { name: "Workspace" })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "File source" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Branch" }));
		const prItem = await screen.findByRole("menuitem", { name: "PR #42 · feature/files" });
		expect(screen.queryByRole("menuitem", { name: "Workspace" })).not.toBeInTheDocument();
		await userEvent.click(prItem);

		expect(screen.getByRole("button", { name: "File source" })).toHaveTextContent("PR #42 · feature/files");
		expect(screen.getByRole("button", { name: "Workspace" })).toBeInTheDocument();
		// The Changes view is workspace-only, so a PR source hides the switch.
		expect(screen.queryByRole("tablist", { name: "File view" })).not.toBeInTheDocument();
		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("true");
		// The preview beside the tree gets the same unified/split switch as Changes,
		// and follows it at any width.
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-split", "false");
		await userEvent.click(screen.getByRole("button", { name: "Split diff view" }));
		expect(screen.getByRole("button", { name: "Unified diff view" })).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-split", "true");
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

	it("lets the user return to Workspace while an active PR has no SCM summary yet", async () => {
		const sessionId = "sess-pr-scm-loading";
		const url = "https://example.test/pr/42";
		useUiStore.getState().setFilesSource(sessionId, { kind: "pull_request", number: 42, url, label: "PR #42 · files" });
		getMock.mockImplementation((path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") return new Promise(() => {});
			return Promise.resolve({ data: { sessionId, files: [], truncated: false } });
		});
		renderWithQuery(<SessionFileExplorer sessionId={sessionId} />);

		// Workspace is a button before the picker; the picker itself has nothing
		// to list until the PR summary arrives, so it stays hidden.
		const workspaceButton = await screen.findByRole("button", { name: "Workspace" });
		expect(screen.queryByRole("button", { name: "File source" })).not.toBeInTheDocument();
		await userEvent.click(workspaceButton);

		expect(useUiStore.getState().inspectorSessions[sessionId]?.filesSource).toEqual({ kind: "workspace" });
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
		client.setQueryData(sessionScmSummaryQueryKey(sessionId), { prs: [{ headSha: "head-2", number: 42, url, sourceBranch: "files", title: "Files" }], linkedPrs: [] });
		await waitFor(() => expect(client.getQueryData(["session-source-files", sessionId, "pull_request", url, "head-2"])).toBeDefined());
	});

	it("lists a PR's own commits and narrows the tree and preview to the one picked", async () => {
		const sessionId = "sess-pr-commits";
		const url = "https://example.test/pr/42";
		useUiStore.getState().setFilesSource(sessionId, { kind: "pull_request", number: 42, url, label: "PR #42 · files" });
		const readme = { path: "README.md", status: "modified", additions: 1, deletions: 1, size: 0, binary: false };
		const notes = { path: "docs/notes.md", status: "added", additions: 2, deletions: 0, size: 0, binary: false };
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return { data: { sessionId, prs: [{ headSha: "head-1", number: 42, url, sourceBranch: "files", title: "Files" }] } };
			}
			return {
				data: {
					sessionId,
					files: [readme, notes],
					commits: [
						{ sha: "bbbbbbb2222222", subject: "docs: add notes", author: "Ada", timestamp: "2026-09-26T10:00:00Z", files: [notes] },
						{ sha: "aaaaaaa1111111", subject: "docs: update readme", author: "Ada", timestamp: "2026-09-26T09:00:00Z", files: [readme] },
					],
					truncated: false,
				},
			};
		});
		renderWithQuery(<SessionFileExplorer sessionId={sessionId} />);

		await waitFor(() => expect(screen.getByTestId("tree-files")).toHaveTextContent("docs/notes.md"));
		expect(screen.getByTestId("tree-files")).toHaveTextContent("README.md");
		// Opened from the keyboard: in jsdom every box sits at 0,0, so the split's
		// resize handle claims (preventDefaults) any pointerdown, including the
		// one on the picker.
		const openPicker = async () => {
			screen.getByRole("button", { name: "File source" }).focus();
			await userEvent.keyboard("{Enter}");
		};
		await openPicker();
		await userEvent.click(await screen.findByRole("menuitem", { name: "Commits" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: /docs: update readme/ }));

		expect(screen.getByRole("button", { name: "File source" })).toHaveTextContent("aaaaaaa");
		expect(screen.getByTestId("tree-files")).toHaveTextContent("README.md");
		expect(screen.getByTestId("tree-files")).not.toHaveTextContent("docs/notes.md");
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-commit-sha", "aaaaaaa1111111");

		// Changes goes back to the whole pull request.
		await openPicker();
		await userEvent.click(await screen.findByRole("menuitem", { name: "Changes" }));
		expect(screen.getByTestId("tree-files")).toHaveTextContent("docs/notes.md");
		expect(screen.getByTestId("content-pane")).toHaveAttribute("data-commit-sha", "");
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

		// The picker appears once the PR list gives it something to switch to.
		await userEvent.click(await screen.findByRole("button", { name: "File source" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Branch" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "PR #42 · canonical" }));
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

	it("edits a review file in place when maximized instead of the hidden center", async () => {
		const onOpenFile = vi.fn();
		renderWithQuery(<SessionFileExplorer isMaximized onOpenFile={onOpenFile} sessionId="sess-review-edit-maximized" />);

		await userEvent.click(await screen.findByRole("button", { name: "Edit src/App.tsx" }));
		const pane = await screen.findByTestId("content-pane");
		expect(pane).toHaveTextContent("src/App.tsx");
		expect(pane).toHaveAttribute("data-editing", "true");
		expect(pane).toHaveAttribute("data-mode", "file");
		expect(onOpenFile).not.toHaveBeenCalled();
	});

	it("hides open-in-center when maximized, since the overlay covers the center pane", async () => {
		renderWithQuery(<SessionFileExplorer isMaximized onOpenFile={vi.fn()} sessionId="sess-review-center-maximized" />);

		expect(await screen.findByRole("button", { name: "Edit src/App.tsx" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Open diff in center" })).not.toBeInTheDocument();
	});

	it("opens the rich preview in place when maximized", async () => {
		renderWithQuery(<SessionFileExplorer isMaximized sessionId="sess-review-rendered-maximized" />);

		await userEvent.click(await screen.findByRole("button", { name: "Render README.md" }));
		const pane = await screen.findByTestId("content-pane");
		expect(pane).toHaveTextContent("README.md");
		expect(pane).toHaveAttribute("data-mode", "rendered");
		expect(pane).toHaveAttribute("data-editing", "false");
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
