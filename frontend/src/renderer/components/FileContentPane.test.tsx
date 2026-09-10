import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileContentPane } from "./FileContentPane";
import type { FileAnnotationModel } from "./WorkspaceDiffView";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	getApiBaseUrl: () => "",
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		return fallback;
	},
}));

function renderWithQuery(children: ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
}

function noopAnnotation(): FileAnnotationModel {
	return { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn() };
}

describe("FileContentPane", () => {
	beforeEach(() => {
		getMock.mockReset();
	});

	it("prompts for a selection when no path is chosen", () => {
		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path={null} sessionId="sess-1" split={false} wrap={true} />);
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

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="src/App.tsx" sessionId="sess-1" split={false} wrap={true} />);

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

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="README.md" sessionId="sess-1" split={false} wrap={true} />);

		expect(await screen.findByText("hello")).toBeInTheDocument();
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

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="build" sessionId="sess-1" split={false} wrap />);

		expect(await screen.findByText(/echo ok/)).toBeInTheDocument();
	});

	it("shows a retryable error instead of a blank pane when a successful response has no body", async () => {
		getMock.mockResolvedValue({ data: undefined });

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="README.md" sessionId="sess-1" split={false} wrap />);

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

		renderWithQuery(<FileContentPane annotation={noopAnnotation()} path="README.md" sessionId="sess-1" split={false} wrap={true} />);

		expect(await screen.findByText("boom")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Retry" }));
		await waitFor(() => expect(screen.getByText("recovered")).toBeInTheDocument());
	});
});
