import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import { renderMermaidDiagram } from "../../lib/mermaid-diagram";
import { MermaidBlock } from "./MermaidBlock";

// The engine chunk is heavy and needs real SVG layout APIs jsdom lacks, so
// these pin the boundary: the block calls render once per settled diagram,
// shows source while streaming, and falls back to source on any failure.
vi.mock("../../lib/mermaid-diagram", () => ({
	isRenderableDiagram: (code: string) => code.trim().length > 0 && code.length <= 20_000,
	renderMermaidDiagram: vi.fn(),
}));

const renderDiagram = vi.mocked(renderMermaidDiagram);
const CODE = "flowchart TD\n    A[User] --> B[AO]";

beforeEach(() => {
	renderDiagram.mockReset();
	renderDiagram.mockResolvedValue('<svg xmlns="http://www.w3.org/2000/svg"><g>diagram</g></svg>');
});

describe("MermaidBlock", () => {
	it("renders the diagram once settled, keeping the source one toggle away", async () => {
		const user = userEvent.setup();
		render(<MermaidBlock code={CODE} />);

		const diagram = await screen.findByTestId("mermaid-diagram");
		expect(diagram).toContainHTML("<svg");
		expect(renderDiagram).toHaveBeenCalledTimes(1);
		expect(renderDiagram).toHaveBeenCalledWith(CODE, expect.stringMatching(/light|dark/));
		expect(screen.getByRole("button", { name: /copy diagram source/i })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Code" }));
		expect(screen.getByText(/A\[User\]/)).toBeInTheDocument();
		expect(screen.queryByTestId("mermaid-diagram")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Code" })).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByRole("button", { name: "Diagram" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);

		await user.click(screen.getByRole("button", { name: "Diagram" }));
		expect(screen.getByTestId("mermaid-diagram")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Diagram" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	it("shows source text without calling the engine while streaming", () => {
		render(<MermaidBlock code={CODE} streaming />);

		expect(screen.getByText(/A\[User\]/)).toBeInTheDocument();
		expect(screen.queryByTestId("mermaid-diagram")).not.toBeInTheDocument();
		expect(renderDiagram).not.toHaveBeenCalled();
	});

	it("falls back to source with a caption when the diagram will not render", async () => {
		renderDiagram.mockRejectedValue(new Error("Parse error"));
		render(<MermaidBlock code={CODE} />);

		await waitFor(() =>
			expect(screen.getByText(/couldn't render this diagram/i)).toBeInTheDocument(),
		);
		expect(screen.getByText(/A\[User\]/)).toBeInTheDocument();
		expect(screen.queryByTestId("mermaid-diagram")).not.toBeInTheDocument();
	});

	it("falls back to source when the engine chunk will not load", async () => {
		renderDiagram.mockRejectedValue(new Error("Failed to fetch dynamically imported module"));
		render(<MermaidBlock code="flowchart TD\n    A --> B" />);

		await waitFor(() =>
			expect(screen.getByText(/couldn't render this diagram/i)).toBeInTheDocument(),
		);
	});

	it("re-renders when the theme flips", async () => {		const { unmount } = render(<MermaidBlock code={CODE} />);
		await screen.findByTestId("mermaid-diagram");
		expect(renderDiagram).toHaveBeenCalledTimes(1);

		document.documentElement.dataset.theme = "light";
		await waitFor(() => expect(renderDiagram).toHaveBeenCalledTimes(2));
		unmount();
		delete document.documentElement.dataset.theme;
	});

	it("routes a diagram link click to the chat handler instead of navigating", async () => {
		const user = userEvent.setup();
		const onLinkOpen = vi.fn();
		renderDiagram.mockResolvedValue(
			'<svg xmlns="http://www.w3.org/2000/svg"><a href="https://example.com/i/1"><text>docs</text></a></svg>',
		);
		render(<MermaidBlock code={CODE} onLinkOpen={onLinkOpen} />);

		// Without interception jsdom would attempt a real navigation here.
		await user.click(await screen.findByText("docs"));
		expect(onLinkOpen).toHaveBeenCalledWith("https://example.com/i/1");
	});

	it("opens diagram links in the system browser without a chat handler", async () => {
		const user = userEvent.setup();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderDiagram.mockResolvedValue(
			'<svg xmlns="http://www.w3.org/2000/svg"><a href="https://example.com/i/1"><text>docs</text></a></svg>',
		);
		render(<MermaidBlock code={CODE} />);

		await user.click(await screen.findByText("docs"));
		expect(openExternal).toHaveBeenCalledWith("https://example.com/i/1");
		openExternal.mockRestore();
	});

	it("routes a middle-click on a diagram link through the same policy", async () => {
		const onLinkOpen = vi.fn();
		renderDiagram.mockResolvedValue(
			'<svg xmlns="http://www.w3.org/2000/svg"><a href="https://example.com/i/1"><text>docs</text></a></svg>',
		);
		render(<MermaidBlock code={CODE} onLinkOpen={onLinkOpen} />);

		// Middle-click fires auxclick, not click: without interception it
		// would skip the chat link policy for the window-open guard.
		fireEvent(await screen.findByText("docs"), new MouseEvent("auxclick", { bubbles: true, button: 1 }));
		expect(onLinkOpen).toHaveBeenCalledWith("https://example.com/i/1");
	});

	it("ignores non-middle aux clicks on diagram links", async () => {
		const onLinkOpen = vi.fn();
		renderDiagram.mockResolvedValue(
			'<svg xmlns="http://www.w3.org/2000/svg"><a href="https://example.com/i/1"><text>docs</text></a></svg>',
		);
		render(<MermaidBlock code={CODE} onLinkOpen={onLinkOpen} />);

		fireEvent(await screen.findByText("docs"), new MouseEvent("auxclick", { bubbles: true, button: 2 }));
		expect(onLinkOpen).not.toHaveBeenCalled();
	});
});
