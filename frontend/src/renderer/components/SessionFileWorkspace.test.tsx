import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SessionFileWorkspace } from "./SessionFileWorkspace";
import type { FileAnnotationModel } from "./WorkspaceDiffView";

vi.mock("./FileContentPane", () => ({
	FileContentPane: ({ initialEditing, initialMode, initialRequestKey, onDirtyChange, split }: { initialEditing?: boolean; initialMode?: string; initialRequestKey?: number; onDirtyChange?: (dirty: boolean) => void; split: boolean }) => <div data-editing={String(Boolean(initialEditing))} data-mode={initialMode} data-request-key={initialRequestKey} data-split={String(split)} data-testid="file-content"><button onClick={() => onDirtyChange?.(true)} type="button">mark dirty</button></div>,
}));

const annotation: FileAnnotationModel = {
	target: null,
	draft: "",
	status: "idle",
	error: "",
	begin: vi.fn(),
	setDraft: vi.fn(),
	cancel: vi.fn(),
	submit: vi.fn(),
};

describe("SessionFileWorkspace", () => {
	it("renders file content without a duplicate path toolbar", () => {
		render(<SessionFileWorkspace annotation={annotation} path="src/App.tsx" sessionId="sess-1" split />);

		expect(screen.getByTestId("session-file-workspace").querySelector("header")).not.toBeInTheDocument();
		expect(screen.getByTestId("file-content")).toHaveAttribute("data-split", "true");
		expect(screen.getByTestId("file-content")).toHaveAttribute("data-mode", "file");
	});

	it("reports center-editor dirty state with the opened path", () => {
		const onDirtyChange = vi.fn();
		render(<SessionFileWorkspace annotation={annotation} onDirtyChange={onDirtyChange} path="src/App.tsx" sessionId="sess-1" split={false} />);

		screen.getByRole("button", { name: "mark dirty" }).click();
		expect(onDirtyChange).toHaveBeenCalledWith("src/App.tsx", true);
	});

	it("forwards an explicit center mode and consumes one-shot edit requests on exit", () => {
		const onInitialEditingConsumed = vi.fn();
		const { unmount } = render(
			<SessionFileWorkspace
				annotation={annotation}
				initialEditing
				initialMode="diff"
				initialRequestKey={3}
				onInitialEditingConsumed={onInitialEditingConsumed}
				path="src/App.tsx"
				sessionId="sess-1"
				split={false}
			/>,
		);

		expect(screen.getByTestId("file-content")).toHaveAttribute("data-mode", "diff");
		expect(screen.getByTestId("file-content")).toHaveAttribute("data-editing", "true");
		unmount();
		expect(onInitialEditingConsumed).toHaveBeenCalledWith("src/App.tsx", 3);
	});
});
