import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionFilesView } from "./SessionFilesView";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: getMock,
		POST: postMock,
	},
	getApiBaseUrl: () => "",
	hasTrustedApiBaseUrl: () => false,
	subscribeApiBaseUrl: () => () => undefined,
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
}));

function renderWithQuery(children: ReactNode) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
}

/**
 * @tanstack/react-virtual falls back to a 150ms scroll-end debounce when the
 * environment has no `scrollend` event (jsdom). That timer is not cancelled on
 * unmount, so it can fire after vitest tears down jsdom and React throws
 * `window is not defined` — failing the run even when every test passed.
 * Unmount first, then drain past that delay while `window` still exists.
 */
async function drainVirtualizerScrollDebounce() {
	cleanup();
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 200));
	});
}

// A diff line's content lives in a span with a `whitespace-pre*` class. Intra-line
// word highlighting splits that span into child spans, so match on the wrapper's
// full text content rather than a single text node.
function diffLine(text: string) {
	return (_content: string, element: Element | null): boolean =>
		element != null && /whitespace-pre/.test(element.className) && element.textContent === text;
}

// Climbs from a DOM node down to the nearest text node, used to build real
// Range boundaries for selection tests. `fromEnd` walks children in reverse so
// callers can find the *last* text node of a subtree (for a Range end) as well
// as the first (for a Range start) — needed because intra-line word highlights
// nest the row's code text a level or two deeper than a plain row.
function findTextNode(node: Node, fromEnd: boolean): Text | null {
	if (node.nodeType === Node.TEXT_NODE) return node as Text;
	const children = Array.from(node.childNodes);
	const ordered = fromEnd ? children.reverse() : children;
	for (const child of ordered) {
		const found = findTextNode(child, fromEnd);
		if (found) return found;
	}
	return null;
}

// Builds a real, non-collapsed browser Selection spanning the diff rows at
// data-row-index `startIndex`..`endIndex` (inclusive) within `container`,
// mirroring how a user would drag-select across rendered diff rows. jsdom
// fires `selectionchange` asynchronously (matching real browsers), so this
// also flushes that pending event before returning.
async function selectAcrossRows(container: HTMLElement, startIndex: number, endIndex: number) {
	const startRow = container.querySelector(`[data-row-index="${startIndex}"]`) as HTMLElement | null;
	const endRow = container.querySelector(`[data-row-index="${endIndex}"]`) as HTMLElement | null;
	if (!startRow || !endRow) throw new Error(`Expected diff rows at indices ${startIndex} and ${endIndex}`);
	const startText = findTextNode(startRow.querySelector("span:last-child")!, false);
	const endText = findTextNode(endRow.querySelector("span:last-child")!, true);
	if (!startText || !endText) throw new Error("Expected a text node inside the selected diff rows");

	const range = document.createRange();
	range.setStart(startText, 0);
	range.setEnd(endText, endText.textContent?.length ?? 0);
	const selection = window.getSelection();
	if (!selection) throw new Error("window.getSelection() unavailable");
	selection.removeAllRanges();
	selection.addRange(range);

	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});

	return { startRow, endRow };
}

const multiHunkDiff =
	"diff --git a/src/App.tsx b/src/App.tsx\n" +
	"index 111..222 100644\n" +
	"--- a/src/App.tsx\n" +
	"+++ b/src/App.tsx\n" +
	"@@ -1,3 +1,3 @@\n" +
	" line one\n" +
	"-line two\n" +
	"+line two changed\n" +
	" line three\n" +
	"@@ -10,3 +10,2 @@\n" +
	" line ten\n" +
	"-line eleven\n" +
	" line twelve\n";

