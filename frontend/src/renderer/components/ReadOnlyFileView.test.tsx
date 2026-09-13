import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { ReadOnlyFileView } from "./ReadOnlyFileView";
import type { WorkspaceFileDetail } from "../hooks/useSessionWorkspaceFiles";
import type { FileAnnotationModel } from "./WorkspaceDiffView";

vi.mock("../lib/api-client", () => ({ getApiBaseUrl: () => "" }));
vi.mock("@pierre/diffs/react", () => ({
	File: ({ edit, editStateKey, file, lineAnnotations, onEditChange, options, renderAnnotation, renderGutterUtility }: {
		edit?: boolean;
		editStateKey?: string;
		file: { name: string; contents: string };
		lineAnnotations?: Array<{ lineNumber: number }>;
		onEditChange?: (event: { file: { contents: string } }) => void;
		options: { enableGutterUtility?: boolean; overflow: string; unsafeCSS?: string };
		renderAnnotation?: () => ReactNode;
		renderGutterUtility?: (getHoveredLine: () => { lineNumber: number }) => ReactNode;
	}) => (
		<div data-edit={String(Boolean(edit))} data-edit-state-key={editStateKey} data-file-name={file.name} data-gutter-enabled={String(Boolean(options.enableGutterUtility))} data-overflow={options.overflow} data-surface-css={options.unsafeCSS}>
			<code>{file.contents}</code>
			{edit ? <button onClick={() => onEditChange?.({ file: { contents: "edited\n" } })} type="button">type edit</button> : null}
			{renderGutterUtility?.(() => ({ lineNumber: 1 }))}
			{lineAnnotations?.map(({ lineNumber }) => <div key={lineNumber}>{renderAnnotation?.()}</div>)}
		</div>
	),
}));

function annotation(overrides: Partial<FileAnnotationModel> = {}): FileAnnotationModel {
	return { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn(), ...overrides };
}

function baseDetail(overrides: Partial<WorkspaceFileDetail> = {}): WorkspaceFileDetail {
	return {
		sessionId: "sess-1",
		path: "README.md",
		status: "unmodified",
		additions: 0,
		deletions: 0,
		size: 20,
		binary: false,
		deleted: false,
		content: "hello world\n",
		contentTruncated: false,
		diff: "",
		diffTruncated: false,
		...overrides,
	};
}

describe("ReadOnlyFileView", () => {
	it("renders source through the wrapped Pierre/Shiki surface", () => {
		const { container } = render(<ReadOnlyFileView annotation={annotation()} detail={baseDetail()} sessionId="sess-1" />);
		expect(screen.getByText("hello world")).toBeInTheDocument();
		expect(container.querySelector(".ao-pierre-surface")).toHaveClass("select-text");
		expect(container.querySelector("[data-overflow]")).toHaveAttribute("data-overflow", "wrap");
		expect(container.querySelector("[data-overflow]")).toHaveAttribute("data-surface-css", expect.stringContaining("--diffs-bg: var(--color-bg-primary)"));
	});

	it("passes the opened path to Shiki for language inference", () => {
		render(
			<ReadOnlyFileView
				annotation={annotation()}
				detail={baseDetail({ content: '{"enabled": true}', path: "frontend/package.json" })}
				sessionId="sess-1"
			/>,
		);
		expect(screen.getByText('{"enabled": true}').closest("[data-file-name]")).toHaveAttribute("data-file-name", "frontend/package.json");
	});

	it("starts inline feedback from the file gutter", () => {
		const model = annotation();
		const { container } = render(<ReadOnlyFileView annotation={model} detail={baseDetail()} sessionId="sess-1" />);
		expect(container.querySelector("[data-gutter-enabled]")).toHaveAttribute("data-gutter-enabled", "true");
		screen.getByRole("button", { name: "Add feedback" }).click();
		expect(model.begin).toHaveBeenCalledWith(expect.objectContaining({ path: "README.md", side: "file", line: 1, lineText: "hello world" }));
	});

	it("uses Pierre edit mode and reports its syntax-aware document", () => {
		const onEditChange = vi.fn();
		const { container } = render(<ReadOnlyFileView annotation={annotation()} detail={baseDetail()} editing onEditChange={onEditChange} sessionId="sess-1" />);
		expect(container.querySelector("[data-edit]"))?.toHaveAttribute("data-edit", "true");
		screen.getByRole("button", { name: "type edit" }).click();
		expect(onEditChange).toHaveBeenCalledWith("edited\n");
	});

	it("uses a unique editor state key when the same file is open in two panes", () => {
		const detail = baseDetail({ path: "src/App.tsx" });
		const { container } = render(
			<>
				<ReadOnlyFileView annotation={annotation()} detail={detail} editing sessionId="sess-1" />
				<ReadOnlyFileView annotation={annotation()} detail={detail} editing sessionId="sess-1" />
			</>,
		);
		const keys = Array.from(container.querySelectorAll("[data-edit-state-key]"), (element) => element.getAttribute("data-edit-state-key"));
		expect(keys).toHaveLength(2);
		expect(new Set(keys).size).toBe(2);
	});

	it("shows an image preview for a binary image file instead of a placeholder", () => {
		render(
			<ReadOnlyFileView
				annotation={annotation()}
				detail={baseDetail({ binary: true, content: "", path: "logo.png", imageMediaType: "image/png" })}
				sessionId="sess-1"
			/>,
		);
		const img = screen.getByRole("img");
		expect(img).toHaveAttribute("src", expect.stringContaining("/api/v1/sessions/sess-1/workspace/file/blob"));
		expect(img).toHaveAttribute("src", expect.stringContaining("side=after"));
	});

	it("shows a binary placeholder for a non-image binary file", () => {
		render(<ReadOnlyFileView annotation={annotation()} detail={baseDetail({ binary: true, content: "" })} sessionId="sess-1" />);
		expect(screen.getByText("Binary file preview is not available.")).toBeInTheDocument();
	});

	it("shows a too-large fallback instead of attempting to render truncated content", () => {
		render(<ReadOnlyFileView annotation={annotation()} detail={baseDetail({ contentTruncated: true, size: 5_000_000 })} sessionId="sess-1" />);
		expect(screen.getByText(/too large to preview/i)).toBeInTheDocument();
	});
});
