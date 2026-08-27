import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReviewDiffBody, type FileAnnotationModel } from "./WorkspaceDiffView";
import type { WorkspaceFileDetail } from "../hooks/useSessionWorkspaceFiles";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	getApiBaseUrl: () => "",
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		return fallback;
	},
}));

/**
 * @tanstack/react-virtual falls back to a 150ms scroll-end debounce when the
 * environment has no `scrollend` event (jsdom). That timer is not cancelled on
 * unmount, so it can fire after vitest tears down jsdom — drain past it while
 * `window` still exists.
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

// Intra-line word highlights nest the row's code text a level or two deeper
// than a plain row, so a selection Range needs to walk down to a real text
// node rather than assuming firstChild is one.
function findTextNode(node: Node): Text | null {
	if (node.nodeType === Node.TEXT_NODE) return node as Text;
	for (const child of Array.from(node.childNodes)) {
		const found = findTextNode(child);
		if (found) return found;
	}
	return null;
}

function noopAnnotation(): FileAnnotationModel {
	return {
		target: null,
		draft: "",
		status: "idle",
		error: "",
		begin: vi.fn(),
		setDraft: vi.fn(),
		cancel: vi.fn(),
		submit: vi.fn(),
	};
}

function baseDetail(overrides: Partial<WorkspaceFileDetail> = {}): WorkspaceFileDetail {
	return {
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
		diff: "diff --git a/src/App.tsx b/src/App.tsx\nindex 111..222 100644\n--- a/src/App.tsx\n+++ b/src/App.tsx\n@@ -1,1 +1,1 @@\n-const value = 0;\n+const value = 1;\n",
		diffTruncated: false,
		...overrides,
	};
}

describe("ReviewDiffBody", () => {
	beforeEach(() => {
		postMock.mockReset().mockResolvedValue({ data: {} });
		window.getSelection()?.removeAllRanges();
		Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn() } });
	});

	afterEach(async () => {
		await drainVirtualizerScrollDebounce();
		vi.useRealTimers();
		window.getSelection()?.removeAllRanges();
	});

	it("renders a real diff without git-header noise and with markers in the gutter", async () => {
		render(
			<ReviewDiffBody
				annotation={noopAnnotation()}
				detail={baseDetail()}
				detailLoadedAt={1}
				filePath="src/App.tsx"
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={false}
				wrap={true}
			/>,
		);

		expect(await screen.findByText(diffLine("const value = 1;"))).toBeInTheDocument();
		expect(screen.getByText(diffLine("const value = 0;"))).toBeInTheDocument();
		expect(screen.getByText("@@ -1,1 +1,1 @@")).toBeInTheDocument();
		expect(screen.queryByText("diff --git a/src/App.tsx b/src/App.tsx")).not.toBeInTheDocument();
	});

	it("highlights only the changed tokens within a replaced line", async () => {
		const { container } = render(
			<ReviewDiffBody
				annotation={noopAnnotation()}
				detail={baseDetail()}
				detailLoadedAt={1}
				filePath="src/App.tsx"
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={false}
				wrap={true}
			/>,
		);

		await screen.findByText(diffLine("const value = 1;"));
		expect(container.querySelector('[class*="bg-success/35"]')?.textContent).toBe("1");
		expect(container.querySelector('[class*="bg-error/35"]')?.textContent).toBe("0");
	});

	it("renders both sides side by side when split is requested", async () => {
		const { container } = render(
			<ReviewDiffBody
				annotation={noopAnnotation()}
				detail={baseDetail()}
				detailLoadedAt={1}
				filePath="src/App.tsx"
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={true}
				wrap={true}
			/>,
		);

		await screen.findByText(diffLine("const value = 1;"));
		expect(container.querySelector(".grid-cols-2")).not.toBeNull();
		expect(screen.getByText(diffLine("const value = 0;"))).toBeInTheDocument();
	});

	it("shows a binary placeholder instead of a diff", () => {
		render(
			<ReviewDiffBody
				annotation={noopAnnotation()}
				detail={baseDetail({ binary: true, diff: "" })}
				detailLoadedAt={1}
				filePath="screenshot.png"
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={false}
				wrap={true}
			/>,
		);

		expect(screen.getByText("Binary file preview is not available.")).toBeInTheDocument();
	});

	it("renders both sides of a changed image instead of the binary placeholder", async () => {
		render(
			<ReviewDiffBody
				annotation={noopAnnotation()}
				detail={baseDetail({ binary: true, diff: "", imageMediaType: "image/png", path: "docs/logo.png" })}
				detailLoadedAt={1}
				filePath="docs/logo.png"
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={false}
				wrap={true}
			/>,
		);

		const before = await screen.findByAltText("Before version of docs/logo.png");
		const after = await screen.findByAltText("After version of docs/logo.png");
		expect(before).toHaveAttribute(
			"src",
			expect.stringContaining("/api/v1/sessions/sess-1/workspace/file/blob?path=docs%2Flogo.png&side=before"),
		);
		expect(after).toHaveAttribute(
			"src",
			expect.stringContaining("/api/v1/sessions/sess-1/workspace/file/blob?path=docs%2Flogo.png&side=after"),
		);
		expect(screen.queryByText("Binary file preview is not available.")).not.toBeInTheDocument();
	});

	it("sends a selected diff range to the session agent via the context menu", async () => {
		render(
			<ReviewDiffBody
				annotation={noopAnnotation()}
				detail={baseDetail()}
				detailLoadedAt={1}
				filePath="src/App.tsx"
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={false}
				wrap={true}
			/>,
		);
		const addedRow = (await screen.findByText(diffLine("const value = 1;"))).closest("[data-row-index]") as HTMLElement;
		const range = document.createRange();
		const textNode = findTextNode(addedRow.querySelector("span:last-child")!)!;
		range.setStart(textNode, 0);
		range.setEnd(textNode, textNode.textContent?.length ?? 0);
		const selection = window.getSelection();
		selection?.removeAllRanges();
		selection?.addRange(range);
		await act(async () => {
			await new Promise((resolve) => setTimeout(resolve, 0));
		});

		const notCanceled = fireEvent.contextMenu(addedRow, { clientX: 5, clientY: 5 });
		expect(notCanceled).toBe(false);

		await userEvent.click(await screen.findByRole("menuitem", { name: "Explain" }));
		await waitFor(() => expect(postMock).toHaveBeenCalled());
		const body = postMock.mock.calls[0][1].body as { message: string };
		expect(body.message).toContain("const value = 1;");
	});

	describe("large diff virtualization", () => {
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
			return `diff --git a/big.txt b/big.txt\nindex 111..222 100644\n--- a/big.txt\n+++ b/big.txt\n${hunkLines.join("\n")}\n`;
		}

		it("opens a large diff without hanging, mounting far fewer DOM rows than the diff has", async () => {
			const lineCount = 400;
			render(
				<div data-files-scroll-root="" style={{ overflow: "auto" }}>
					<ReviewDiffBody
						annotation={noopAnnotation()}
						detail={baseDetail({ path: "big.txt", diff: bigModifiedDiff(lineCount) })}
						detailLoadedAt={1}
						filePath="big.txt"
						onActiveSelectionChange={vi.fn()}
						sessionId="sess-1"
						split={false}
						wrap={true}
					/>
				</div>,
			);

			await waitFor(() =>
				expect(screen.getByText(diffLine("new line 0 with some different content entirely"))).toBeInTheDocument(),
			);

			const mountedRows = document.querySelectorAll("[data-diff-row]").length;
			expect(mountedRows).toBeGreaterThan(0);
			expect(mountedRows).toBeLessThan(lineCount);
			expect(
				screen.queryByText(diffLine(`new line ${lineCount - 1} with some different content entirely`)),
			).not.toBeInTheDocument();
		});
	});
});
