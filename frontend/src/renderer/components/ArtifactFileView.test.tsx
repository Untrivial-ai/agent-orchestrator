import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ArtifactFileView } from "./ArtifactFileView";
import { TooltipProvider } from "./ui/tooltip";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: (error: unknown, fallback?: string) => error instanceof Error ? error.message : fallback ?? "error",
	getApiBaseUrl: () => "http://127.0.0.1:3001",
}));
vi.mock("../hooks/usePierreFileHighlight", () => ({ usePierreFileHighlightReady: () => true }));
vi.mock("./ReadOnlyFileView", () => ({
	ReadOnlyFileView: ({ detail }: { detail: { binary: boolean; content: string; contentTruncated: boolean; size: number } }) => (
		<div>
			<code>{detail.content}</code>
			<span data-testid="artifact-binary">{String(detail.binary)}</span>
			<span data-testid="artifact-truncated">{String(detail.contentTruncated)}</span>
			<span data-testid="artifact-size">{detail.size}</span>
		</div>
	),
}));

const fetchMock = vi.fn();

function renderWithQuery(ui: ReactNode) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>{ui}</TooltipProvider>
		</QueryClientProvider>,
	);
}

function scrollContainer(): HTMLElement {
	const element = document.querySelector(".board-scrollbar");
	if (!(element instanceof HTMLElement)) throw new Error("expected artifact scroll container");
	return element;
}

describe("ArtifactFileView", () => {
	beforeEach(() => {
		vi.stubGlobal("fetch", fetchMock);
	});

	afterEach(() => {
		fetchMock.mockReset();
		postMock.mockReset();
		vi.unstubAllGlobals();
	});

	it("fetches a .txt artifact through the artifact-scoped preview route and renders its content", async () => {
		fetchMock.mockResolvedValue(new Response("hello world", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" path="notes.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByText("hello world")).toBeInTheDocument());
		expect(scrollContainer()).toHaveClass("overflow-y-auto", "min-h-0");

		expect(fetchMock).toHaveBeenCalledWith(
			"http://127.0.0.1:3001/api/v1/sessions/sess-1/preview/files/__ao_artifacts__/notes.txt?raw=true",
		);
	});

	it("opens a .md artifact already rendered, not as raw markdown source", async () => {
		fetchMock.mockResolvedValue(new Response("# Notes\n\nhello", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.md" path="notes.md" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByRole("heading", { name: "Notes" })).toBeInTheDocument());
		expect(fetchMock).toHaveBeenCalledWith(
			"http://127.0.0.1:3001/api/v1/sessions/sess-1/preview/files/__ao_artifacts__/notes.md?raw=true",
		);
		expect(screen.getByText("hello")).toBeInTheDocument();
		expect(screen.queryByText(/^# Notes/)).not.toBeInTheDocument();
	});

	it("URL-encodes nested artifact paths per segment", async () => {
		fetchMock.mockResolvedValue(new Response("content", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="report.txt" path="sub dir/report.txt" sessionId="sess-1" />);

		await waitFor(() => expect(fetchMock).toHaveBeenCalled());
		expect(fetchMock).toHaveBeenCalledWith(
			"http://127.0.0.1:3001/api/v1/sessions/sess-1/preview/files/__ao_artifacts__/sub%20dir/report.txt?raw=true",
		);
	});

	it("marks binary artifacts as binary instead of decoding them as text", async () => {
		fetchMock.mockResolvedValue(new Response("prefix\0suffix", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="data.bin" path="data.bin" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByTestId("artifact-binary")).toHaveTextContent("true"));
		expect(screen.getByTestId("artifact-truncated")).toHaveTextContent("false");
		expect(document.querySelector("code")).toHaveTextContent("");
	});

	it("bounds oversized artifact reads and marks the content truncated", async () => {
		fetchMock.mockResolvedValue(new Response("a".repeat(256 * 1024 + 1), { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="large.txt" path="large.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByTestId("artifact-truncated")).toHaveTextContent("true"));
		expect(screen.getByTestId("artifact-binary")).toHaveTextContent("false");
		expect(screen.getByTestId("artifact-size")).toHaveTextContent(String(256 * 1024 + 1));
	});

	it("does not show an edit affordance (no write endpoint for artifacts yet)", async () => {
		fetchMock.mockResolvedValue(new Response("content", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" path="notes.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByText("content")).toBeInTheDocument());
		expect(screen.queryByRole("button", { name: "Edit file" })).not.toBeInTheDocument();
	});

	it("shows a retry option when the fetch fails", async () => {
		fetchMock.mockResolvedValue(new Response("nope", { status: 404 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" path="notes.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument());
	});

	it("opens whole-artifact feedback from a reveal request and sends it to the worker", async () => {
		fetchMock.mockResolvedValue(new Response("content", { status: 200 }));
		postMock.mockResolvedValue({ data: { status: "ok" } });

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" feedbackRequestKey={1} path="notes.txt" sessionId="sess-1" />);

		const textbox = await screen.findByRole("textbox", { name: /Feedback for notes\.txt/ });
		await userEvent.type(textbox, "Please tighten this artifact.");
		await userEvent.click(screen.getByRole("button", { name: "Send feedback" }));

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
				params: { path: { sessionId: "sess-1" } },
				body: { message: expect.stringContaining("Please tighten this artifact.") },
			}),
		);
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
			params: { path: { sessionId: "sess-1" } },
			body: { message: expect.stringContaining("notes.txt") },
		});
	});
});
