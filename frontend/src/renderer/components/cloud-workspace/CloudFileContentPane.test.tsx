// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { CloudCpClient } from "../../lib/cloud-cp";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { TooltipProvider } from "../ui/tooltip";
import { CloudFileContentPane } from "./CloudFileContentPane";

vi.mock("../ReadOnlyFileView", () => ({ ReadOnlyFileView: ({ detail, editing, onEditChange }: { detail: { content: string }; editing: boolean; onEditChange?: (value: string) => void }) => editing ? <textarea aria-label="editor" defaultValue={detail.content} onChange={(event) => onEditChange?.(event.target.value)} /> : <pre>{detail.content}</pre> }));
vi.mock("./CloudDiffFile", () => ({ CloudDiffFile: () => <div data-testid="cloud-diff" /> }));
vi.mock("../markdown/MarkdownFileView", () => ({ MarkdownFileView: ({ content }: { content: string }) => <article>{content}</article> }));

const annotation: FileAnnotationModel = { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn() };
const detail = { path: "README.md", status: "modified" as const, additions: 1, deletions: 1, size: 8, binary: false, editable: true, fileFingerprint: "fp-1", deleted: false, content: "# Hello", contentTruncated: false, diff: "diff", diffTruncated: false, workspaceVersion: "v1" };

function renderPane(client: CloudCpClient, props: Partial<React.ComponentProps<typeof CloudFileContentPane>> = {}) {
	return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><TooltipProvider><CloudFileContentPane annotation={annotation} baseUrl="https://cloud.test" client={client} orgId="org-1" path="README.md" sessionId="session-1" split={false} {...props} /></TooltipProvider></QueryClientProvider>);
}

describe("CloudFileContentPane", () => {
	it("switches between diff, complete file, and rendered Markdown", async () => {
		const client = { getWorkspaceReviewFile: vi.fn().mockResolvedValue(detail) } as unknown as CloudCpClient;
		renderPane(client);
		expect(await screen.findByTestId("cloud-diff")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("tab", { name: "File" }));
		expect(screen.getByText("# Hello")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("tab", { name: "Rich preview" }));
		expect(screen.getByRole("article")).toHaveTextContent("# Hello");
	});

	it("saves edits with the current fingerprint and reports dirty state", async () => {
		const updateWorkspaceReviewFile = vi.fn().mockResolvedValue({ ...detail, content: "# Changed", fileFingerprint: "fp-2" });
		const client = { getWorkspaceReviewFile: vi.fn().mockResolvedValue(detail), updateWorkspaceReviewFile } as unknown as CloudCpClient;
		const onDirtyChange = vi.fn();
		renderPane(client, { initialMode: "file", onDirtyChange });
		await userEvent.click(await screen.findByRole("button", { name: "Edit file" }));
		fireEvent.change(screen.getByRole("textbox", { name: "editor" }), { target: { value: "# Changed" } });
		await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(true));
		await userEvent.click(screen.getByRole("button", { name: "Save" }));
		await waitFor(() => expect(updateWorkspaceReviewFile).toHaveBeenCalledWith("org-1", "session-1", { path: "README.md", content: "# Changed", expectedFileFingerprint: "fp-1" }));
	});

	it("leaves line feedback to the file renderer instead of opening a second header composer", async () => {
		const client = { getWorkspaceReviewFile: vi.fn().mockResolvedValue(detail) } as unknown as CloudCpClient;
		const lineAnnotation: FileAnnotationModel = {
			...annotation,
			target: {
				path: "README.md",
				side: "file",
				line: 2,
				lineKind: "context",
				lineText: "Hello",
				scope: "combined",
				surface: "focused",
			},
		};
		renderPane(client, { annotation: lineAnnotation, initialMode: "file" });
		await screen.findByText("# Hello");
		expect(screen.queryByRole("textbox", { name: /feedback/i })).not.toBeInTheDocument();
	});
});
