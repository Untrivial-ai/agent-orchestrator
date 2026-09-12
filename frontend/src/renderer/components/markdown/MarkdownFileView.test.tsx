import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import { renderMermaidDiagram } from "../../lib/mermaid-diagram";
import { MarkdownFileView } from "./MarkdownFileView";

vi.mock("../../lib/api-client", () => ({
	getApiBaseUrl: () => "http://127.0.0.1:4567",
}));

// Same boundary as chat: mermaid needs layout APIs jsdom lacks, so stub the
// engine and assert the preview routes the fence to the diagram.
vi.mock("../../lib/mermaid-diagram", () => ({
	isRenderableDiagram: (code: string) => code.trim().length > 0 && code.length <= 20_000,
	renderMermaidDiagram: vi.fn(async () => '<svg xmlns="http://www.w3.org/2000/svg"><g>diagram</g></svg>'),
}));

beforeEach(() => {
	vi.mocked(renderMermaidDiagram).mockClear();
});

afterEach(() => {
	vi.restoreAllMocks();
});

function view(content: string, filePath = "docs/README.md", truncated = false) {
	return (
		<MarkdownFileView
			content={content}
			filePath={filePath}
			sessionId="sess-1"
			truncated={truncated}
			version={7}
		/>
	);
}

describe("MarkdownFileView", () => {
	it("renders GFM, GitHub alerts, and highlighted fenced code", async () => {
		const { container } = render(
			view(
				"| Item | Done |\n| --- | --- |\n| Tests | ✅ |\n\n> [!NOTE]\n> Read this.\n\n```ts\nconst ready = true;\n```",
			),
		);

		expect(screen.getByRole("table")).toBeInTheDocument();
		expect(screen.getByText("Read this.").closest(".markdown-alert")).toHaveClass("markdown-alert-note");
		expect(container.querySelector("pre.markdown-code")).toHaveTextContent("const ready = true;");
		await waitFor(() =>
			expect(container.querySelector(".markdown-code .hljs-keyword")).toHaveTextContent("const"),
		);
	});

	it("resolves relative images through the workspace blob route without double encoding", () => {
		render(view("![Flow diagram](./assets/flow%20chart.png)", "docs/README.md"));

		const src = screen.getByRole("img", { name: "Flow diagram" }).getAttribute("src");
		const url = new URL(src!);
		expect(url.searchParams.get("path")).toBe("docs/assets/flow chart.png");
		expect(url.searchParams.get("side")).toBe("after");
	});

	it("keeps relative file links inert and escapes raw HTML", () => {
		render(view('[Other file](./OTHER.md) before <img src=x onerror="alert(1)"> after'));

		expect(screen.queryByRole("link", { name: "Other file" })).not.toBeInTheDocument();
		expect(screen.getByText("Other file").tagName).toBe("SPAN");
		expect(document.querySelector("img")).toBeNull();
		expect(document.body.textContent).toContain("onerror");
	});

	it("opens external links in the system browser", () => {
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(view("[AO docs](https://example.com/docs)"));

		fireEvent.click(screen.getByRole("link", { name: "AO docs" }));

		expect(openExternal).toHaveBeenCalledWith("https://example.com/docs");
	});

	it("renders a mermaid fence as a diagram rather than source text", async () => {
		render(view("```mermaid\nflowchart TD\n    A --> B\n```"));

		expect(await screen.findByTestId("mermaid-diagram")).toBeInTheDocument();
		expect(vi.mocked(renderMermaidDiagram)).toHaveBeenCalledWith(
			"flowchart TD\n    A --> B",
			expect.stringMatching(/light|dark/),
		);
	});

	it("warns when the daemon returned only a prefix of the file", () => {
		render(view("# Partial guide", "docs/README.md", true));

		expect(screen.getByText("Rendered preview truncated.")).toBeInTheDocument();
	});

	it("scrolls the clicked heading when multiple previews contain the same slug", () => {
		const writeText = vi.spyOn(aoBridge.clipboard, "writeText").mockResolvedValue(undefined);
		const { container } = render(
			<>
				{view("# Repeat", "docs/one.md")}
				{view("# Repeat", "docs/two.md")}
			</>,
		);
		const headings = screen.getAllByRole("heading", { name: "Repeat" });
		const firstScroll = vi.fn();
		const secondScroll = vi.fn();
		Object.defineProperty(headings[0], "scrollIntoView", { configurable: true, value: firstScroll });
		Object.defineProperty(headings[1], "scrollIntoView", { configurable: true, value: secondScroll });
		const anchors = container.querySelectorAll<HTMLAnchorElement>("a.anchor");

		fireEvent.click(anchors[1]!);

		expect(firstScroll).not.toHaveBeenCalled();
		expect(secondScroll).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
		expect(writeText).not.toHaveBeenCalled();
		expect(anchors[1]).not.toHaveAttribute("node");
	});
});
