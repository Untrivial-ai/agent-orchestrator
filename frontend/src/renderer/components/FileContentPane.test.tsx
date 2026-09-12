import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileContentPane } from "./FileContentPane";
import type { FileAnnotationModel } from "./WorkspaceDiffView";
import { TooltipProvider } from "./ui/tooltip";

const { getMock, putMock } = vi.hoisted(() => ({ getMock: vi.fn(), putMock: vi.fn() }));

vi.mock("../hooks/usePierreFileHighlight", () => ({
	usePierreFileHighlightReady: () => true,
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, PUT: putMock },
	getApiBaseUrl: () => "",
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		return fallback;
	},
}));

vi.mock("./ReadOnlyFileView", () => ({
	ReadOnlyFileView: ({ detail, editing, onEditChange }: { detail: { content: string; path: string }; editing?: boolean; onEditChange?: (content: string) => void }) => editing ? (
		<textarea
			aria-label={`Edit ${detail.path}`}
			defaultValue={detail.content}
			onChange={(event) => onEditChange?.(event.target.value)}
		/>
	) : <code>{detail.content}</code>,
}));

function renderWithQuery(children: ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={client}><TooltipProvider>{children}</TooltipProvider></QueryClientProvider>);
}

function noopAnnotation(): FileAnnotationModel {
	return { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn() };
}