describe("SessionFilesView", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		postMock.mockResolvedValue({ data: {} });
		window.getSelection()?.removeAllRanges();
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText: vi.fn() },
		});
		getMock.mockImplementation(async (path: string, options?: unknown) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
						data: {
							sessionId: "sess-1",
							truncated: false,
							compareBaseSha: "base-sha",
							compareBaseRef: "main",
							compareMode: "base",
							files: [
							{
								path: "src/App.tsx",
								status: "modified",
								additions: 2,
								deletions: 1,
								size: 120,
								binary: false,
							},
							{
								path: "README.md",
								status: "unmodified",
								additions: 0,
								deletions: 0,
								size: 80,
								binary: false,
							},
							{
								path: "docs/guide.md",
								status: "added",
								additions: 3,
								deletions: 0,
								size: 90,
								binary: false,
							},
						],
					},
				};
			}
			if (path === "/api/v1/sessions/{sessionId}/workspace/file") {
				const query = options as { params?: { query?: { path?: string } } };
				return {
					data: {
						sessionId: "sess-1",
						path: query.params?.query?.path ?? "src/App.tsx",
						status: "modified",
						additions: 2,
						deletions: 1,
						size: 120,
						binary: false,
						deleted: false,
						content: "const value = 1;\n",
							contentTruncated: false,
							diff: "@@\n-const value = 0;\n+const value = 1;\n",
							diffTruncated: false,
							compareBaseSha: "base-sha",
							compareBaseRef: "main",
							compareMode: "base",
						},
					};
			}
			return { data: undefined };
		});
	});

	afterEach(async () => {
		await drainVirtualizerScrollDebounce();
		vi.useRealTimers();
		window.getSelection()?.removeAllRanges();
	});

	it("shows a loading search state instead of a false zero while the first file request is pending", () => {
		getMock.mockImplementation(() => new Promise(() => {}));
		const { unmount } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		expect(screen.getByPlaceholderText("Loading files...")).toBeInTheDocument();
		expect(screen.queryByPlaceholderText("Search 0 files")).not.toBeInTheDocument();
		unmount();
	});

	it("loads the workspace files and requests detail for the selected file", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		const firstFile = await screen.findByRole("button", { name: "Expand src/App.tsx" });
		expect(screen.getByPlaceholderText("Search 2 files")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Close files" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Refresh files" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /README\.md/ })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Download src/App.tsx" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add feedback for file src/App.tsx" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Diff layout" })).not.toBeInTheDocument();
		expect(screen.queryByText("Stacked")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Refresh files" })).not.toBeInTheDocument();
		expect(screen.queryByLabelText("2 changed files")).not.toBeInTheDocument();
		expect(getMock).not.toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/file", expect.anything());

		await userEvent.click(firstFile);

		await waitFor(() =>
			expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/file", {
				params: { path: { sessionId: "sess-1" }, query: { path: "src/App.tsx" } },
			}),
		);
		expect(await screen.findByText(diffLine("const value = 1;"))).toBeInTheDocument();
	});

	it("filters and expands a changed file from the review list", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		await userEvent.type(await screen.findByPlaceholderText("Search 2 files"), "guide");
		expect(screen.queryByRole("button", { name: /src\/App\.tsx/ })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Expand docs/guide.md" }));

		await waitFor(() =>
			expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/file", {
				params: { path: { sessionId: "sess-1" }, query: { path: "docs/guide.md" } },
			}),
		);
	});

	it("keeps multiple files expanded at once", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await userEvent.click(await screen.findByRole("button", { name: "Expand docs/guide.md" }));

		expect(await screen.findByRole("button", { name: "Collapse docs/guide.md" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Collapse src/App.tsx" })).toBeInTheDocument();
	});

	it("renders previous and current paths for renamed files", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						truncated: false,
						compareMode: "base",
						files: [
							{
								path: "src/NewName.tsx",
								previousPath: "src/OldName.tsx",
								status: "renamed",
								additions: 0,
								deletions: 0,
								size: 120,
								binary: false,
							},
						],
					},
				};
			}
			return {
				data: {
					sessionId: "sess-1",
					path: "src/NewName.tsx",
					previousPath: "src/OldName.tsx",
					status: "renamed",
					additions: 0,
					deletions: 0,
					size: 120,
					binary: false,
					deleted: false,
					content: "",
					contentTruncated: false,
					diff: "",
					diffTruncated: false,
					compareMode: "base",
				},
			};
		});

		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		expect(await screen.findByText("src/OldName.tsx")).toBeInTheDocument();
		expect(screen.getByText("src/NewName.tsx")).toBeInTheDocument();
	});

	it("reports HEAD fallback when no base comparison is available", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						truncated: false,
						compareMode: "head_fallback",
						files: [],
					},
				};
			}
			return { data: undefined };
		});

		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		expect(await screen.findByText("No changes against HEAD.")).toBeInTheDocument();
		expect(screen.queryByLabelText(/changed files?$/)).not.toBeInTheDocument();
	});

	it("uses the terminal foreground color for diff content", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));

		const codePane = (await screen.findByText(diffLine("const value = 1;"))).closest(".session-files-diff-scrollbar");
		expect(codePane).toHaveClass("text-terminal-foreground");
		expect(codePane).toHaveClass("session-files-diff-scrollbar");
		expect(codePane).not.toHaveClass("text-terminal");
	});

	it("renders a real diff without git-header noise and with markers in the gutter", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						truncated: false,
						files: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 120, binary: false }],
					},
				};
			}
			return {
				data: {
					sessionId: "sess-1",
					path: "src/App.tsx",
					status: "modified",
					additions: 1,
					deletions: 1,
					size: 120,
					binary: false,
					deleted: false,
					content: "",
					contentTruncated: false,
					// CRLF endings on purpose: the parser must normalize them on every OS.
					diff: "diff --git a/src/App.tsx b/src/App.tsx\r\nindex 111..222 100644\r\n--- a/src/App.tsx\r\n+++ b/src/App.tsx\r\n@@ -1,2 +1,2 @@\r\n context line\r\n-old line\r\n+new line\r\n",
					diffTruncated: false,
				},
			};
		});

		renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));

		// Content renders without the leading +/- marker (it lives in the gutter).
		expect(await screen.findByText(diffLine("new line"))).toBeInTheDocument();
		expect(screen.getByText(diffLine("old line"))).toBeInTheDocument();
		expect(screen.getByText("context line")).toBeInTheDocument();
		// Hunk header stays; git file-header lines are hidden.
		expect(screen.getByText("@@ -1,2 +1,2 @@")).toBeInTheDocument();
		expect(screen.queryByText("diff --git a/src/App.tsx b/src/App.tsx")).not.toBeInTheDocument();
		expect(screen.queryByText("index 111..222 100644")).not.toBeInTheDocument();
		expect(screen.queryByText("+++ b/src/App.tsx")).not.toBeInTheDocument();
		expect(screen.queryByText("+new line")).not.toBeInTheDocument();
		expect(screen.queryByText("-old line")).not.toBeInTheDocument();
	});

	it("wraps long diff lines by default without a toggle", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		expect(await screen.findByText(diffLine("const value = 1;"))).toHaveClass("whitespace-pre-wrap");
		expect(screen.queryByRole("button", { name: "Wrap long lines" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Disable line wrapping" })).not.toBeInTheDocument();
	});

	it("highlights only the changed tokens within a replaced line", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						truncated: false,
						files: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 1, size: 120, binary: false }],
					},
				};
			}
			return {
				data: {
					sessionId: "sess-1",
					path: "src/App.tsx",
					status: "modified",
					additions: 1,
					deletions: 1,
					size: 120,
					binary: false,
					deleted: false,
					content: "",
					contentTruncated: false,
					diff: "@@ -1,1 +1,1 @@\n-const value = 0;\n+const value = 1;\n",
					diffTruncated: false,
				},
			};
		});

		const { container } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await screen.findByText(diffLine("const value = 1;"));

		// Only the differing token is highlighted on each side, not the whole line.
		expect(container.querySelector('[class*="bg-success/35"]')?.textContent).toBe("1");
		expect(container.querySelector('[class*="bg-error/35"]')?.textContent).toBe("0");
	});

	it("switches between unified and side-by-side split diff", async () => {
		const { container } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await screen.findByText(diffLine("const value = 1;"));
		expect(container.querySelector(".grid-cols-2")).toBeNull();

		const unifiedToggle = screen.getByRole("button", { name: "Split diff view" });
		expect(unifiedToggle).toHaveAttribute("aria-pressed", "false");
		expect(unifiedToggle.querySelector(".lucide-rows-3")).not.toBeNull();
		await userEvent.click(unifiedToggle);
		expect(container.querySelector(".grid-cols-2")).not.toBeNull();
		// Old on the left, new on the right — both still rendered.
		expect(screen.getByText(diffLine("const value = 0;"))).toBeInTheDocument();
		expect(screen.getByText(diffLine("const value = 1;"))).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add feedback on old line 1 in src/App.tsx" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add feedback on new line 1 in src/App.tsx" })).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Add feedback on old line 1 in src/App.tsx" }));
		expect(screen.getByRole("textbox", { name: "Feedback for src/App.tsx · old line 1" })).toBeInTheDocument();
		await userEvent.keyboard("{Escape}");

		const splitToggle = screen.getByRole("button", { name: "Unified diff view" });
		expect(splitToggle).toHaveAttribute("aria-pressed", "true");
		expect(splitToggle.querySelector(".lucide-columns-2")).not.toBeNull();
		expect(splitToggle).not.toHaveClass("text-accent");
		await userEvent.click(splitToggle);
		expect(container.querySelector(".grid-cols-2")).toBeNull();
		expect(screen.getByRole("button", { name: "Split diff view" }).querySelector(".lucide-rows-3")).not.toBeNull();
	});

	it("ignores split view for an added file — there is no old side to compare", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await screen.findByText(diffLine("const value = 1;"));

		// docs/guide.md is an added file in the shared mock data.
		await userEvent.click(await screen.findByRole("button", { name: "Expand docs/guide.md" }));
		await waitFor(() =>
			expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/file", {
				params: { path: { sessionId: "sess-1" }, query: { path: "docs/guide.md" } },
			}),
		);

		await userEvent.click(screen.getByRole("button", { name: "Split diff view" }));

		const modifiedRow = screen.getByRole("button", { name: "Collapse src/App.tsx" }).closest("li");
		const addedRow = screen.getByRole("button", { name: "Collapse docs/guide.md" }).closest("li");
		expect(modifiedRow?.querySelector(".grid-cols-2")).not.toBeNull();
		expect(addedRow?.querySelector(".grid-cols-2")).toBeNull();
	});

	it("sends inline line feedback to the session agent with precise diff context", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await screen.findByText(diffLine("const value = 1;"));

		const feedbackButton = screen.getByRole("button", { name: "Add feedback on new line 1 in src/App.tsx" });
		expect(feedbackButton).toHaveClass("bg-primary", "active:translate-y-0", "active:scale-100");
		expect(feedbackButton).not.toHaveClass("-translate-y-1/2");
		await userEvent.click(feedbackButton);
		const feedback = screen.getByRole("textbox", { name: "Feedback for src/App.tsx · new line 1" });
		await userEvent.type(feedback, "Reuse the shared value instead.");
		await userEvent.click(screen.getByRole("button", { name: "Send feedback" }));

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
				params: { path: { sessionId: "sess-1" } },
				body: { message: expect.stringContaining("Reuse the shared value instead.") },
			}),
		);
		const body = postMock.mock.calls[0][1].body as { message: string };
		expect(body.message).toContain("- Path: src/App.tsx");
		expect(body.message).toContain("- Location: New side, line 1");
		expect(body.message).toContain("- Code: const value = 1;");
		expect(await screen.findByText("Sent to agent")).toBeInTheDocument();
	});

	it("opens whole-file feedback and cancels it with Escape", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await screen.findByRole("button", { name: "Expand src/App.tsx" });

		await userEvent.click(screen.getByRole("button", { name: "Add feedback for file src/App.tsx" }));
		const feedback = screen.getByRole("textbox", { name: "Feedback for src/App.tsx · whole file" });
		expect(feedback).toHaveFocus();
		await userEvent.keyboard("{Escape}");

		expect(screen.queryByRole("textbox", { name: "Feedback for src/App.tsx · whole file" })).not.toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
	});

	it("moves focus between file rows with j and k", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		const first = await screen.findByRole("button", { name: "Expand src/App.tsx" });
		const second = screen.getByRole("button", { name: "Expand docs/guide.md" });

		first.focus();
		await userEvent.keyboard("j");
		expect(second).toHaveFocus();

		await userEvent.keyboard("k");
		expect(first).toHaveFocus();
	});

	it("renders changed files as one integrated review list instead of boxed cards", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		const activeRowButton = await screen.findByRole("button", { name: "Expand src/App.tsx" });
		const list = screen.getByRole("list");
		const row = activeRowButton.closest("li");

		expect(list).toHaveClass("session-files-review-list");
		expect(row).toHaveClass("session-files-review-row");
		expect(row).not.toHaveClass("border");
		expect(row).not.toHaveClass("bg-surface");
		expect(row).not.toHaveClass("shadow-sm");
		expect(activeRowButton.parentElement).toHaveClass("min-h-9");
		expect(activeRowButton).toHaveClass("gap-1.5", "px-2.5", "py-1");
		expect(screen.getByLabelText("Session files").querySelector("header")).toHaveClass("h-10", "px-2");
		const modifiedMark = screen.getByTitle("Modified");
		expect(modifiedMark).toHaveTextContent("M");
		expect(modifiedMark).not.toHaveClass("rounded", "border", "bg-warning/10");
	});

	it("uses one vertical scroller for the file list and expanded diffs", async () => {
		const { container } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await screen.findByText(diffLine("const value = 1;"));

		const panelScroller = container.querySelector("[data-files-scroll-root]");
		const diffScroller = container.querySelector(".session-files-diff-scrollbar");
		expect(panelScroller).toHaveClass("overflow-y-auto");
		expect(diffScroller).toHaveClass("overflow-x-auto");
		expect(diffScroller).not.toHaveClass("overflow-auto");
		expect(diffScroller?.parentElement).not.toHaveClass("max-h-[min(620px,calc(100vh-18rem))]");
	});

	it("routes vertical wheel gestures over a diff to the Files scroller immediately", async () => {
		const { container } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
		await screen.findByText(diffLine("const value = 1;"));

		const panelScroller = container.querySelector<HTMLElement>("[data-files-scroll-root]");
		const diffScroller = container.querySelector<HTMLElement>(".session-files-diff-scrollbar");
		expect(panelScroller).not.toBeNull();
		expect(diffScroller).not.toBeNull();
		if (!panelScroller || !diffScroller) return;

		fireEvent.wheel(diffScroller, { deltaX: 0, deltaY: 80, deltaMode: WheelEvent.DOM_DELTA_PIXEL });
		expect(panelScroller.scrollTop).toBe(80);

		fireEvent.wheel(diffScroller, { deltaX: 80, deltaY: 4, deltaMode: WheelEvent.DOM_DELTA_PIXEL });
		expect(panelScroller.scrollTop).toBe(80);
	});

	it("shows binary-file feedback as a compact inline state", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						truncated: false,
						files: [{ path: "screenshot.png", status: "added", additions: 0, deletions: 0, size: 120, binary: true }],
					},
				};
			}
			return {
				data: {
					sessionId: "sess-1",
					path: "screenshot.png",
					status: "added",
					additions: 0,
					deletions: 0,
					size: 120,
					binary: true,
					deleted: false,
					content: "",
					contentTruncated: false,
					diff: "",
					diffTruncated: false,
				},
			};
		});

		renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		await userEvent.click(await screen.findByRole("button", { name: "Expand screenshot.png" }));

		expect((await screen.findByText("Binary file preview is not available.")).parentElement?.parentElement).toHaveClass(
			"min-h-16",
			"p-3",
		);
	});

	it("uses the full session panel width while maximized", async () => {
		const { unmount } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
		const railList = await screen.findByRole("list");
		expect(railList.parentElement).toHaveClass("max-w-[1200px]");
		unmount();

		renderWithQuery(<SessionFilesView isMaximized sessionId="sess-1" />);
		const maximizedList = await screen.findByRole("list");
		expect(maximizedList.parentElement).not.toHaveClass("max-w-[1200px]");
	});

	it("lets the caller toggle between rail and maximized layouts", async () => {
		const onToggleMaximized = vi.fn();
		renderWithQuery(<SessionFilesView onToggleMaximized={onToggleMaximized} sessionId="sess-1" />);

		await userEvent.click(await screen.findByRole("button", { name: "Maximize files" }));
		expect(onToggleMaximized).toHaveBeenCalledWith(true);
	});

	it("shows a minimize action while maximized", async () => {
		const onToggleMaximized = vi.fn();
		renderWithQuery(
			<SessionFilesView isMaximized onToggleMaximized={onToggleMaximized} sessionId="sess-1" />,
		);

		await userEvent.click(await screen.findByRole("button", { name: "Minimize files" }));
		expect(onToggleMaximized).toHaveBeenCalledWith(false);
	});

	it("does not render a redundant close button in the Files toolbar", async () => {
		renderWithQuery(<SessionFilesView sessionId="sess-1" />);

		await screen.findByRole("button", { name: "Expand src/App.tsx" });
		expect(screen.queryByRole("button", { name: "Close files" })).not.toBeInTheDocument();
	});

	describe("diff selection -> send to agent", () => {
		it("removes the selectionchange listener when the diff view unmounts", async () => {
			const { unmount } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
			await screen.findByText(diffLine("const value = 1;"));

			const removeSpy = vi.spyOn(document, "removeEventListener");
			unmount();

			expect(removeSpy).toHaveBeenCalledWith("selectionchange", expect.any(Function));
			removeSpy.mockRestore();
		});

		it("opens the custom menu for a real drag selection across diff rows and suppresses the native menu", async () => {
			const { container } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
			await screen.findByText(diffLine("const value = 1;"));

			const scrollPane = container.querySelector(".session-files-diff-scrollbar") as HTMLElement;
			// Row 1 is the deleted line, row 2 is the added line — a selection
			// dragged across both.
			const { startRow } = await selectAcrossRows(scrollPane, 1, 2);

			const notCanceled = fireEvent.contextMenu(startRow, { clientX: 12, clientY: 34 });

			// fireEvent's return value is false when a handler called preventDefault —
			// i.e. this is direct evidence the native context menu was suppressed.
			expect(notCanceled).toBe(false);
			expect(await screen.findByRole("menuitem", { name: "Copy" })).toBeInTheDocument();
			expect(screen.getByRole("menuitem", { name: "Explain" })).toBeInTheDocument();
			expect(screen.getByRole("menuitem", { name: "Make changes" })).toBeInTheDocument();
		});

		it("leaves the native context menu untouched when there is no active selection", async () => {
			renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
			const contentSpan = await screen.findByText(diffLine("const value = 1;"));
			window.getSelection()?.removeAllRanges();

			const notCanceled = fireEvent.contextMenu(contentSpan, { clientX: 1, clientY: 1 });

			expect(notCanceled).toBe(true);
			expect(screen.queryByRole("menuitem", { name: "Copy" })).not.toBeInTheDocument();
		});

		it("maps a selection spanning multiple hunks and a pure-deletion line to the composed message", async () => {
			getMock.mockImplementation(async (path: string) => {
				if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
					return {
						data: {
							sessionId: "sess-1",
							truncated: false,
							compareMode: "base",
							files: [{ path: "src/App.tsx", status: "modified", additions: 1, deletions: 2, size: 120, binary: false }],
						},
					};
				}
				return {
					data: {
						sessionId: "sess-1",
						path: "src/App.tsx",
						status: "modified",
						additions: 1,
						deletions: 2,
						size: 120,
						binary: false,
						deleted: false,
						content: "",
						contentTruncated: false,
						diff: multiHunkDiff,
						diffTruncated: false,
					},
				};
			});

			const { container } = renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
			await screen.findByText(diffLine("line twelve"));

			const scrollPane = container.querySelector(".session-files-diff-scrollbar") as HTMLElement;
			// Row 1 ("line one", first hunk) through row 8 ("line twelve", second
			// hunk) — the range includes the hunk-band row (index 5) in between,
			// and the pure-deletion row (index 7, no newNo).
			const { startRow } = await selectAcrossRows(scrollPane, 1, 8);

			const notCanceled = fireEvent.contextMenu(startRow, { clientX: 5, clientY: 6 });
			expect(notCanceled).toBe(false);

			await userEvent.click(await screen.findByRole("menuitem", { name: "Explain" }));

			await waitFor(() => expect(postMock).toHaveBeenCalled());
			const body = postMock.mock.calls[0][1].body as { message: string };
			// Both hunks' real content lines made it into the message...
			expect(body.message).toContain(" line one");
			expect(body.message).toContain("- line two");
			expect(body.message).toContain("+ line two changed");
			expect(body.message).toContain(" line three");
			expect(body.message).toContain(" line ten");
			expect(body.message).toContain("- line eleven");
			expect(body.message).toContain(" line twelve");
			// ...but the intervening hunk-band row was dropped, not turned into a
			// bogus "line".
			expect(body.message).not.toContain("@@");
			// The pure-deletion line correctly falls back to old-line numbering
			// only where it has to; the overall range still prefers new numbers.
			expect(body.message).toContain("Selected lines 1-11:");
		});

		it("maps a single-side selection in split view to only that side's line", async () => {
			renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand src/App.tsx" }));
			await screen.findByText(diffLine("const value = 1;"));

			await userEvent.click(screen.getByRole("button", { name: "Split diff view" }));

			const oldSpan = await screen.findByText(diffLine("const value = 0;"));
			const scrollPane = oldSpan.closest(".session-files-diff-scrollbar") as HTMLElement;
			// Row 1 (the old/left side) on both ends — a selection confined to a
			// single split-view column.
			const { startRow } = await selectAcrossRows(scrollPane, 1, 1);

			const notCanceled = fireEvent.contextMenu(startRow, { clientX: 3, clientY: 4 });
			expect(notCanceled).toBe(false);

			await userEvent.click(await screen.findByRole("menuitem", { name: "Explain" }));

			await waitFor(() => expect(postMock).toHaveBeenCalled());
			const body = postMock.mock.calls[0][1].body as { message: string };
			expect(body.message).toContain("- const value = 0;");
			expect(body.message).not.toContain("const value = 1;");
		});
	});

	describe("large diff virtualization", () => {
		// jsdom has no real layout engine — getBoundingClientRect/clientHeight
		// are 0 for every element by default, which would make the virtualizer
		// see a zero-height viewport and render nothing. Mock a realistic
		// viewport so its visible-range math has something real to work with;
		// scrollTop is a plain settable property in jsdom, so simulating scroll
		// (set scrollTop, fire "scroll") drives the virtualizer for real.
		const VIEWPORT_HEIGHT = 600;

		beforeEach(() => {
			vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
				top: 0,
				left: 0,
				right: 800,
				bottom: VIEWPORT_HEIGHT,
				width: 800,
				height: VIEWPORT_HEIGHT,
				x: 0,
				y: 0,
				toJSON() {
					return this;
				},
			});
			vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(VIEWPORT_HEIGHT);
			vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(VIEWPORT_HEIGHT);
			vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(800);
		});

		afterEach(async () => {
			await drainVirtualizerScrollDebounce();
			vi.restoreAllMocks();
		});

		function bigModifiedDiff(lineCount: number) {
			const hunkLines: string[] = [`@@ -1,${lineCount} +1,${lineCount} @@`];
			for (let i = 0; i < lineCount; i += 1) {
				hunkLines.push(`-old line ${i} with some content to diff against`);
				hunkLines.push(`+new line ${i} with some different content entirely`);
			}
			return "diff --git a/big.txt b/big.txt\nindex 111..222 100644\n--- a/big.txt\n+++ b/big.txt\n" + hunkLines.join("\n") + "\n";
		}

		function mockBigFile(diff: string, lineCount: number) {
			getMock.mockImplementation(async (path: string, options?: unknown) => {
				if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
					return {
						data: {
							sessionId: "sess-1",
							truncated: false,
							files: [
								{ path: "big.txt", status: "modified", additions: lineCount, deletions: lineCount, size: 20000, binary: false },
							],
						},
					};
				}
				if (path === "/api/v1/sessions/{sessionId}/workspace/file") {
					const query = options as { params?: { query?: { path?: string } } };
					return {
						data: {
							sessionId: "sess-1",
							path: query.params?.query?.path ?? "big.txt",
							status: "modified",
							additions: lineCount,
							deletions: lineCount,
							size: 20000,
							binary: false,
							deleted: false,
							content: "irrelevant",
							contentTruncated: false,
							diff,
							diffTruncated: false,
							compareBaseSha: "base-sha",
							compareBaseRef: "main",
							compareMode: "base",
						},
					};
				}
				return { data: undefined };
			});
		}

		it("opens a large diff without hanging, mounting far fewer DOM rows than the diff has", async () => {
			const lineCount = 400;
			mockBigFile(bigModifiedDiff(lineCount), lineCount);

			renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand big.txt" }));

			await waitFor(() =>
				expect(screen.getByText(diffLine("new line 0 with some different content entirely"))).toBeInTheDocument(),
			);

			const mountedRows = document.querySelectorAll("[data-diff-row]").length;
			// lineCount*2 (del+add) + 1 hunk row is the fully-unvirtualized count;
			// a real viewport-sized window plus overscan should be a small
			// fraction of that.
			expect(mountedRows).toBeGreaterThan(0);
			expect(mountedRows).toBeLessThan(lineCount);

			// Never reaches the last line without scrolling — proves it's actually
			// windowed, not just slow to reveal everything.
			expect(screen.queryByText(diffLine(`new line ${lineCount - 1} with some different content entirely`))).not.toBeInTheDocument();
		});

		it("loads more rows as the shared scroll container scrolls", async () => {
			const lineCount = 400;
			mockBigFile(bigModifiedDiff(lineCount), lineCount);

			renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand big.txt" }));
			await waitFor(() =>
				expect(screen.getByText(diffLine("new line 0 with some different content entirely"))).toBeInTheDocument(),
			);

			const scrollRoot = document.querySelector<HTMLElement>("[data-files-scroll-root]");
			expect(scrollRoot).not.toBeNull();
			// jsdom doesn't clamp scrollTop against a real scrollHeight, so an
			// oversized value reliably lands on the last rows regardless of the
			// virtualizer's exact estimated-vs-measured row height math.
			scrollRoot!.scrollTop = 999_999;
			fireEvent.scroll(scrollRoot!);

			await waitFor(() =>
				expect(
					screen.queryByText(diffLine(`new line ${lineCount - 1} with some different content entirely`)),
				).toBeInTheDocument(),
			);
		});

		// jsdom has no real Worker implementation, so this exercises
		// useParsedDiff's exact "worker unavailable" fallback path — the same
		// path a CSP-blocked or otherwise-broken worker would take in a real
		// browser. A diff this size (well over the 50,000-char worker
		// threshold) would otherwise never resolve if that fallback were
		// broken.
		it("still renders a diff large enough to route through the parser worker path", async () => {
			const lineCount = 700;
			const diff = bigModifiedDiff(lineCount);
			expect(diff.length).toBeGreaterThan(50_000);
			mockBigFile(diff, lineCount);

			renderWithQuery(<SessionFilesView sessionId="sess-1" />);
			await userEvent.click(await screen.findByRole("button", { name: "Expand big.txt" }));

			await waitFor(() =>
				expect(screen.getByText(diffLine("new line 0 with some different content entirely"))).toBeInTheDocument(),
			);
		});
	});
});
