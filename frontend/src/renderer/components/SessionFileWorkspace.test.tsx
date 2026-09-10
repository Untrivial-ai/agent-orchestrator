import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SessionFileWorkspace } from "./SessionFileWorkspace";
import type { FileAnnotationModel } from "./WorkspaceDiffView";

vi.mock("./FileContentPane", () => ({ FileContentPane: () => <div data-testid="file-content" /> }));

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
		render(<SessionFileWorkspace annotation={annotation} path="src/App.tsx" sessionId="sess-1" />);

		expect(screen.getByTestId("session-file-workspace").querySelector("header")).not.toBeInTheDocument();
		expect(screen.getByTestId("file-content")).toBeInTheDocument();
	});
});
