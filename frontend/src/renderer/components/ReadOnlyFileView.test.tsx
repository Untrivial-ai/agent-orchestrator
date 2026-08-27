import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ReadOnlyFileView } from "./ReadOnlyFileView";
import type { WorkspaceFileDetail } from "../hooks/useSessionWorkspaceFiles";

vi.mock("../lib/api-client", () => ({ getApiBaseUrl: () => "" }));
vi.mock("./chat/HighlightedCode", () => ({
	HighlightedCode: ({ code, language }: { code: string; language?: string }) => (
		<span data-language={language}>{code}</span>
	),
}));

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
	it("renders plain content through the shared syntax renderer", () => {
		render(<ReadOnlyFileView detail={baseDetail()} sessionId="sess-1" />);
		expect(screen.getByText("hello world")).toBeInTheDocument();
	});

	it("selects the JSON grammar from the opened file path", () => {
		render(
			<ReadOnlyFileView
				detail={baseDetail({ content: '{"enabled": true}', path: "frontend/package.json" })}
				sessionId="sess-1"
			/>,
		);
		expect(screen.getByText('{"enabled": true}')).toHaveAttribute("data-language", "json");
	});

	it("shows an image preview for a binary image file instead of a placeholder", () => {
		render(
			<ReadOnlyFileView
				detail={baseDetail({ binary: true, content: "", path: "logo.png", imageMediaType: "image/png" })}
				sessionId="sess-1"
			/>,
		);
		const img = screen.getByRole("img");
		expect(img).toHaveAttribute("src", expect.stringContaining("/api/v1/sessions/sess-1/workspace/file/blob"));
		expect(img).toHaveAttribute("src", expect.stringContaining("side=after"));
	});

	it("shows a binary placeholder for a non-image binary file", () => {
		render(<ReadOnlyFileView detail={baseDetail({ binary: true, content: "" })} sessionId="sess-1" />);
		expect(screen.getByText("Binary file preview is not available.")).toBeInTheDocument();
	});

	it("shows a too-large fallback instead of attempting to render truncated content", () => {
		render(<ReadOnlyFileView detail={baseDetail({ contentTruncated: true, size: 5_000_000 })} sessionId="sess-1" />);
		expect(screen.getByText(/too large to preview/i)).toBeInTheDocument();
	});
});