describe("FileContentPane", () => {
	beforeEach(() => {
		getMock.mockReset();
		putMock.mockReset();
	});

	it("prompts for a selection when no path is chosen", () => {
		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path={null} sessionId="sess-1" split={false} />);
		expect(screen.getByText("Select a file to preview.")).toBeInTheDocument();
		expect(getMock).not.toHaveBeenCalled();
	});

	it("renders the diff view for a changed file", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 1,
				size: 10,
				binary: false,
				deleted: false,
				content: "",
				contentTruncated: false,
				diff: "@@ -1,1 +1,1 @@\n-old\n+new\n",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="src/App.tsx" sessionId="sess-1" split={false} />);

		expect(
			await screen.findByText(
				(_, el) => el != null && /whitespace-pre/.test(el.className) && el.textContent === "new",
			),
		).toBeInTheDocument();
	});

	it("renders the read-only view for an untouched file", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "README.md",
				status: "unmodified",
				additions: 0,
				deletions: 0,
				size: 10,
				binary: false,
				deleted: false,
				content: "hello\n",
				contentTruncated: false,
				diff: "",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="README.md" sessionId="sess-1" split={false} />);

		expect(await screen.findByText("hello")).toBeInTheDocument();
	});

	it("shows the filename instead of a redundant File tab for an untouched non-Markdown file", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/config.ts",
				status: "unmodified",
				additions: 0,
				deletions: 0,
				size: 18,
				binary: false,
				deleted: false,
				content: "export const x = 1;\n",
				contentTruncated: false,
				diff: "",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="src/config.ts" sessionId="sess-1" split={false} />);

		expect((await screen.findByText("config.ts")).closest("[title]"))?.toHaveAttribute("title", "src/config.ts");
		expect(screen.queryByRole("tab", { name: "File" })).not.toBeInTheDocument();
		expect(screen.queryByRole("tablist", { name: "File display mode" })).not.toBeInTheDocument();
	});

	it("switches a changed file from its diff to the complete file", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 1,
				size: 18,
				binary: false,
				deleted: false,
				content: "export const next = 2;\n",
				contentTruncated: false,
				diff: "@@ -1,1 +1,1 @@\n-export const next = 1;\n+export const next = 2;\n",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="src/App.tsx" sessionId="sess-1" split={false} />);

		await userEvent.click(await screen.findByRole("tab", { name: "File" }));
		expect(await screen.findByText((_, element) => element?.tagName === "CODE" && element.textContent === "export const next = 2;\n")).toBeInTheDocument();
	});

	it("opens a changed markdown file directly in rendered mode while retaining its status", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "README.md",
				status: "modified",
				additions: 1,
				deletions: 0,
				size: 8,
				binary: false,
				deleted: false,
				content: "# Hello\n",
				contentTruncated: false,
				diff: "@@ -0,0 +1,1 @@\n+# Hello\n",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} initialMode="rendered" path="README.md" sessionId="sess-1" split={false} />);

		expect(await screen.findByRole("heading", { name: "Hello" })).toBeInTheDocument();
		expect(screen.getByText("M")).toBeInTheDocument();
		expect(screen.getByRole("tab", { name: "Rich preview" })).toHaveAttribute("aria-selected", "true");
	});

	it("starts whole-file feedback from the focused file header", async () => {
		const model = noopAnnotation();
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 0,
				size: 18,
				binary: false,
				deleted: false,
				editable: true,
				content: "export const x = 1;\n",
				contentTruncated: false,
				diff: "@@ -0,0 +1,1 @@\n+export const x = 1;\n",
				diffTruncated: false,
				workspaceVersion: "workspace-1",
				fileFingerprint: "file-1",
			},
		});

		renderWithQuery(<FileContentPane annotation={model} path="src/App.tsx" sessionId="sess-1" split={false} />);
		await userEvent.click(await screen.findByRole("button", { name: "Add feedback" }));

		expect(model.begin).toHaveBeenCalledWith(expect.objectContaining({
			fileFingerprint: "file-1",
			path: "src/App.tsx",
			side: "file",
			workspaceVersion: "workspace-1",
		}));
	});

	it("edits and saves a text file with optimistic stale-write protection", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 0,
				size: 18,
				binary: false,
				deleted: false,
				editable: true,
				content: "export const x = 1;\n",
				contentTruncated: false,
				diff: "@@ -0,0 +1,1 @@\n+export const x = 1;\n",
				diffTruncated: false,
				workspaceVersion: "workspace-1",
				fileFingerprint: "file-1",
			},
		});
		putMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 0,
				size: 18,
				binary: false,
				deleted: false,
				editable: true,
				content: "export const x = 2;\n",
				contentTruncated: false,
				diff: "@@ -0,0 +1,1 @@\n+export const x = 2;\n",
				diffTruncated: false,
				workspaceVersion: "workspace-2",
				fileFingerprint: "file-2",
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="src/App.tsx" sessionId="sess-1" split={false} />);
		await userEvent.click(await screen.findByRole("tab", { name: "File" }));
		await userEvent.click(await screen.findByRole("button", { name: "Edit file" }));
		expect(screen.queryByTestId("unsaved-file-indicator")).not.toBeInTheDocument();
		const editor = screen.getByRole("textbox", { name: "Edit src/App.tsx" });
		await userEvent.clear(editor);
		await userEvent.type(editor, "export const x = 2;\n");
		expect(screen.getByTestId("unsaved-file-indicator")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Save" }));

		await waitFor(() => expect(putMock).toHaveBeenCalledWith(
			"/api/v1/sessions/{sessionId}/workspace/file",
			expect.objectContaining({
				body: {
					content: "export const x = 2;\n",
					expectedFileFingerprint: "file-1",
					path: "src/App.tsx",
				},
			}),
		));
		expect(screen.queryByRole("textbox", { name: "Edit src/App.tsx" })).not.toBeInTheDocument();
		expect(screen.queryByTestId("unsaved-file-indicator")).not.toBeInTheDocument();
	});

	it.each([
		["Command+S", { metaKey: true }],
		["Control+S", { ctrlKey: true }],
	] as const)("saves a dirty editor with %s", async (_label, modifier) => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "unmodified",
				additions: 0,
				deletions: 0,
				size: 18,
				binary: false,
				deleted: false,
				editable: true,
				content: "export const x = 1;\n",
				contentTruncated: false,
				diff: "",
				diffTruncated: false,
				workspaceVersion: "workspace-1",
				fileFingerprint: "file-1",
			},
		});
		putMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 1,
				size: 18,
				binary: false,
				deleted: false,
				editable: true,
				content: "export const x = 2;\n",
				contentTruncated: false,
				diff: "@@ -1 +1 @@\n-export const x = 1;\n+export const x = 2;\n",
				diffTruncated: false,
				workspaceVersion: "workspace-2",
				fileFingerprint: "file-2",
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="src/App.tsx" sessionId="sess-1" split={false} />);
		await userEvent.click(await screen.findByRole("button", { name: "Edit file" }));
		const editor = screen.getByRole("textbox", { name: "Edit src/App.tsx" });
		await userEvent.clear(editor);
		await userEvent.type(editor, "export const x = 2;\n");
		fireEvent.keyDown(window, { key: "s", ...modifier });

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(screen.queryByRole("textbox", { name: "Edit src/App.tsx" })).not.toBeInTheDocument();
	});

	it("anchors whole-file feedback below the focused header", async () => {
		const model = noopAnnotation();
		model.target = { path: "src/App.tsx", side: "file", scope: "combined", surface: "focused" };
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "src/App.tsx",
				status: "modified",
				additions: 1,
				deletions: 0,
				size: 18,
				binary: false,
				deleted: false,
				content: "export const x = 1;\n",
				contentTruncated: false,
				diff: "@@ -0,0 +1,1 @@\n+export const x = 1;\n",
				diffTruncated: false,
				workspaceVersion: "workspace-1",
				fileFingerprint: "file-1",
			},
		});

		renderWithQuery(<FileContentPane annotation={model} path="src/App.tsx" sessionId="sess-1" split={false} />);

		const composer = await screen.findByRole("textbox", { name: /Feedback for src\/App\.tsx/ });
		expect(composer.closest(".absolute.top-full")?.parentElement).toHaveClass("sticky", "top-0");
	});

	it("loads the before revision when opening the complete view of a deleted file", async () => {
		getMock.mockImplementation(async (path: string) => path.endsWith("/revision") ? {
			data: {
				sessionId: "sess-1",
				path: "removed.txt",
				side: "before",
				revision: "old-1",
				workspaceVersion: "workspace-1",
				size: 12,
				exists: true,
				binary: false,
				truncated: false,
				content: "removed text\n",
			},
		} : {
			data: {
				sessionId: "sess-1",
				path: "removed.txt",
				status: "deleted",
				additions: 0,
				deletions: 1,
				size: 12,
				binary: false,
				deleted: true,
				content: "",
				contentTruncated: false,
				diff: "@@ -1,1 +0,0 @@\n-removed text\n",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="removed.txt" sessionId="sess-1" split={false} />);
		await userEvent.click(await screen.findByRole("tab", { name: "File" }));

		expect(await screen.findByText("removed text")).toBeInTheDocument();
		expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/file/revision", expect.objectContaining({
			params: expect.objectContaining({ query: expect.objectContaining({ path: "removed.txt", side: "before" }) }),
		}));
	});

	it("falls back to current content when a changed extensionless file has no renderable diff", async () => {
		getMock.mockResolvedValue({
			data: {
				sessionId: "sess-1",
				path: "build",
				status: "modified",
				additions: 0,
				deletions: 0,
				size: 24,
				binary: false,
				deleted: false,
				content: "#!/usr/bin/env bash\necho ok\n",
				contentTruncated: false,
				diff: "",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="build" sessionId="sess-1" split={false} />);

		expect(await screen.findByText(/echo ok/)).toBeInTheDocument();
	});

	it("shows a retryable error instead of a blank pane when a successful response has no body", async () => {
		getMock.mockResolvedValue({ data: undefined });

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="README.md" sessionId="sess-1" split={false} />);

		expect(await screen.findByText("Unable to load workspace file")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
	});

	it("shows a retry action on load failure and refetches on click", async () => {
		getMock.mockRejectedValueOnce(new Error("boom")).mockResolvedValueOnce({
			data: {
				sessionId: "sess-1",
				path: "README.md",
				status: "unmodified",
				additions: 0,
				deletions: 0,
				size: 10,
				binary: false,
				deleted: false,
				content: "recovered\n",
				contentTruncated: false,
				diff: "",
				diffTruncated: false,
			},
		});

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="README.md" sessionId="sess-1" split={false} />);

		expect(await screen.findByText("boom")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Retry" }));
		await waitFor(() => expect(screen.getByText("recovered")).toBeInTheDocument());
	});
});
