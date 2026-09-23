import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { WorkspaceFileDetail } from "../../hooks/useSessionWorkspaceFiles";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { AoDiffFile } from "./AoDiffFile";

vi.mock("@pierre/diffs", () => ({
	parsePatchFiles: () => [{ files: [{ name: "src/App.tsx", type: "changed" }] }],
}));

vi.mock("@pierre/diffs/react", () => ({
	FileDiff: ({ options, renderGutterUtility }: {
		options: { enableGutterUtility?: boolean; unsafeCSS?: string };
		renderGutterUtility?: (getHoveredLine: () => { lineNumber: number; side: "additions" }) => ReactNode;
	}) => <div data-gutter-enabled={String(Boolean(options.enableGutterUtility))} data-surface-css={options.unsafeCSS}>{renderGutterUtility?.(() => ({ lineNumber: 2, side: "additions" }))}</div>,
}));

function detail(): WorkspaceFileDetail {
	return {
		sessionId: "sess-1",
		path: "src/App.tsx",
		status: "modified",
		additions: 1,
		deletions: 1,
		size: 24,
		binary: false,
		deleted: false,
		content: "const value = 2;\n",
		contentTruncated: false,
		diff: "@@ -1,2 +1,2 @@\n const first = 1;\n-const value = 1;\n+const value = 2;\n",
		diffTruncated: false,
		workspaceVersion: "workspace-1",
		fileFingerprint: "file-1",
	};
}

function annotation(): FileAnnotationModel {
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

describe("AoDiffFile", () => {
	it("starts line feedback using Pierre's hovered-side contract", () => {
		const model = annotation();
		render(
			<AoDiffFile
				annotation={model}
				detail={detail()}
				fallback={null}
				onActiveSelectionChange={vi.fn()}
				sessionId="sess-1"
				split={false}
			/>,
		);

		expect(screen.getByRole("button", { name: "Add feedback" }).parentElement).toHaveAttribute("data-gutter-enabled", "true");
		expect(screen.getByRole("button", { name: "Add feedback" }).parentElement).toHaveAttribute("data-surface-css", expect.stringContaining("--diffs-bg: var(--color-bg-primary)"));
		screen.getByRole("button", { name: "Add feedback" }).click();
		expect(model.begin).toHaveBeenCalledWith(expect.objectContaining({
			line: 2,
			lineText: "const value = 2;",
			path: "src/App.tsx",
			side: "new",
			surface: "focused",
		}));
	});
});
