import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import { renderMermaidDiagram } from "../../lib/mermaid-diagram";
import { ActivityTitle, ChatLinkProvider, ChatMarkdown } from "./ChatMarkdown";

// Mermaid needs real SVG layout APIs jsdom lacks; pin the routing boundary and
// let MermaidBlock.test.tsx own the block's states.
vi.mock("../../lib/mermaid-diagram", () => ({
	isRenderableDiagram: (code: string) => code.trim().length > 0 && code.length <= 20_000,
	renderMermaidDiagram: vi.fn(async () => '<svg xmlns="http://www.w3.org/2000/svg"><g>diagram</g></svg>'),
}));

beforeEach(() => {
	vi.mocked(renderMermaidDiagram).mockClear();
});

// The point of these is that the SYNTAX stops being visible. Every case here is a
// shape agents actually emit, and the assertion is that structure replaced markup.

function renderWithLinkHandler(text: string, onLinkOpen: (url: string) => void) {
	return render(
		<ChatLinkProvider onLinkOpen={onLinkOpen}>
			<ChatMarkdown text={text} />
		</ChatLinkProvider>,
	);
}

function renderWithSessionLinkHandler(text: string, onSessionLinkOpen: (url: string) => void) {
	return render(<ChatLinkProvider onSessionLinkOpen={onSessionLinkOpen}><ChatMarkdown text={text} /></ChatLinkProvider>);
}

describe("ChatMarkdown", () => {
	it("renders headings as headings rather than literal hashes", () => {
		render(<ChatMarkdown text={"## Findings\n\nTwo files changed."} />);
		expect(screen.getByRole("heading", { name: "Findings" })).toBeInTheDocument();
		expect(screen.queryByText(/## Findings/)).not.toBeInTheDocument();
	});

	it("renders bullet and numbered lists as lists", () => {
		render(<ChatMarkdown text={"- first\n- second\n\n1. one\n2. two"} />);
		const lists = screen.getAllByRole("list");
		expect(lists).toHaveLength(2);
		expect(screen.getAllByRole("listitem")).toHaveLength(4);
	});

	it("renders a task list with read-only checkboxes reflecting the agent's state", () => {
		render(<ChatMarkdown text={"- [x] done thing\n- [ ] pending thing"} />);
		const boxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
		expect(boxes).toHaveLength(2);
		expect(boxes[0]!.checked).toBe(true);
		expect(boxes[1]!.checked).toBe(false);
		// Clicking must not imply AO can rewrite the agent's plan.
		expect(boxes[0]!.readOnly).toBe(true);
	});

	it("renders a GFM table, in a container that scrolls instead of widening the column", () => {
		render(<ChatMarkdown text={"| file | lines |\n| --- | --- |\n| a.ts | 12 |"} />);
		const table = screen.getByRole("table");
		expect(table).toBeInTheDocument();
		expect(screen.getByRole("columnheader", { name: "file" })).toBeInTheDocument();
		expect(screen.getByRole("cell", { name: "a.ts" })).toBeInTheDocument();
		const scroller = table.closest("div");
		expect(scroller?.className).toContain("overflow-x-auto");
	});

	it("compacts emoji status markers inside markdown tables", () => {
		render(<ChatMarkdown text={"| status |\n| --- |\n| 🟡 idle |\n| ✅ merged |"} />);
		const idle = screen.getByRole("cell", { name: "🟡 idle" });
		const merged = screen.getByRole("cell", { name: "✅ merged" });

		expect(idle.querySelector(".chat-md-emoji")).toHaveTextContent("🟡");
		expect(merged.querySelector(".chat-md-emoji")).toHaveTextContent("✅");
	});

	it("keeps skin-tone and ZWJ emoji together as grapheme clusters", () => {
		render(<ChatMarkdown text={"Ready 👍🏽 with family 👨‍👩‍👧‍👦 and flag 🏳️‍🌈."} />);
		const emoji = [...document.querySelectorAll(".chat-md-emoji")];
		expect(emoji.map((node) => node.textContent)).toEqual(["👍🏽", "👨‍👩‍👧‍👦", "🏳️‍🌈"]);
	});

	it("renders a fenced code block with its language and a copy control, and no stray backticks", () => {
		render(<ChatMarkdown text={"```go\nfunc main() {}\n```"} />);
		expect(screen.getByText("go")).toBeInTheDocument();
		expect(screen.getByText("func main() {}")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /copy code/i })).toBeInTheDocument();
		expect(document.body.textContent).not.toContain("```");
	});

	it("keeps inline code inline rather than promoting it to a block", () => {
		render(<ChatMarkdown text={"run `go test ./...` first"} />);
		const code = screen.getByText("go test ./...");
		expect(code.tagName).toBe("CODE");
		expect(code.closest("pre")).toBeNull();
		expect(code).toHaveClass("text-markdown-code");
	});

	it("uses the AO logo colour for file paths and inline notation", () => {
		render(
			<ChatMarkdown
				text={"See `backend/internal/adapters/agent/` and then pass `--resume` to `ao`."}
			/>,
		);

		expect(screen.getByText("backend/internal/adapters/agent/")).toHaveClass("text-markdown-code");
		expect(screen.getByText("--resume")).toHaveClass("text-markdown-code");
		expect(screen.getByText("ao")).toHaveClass("text-markdown-code");
	});

	it("recognizes standalone filenames and paths with line locations", () => {
		render(<ChatMarkdown text={"Compare `README.md` with `backend/service.go:42`."} />);
		expect(screen.getByText("README.md")).toHaveClass("text-markdown-code");
		expect(screen.getByText("backend/service.go:42")).toHaveClass("text-markdown-code");
	});

	it("escapes raw HTML instead of rendering it", () => {
		// Agent output is only as trustworthy as the files it just read, so an
		// <img onerror> in a README must never become a live element.
		render(<ChatMarkdown text={'before <img src=x onerror="alert(1)"> after'} />);
		expect(document.querySelector("img")).toBeNull();
		expect(document.body.textContent).toContain("onerror");
	});

	it("marks external links to open outside the app", () => {
		render(<ChatMarkdown text={"see [the issue](https://example.com/i/1)"} />);
		const link = screen.getByRole("link", { name: "the issue" });
		expect(link).toHaveAttribute("href", "https://example.com/i/1");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveClass("text-markdown-link", "hover:text-markdown-link-hover");
		// Without noreferrer the opened page gets a handle on the renderer.
		expect(link.getAttribute("rel")).toContain("noreferrer");
	});

	it("routes a plain web-link click to the AO Browser handler", async () => {
		const user = userEvent.setup();
		const onLinkOpen = vi.fn();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderWithLinkHandler("see [the issue](https://example.com/i/1)", onLinkOpen);

		await user.click(screen.getByRole("link", { name: "the issue" }));

		expect(onLinkOpen).toHaveBeenCalledWith("https://example.com/i/1");
		expect(openExternal).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it.each([
		"ao://sessions/project/session",
		"[Open session](ao://sessions/project/session)",
	])("renders and activates a canonical session link: %s", async (text) => {
		const onSessionLinkOpen = vi.fn();
		renderWithSessionLinkHandler(text, onSessionLinkOpen);
		const link = screen.getByRole("link");
		expect(link).toHaveAttribute("href", "ao://sessions/project/session");
		await userEvent.setup().click(link);
		expect(onSessionLinkOpen).toHaveBeenCalledWith("ao://sessions/project/session");
	});

	it("does not auto-activate a session link while streaming", () => {
		const onSessionLinkOpen = vi.fn();
		render(<ChatLinkProvider onSessionLinkOpen={onSessionLinkOpen}><ChatMarkdown streaming text="ao://sessions/project/session" /></ChatLinkProvider>);
		expect(screen.getByRole("link")).toBeInTheDocument();
		expect(onSessionLinkOpen).not.toHaveBeenCalled();
	});

	it("leaves malformed session-like text inert", () => {
		renderWithSessionLinkHandler("ao://sessions/project/session/kill", vi.fn());
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});

	it("opens a web link in the system browser on Option/Alt-click", () => {
		const onLinkOpen = vi.fn();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderWithLinkHandler("see [the issue](https://example.com/i/1)", onLinkOpen);

		fireEvent.click(screen.getByRole("link", { name: "the issue" }), { altKey: true });

		expect(openExternal).toHaveBeenCalledWith("https://example.com/i/1");
		expect(onLinkOpen).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it("opens a web link in the system browser on Cmd-click", () => {
		const onLinkOpen = vi.fn();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderWithLinkHandler("see [the issue](https://example.com/i/1)", onLinkOpen);

		fireEvent.click(screen.getByRole("link", { name: "the issue" }), { metaKey: true });

		expect(openExternal).toHaveBeenCalledWith("https://example.com/i/1");
		expect(onLinkOpen).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it("opens a web link in the system browser on Ctrl-click", () => {
		const onLinkOpen = vi.fn();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderWithLinkHandler("see [the issue](https://example.com/i/1)", onLinkOpen);

		fireEvent.click(screen.getByRole("link", { name: "the issue" }), { ctrlKey: true });

		expect(openExternal).toHaveBeenCalledWith("https://example.com/i/1");
		expect(onLinkOpen).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it("offers 'Open in system browser' on right-click, without opening in the panel", async () => {
		const user = userEvent.setup();
		const onLinkOpen = vi.fn();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderWithLinkHandler("see [the issue](https://example.com/i/1)", onLinkOpen);

		fireEvent.contextMenu(screen.getByRole("link", { name: "the issue" }));
		await user.click(await screen.findByRole("menuitem", { name: "Open in system browser" }));

		expect(openExternal).toHaveBeenCalledWith("https://example.com/i/1");
		expect(onLinkOpen).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it("offers 'Copy link address' on right-click", async () => {
		const user = userEvent.setup();
		const writeText = vi.spyOn(aoBridge.clipboard, "writeText").mockResolvedValue(undefined);
		renderWithLinkHandler("see [the issue](https://example.com/i/1)", vi.fn());

		fireEvent.contextMenu(screen.getByRole("link", { name: "the issue" }));
		await user.click(await screen.findByRole("menuitem", { name: "Copy link address" }));

		expect(writeText).toHaveBeenCalledWith("https://example.com/i/1");
		writeText.mockRestore();
	});

	it("omits the system-browser item for non-web links but still offers copying", async () => {
		// mailto must never reach shell.openExternal from the menu.
		renderWithLinkHandler("[Email support](mailto:support@example.com)", vi.fn());

		fireEvent.contextMenu(screen.getByRole("link", { name: "Email support" }));

		expect(await screen.findByRole("menuitem", { name: "Copy link address" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Open in system browser" })).not.toBeInTheDocument();
	});

	it("opens non-web links in the system browser", async () => {
		const user = userEvent.setup();
		const onLinkOpen = vi.fn();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderWithLinkHandler("[Email support](mailto:support@example.com)", onLinkOpen);

		await user.click(screen.getByRole("link", { name: "Email support" }));

		expect(openExternal).toHaveBeenCalledWith("mailto:support@example.com");
		expect(onLinkOpen).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it("renders bold, strikethrough and blockquotes", () => {
		render(<ChatMarkdown text={"**bold** and ~~gone~~\n\n> quoted"} />);
		expect(screen.getByText("bold").tagName).toBe("STRONG");
		expect(screen.getByText("gone").tagName).toBe("DEL");
		expect(screen.getByText("quoted").closest("blockquote")).not.toBeNull();
	});

	it("renders an unterminated fence as a code block, because streaming text arrives mid-fence", () => {
		render(<ChatMarkdown text={"```ts\nconst x = 1;"} />);
		expect(document.querySelector("pre code")).toHaveTextContent("const x = 1;");
		expect(document.body.textContent).not.toContain("```");
	});

	it("renders a mermaid fence as a diagram rather than source text", async () => {
		render(<ChatMarkdown text={"```mermaid\nflowchart TD\n    A --> B\n```"} />);

		expect(await screen.findByTestId("mermaid-diagram")).toBeInTheDocument();
		expect(vi.mocked(renderMermaidDiagram)).toHaveBeenCalledWith(
			"flowchart TD\n    A --> B",
			expect.stringMatching(/light|dark/),
		);
	});

	it("treats the mermaid label case-insensitively", async () => {
		render(<ChatMarkdown text={"```Mermaid\nflowchart TD\n    A --> B\n```"} />);

		expect(await screen.findByTestId("mermaid-diagram")).toBeInTheDocument();
	});

	it("keeps a mermaid fence as source text while streaming", () => {
		render(<ChatMarkdown text={"```mermaid\nflowchart TD\n    A --> B\n```"} streaming />);

		expect(screen.queryByTestId("mermaid-diagram")).not.toBeInTheDocument();
		expect(screen.getByText(/A --> B/)).toBeInTheDocument();
		expect(vi.mocked(renderMermaidDiagram)).not.toHaveBeenCalled();
	});

	it("renders a fence with no language as a block, not as inline code", () => {
		// Matching on the `language-*` class alone used to send these down the inline
		// path, where a whole `go test` transcript rendered as one accent-coloured run.
		render(<ChatMarkdown text={"```\nok\tgithub.com/aoagents/ao\t0.4s\n```"} />);
		const code = screen.getByText(/aoagents/);
		expect(code.closest("pre")).not.toBeNull();
		expect(screen.getByRole("button", { name: /copy code/i })).toBeInTheDocument();
	});
});

/* -------------------------------------------------------------------------- */

describe("ChatMarkdown code highlighting", () => {
	const block = (language: string, code: string) => `\`\`\`${language}\n${code}\n\`\`\``;

	/** The tokens land as `hljs-*` classes; `code-theme.css` colours them. */
	function tokens(): Element[] {
		return [...document.querySelectorAll("pre [class*='hljs-']")];
	}

	it("highlights a known language, leaving the code itself untouched", async () => {
		render(<ChatMarkdown text={block("go", "func main() {}")} />);
		await waitFor(() => expect(tokens().length).toBeGreaterThan(0), { timeout: 5_000 });
		expect(document.querySelector("pre")?.textContent).toBe("func main() {}");
	});

	it("leaves an unknown language as plain monospace rather than guessing", async () => {
		render(<ChatMarkdown text={block("brainfuck", "++++[>++++<-]")} />);
		// Nothing to wait for — there is no grammar to load — so a flush is enough to
		// prove no upgrade is coming.
		await waitFor(() => expect(screen.getByText("++++[>++++<-]")).toBeInTheDocument());
		expect(tokens()).toHaveLength(0);
		expect(screen.getByText("brainfuck")).toBeInTheDocument();
	});

	it("does not tokenize a fence that is still streaming, and does once it settles", async () => {
		const code = "type Session struct {\n\tID string\n}";
		const { rerender } = render(<ChatMarkdown text={block("go", code)} streaming />);
		expect(tokens()).toHaveLength(0);
		expect(document.querySelector("pre")?.textContent).toBe(code);

		rerender(<ChatMarkdown text={block("go", code)} />);
		await waitFor(() => expect(tokens().length).toBeGreaterThan(0));
		expect(document.querySelector("pre")?.textContent).toBe(code);
	});

	it("shows a settled block highlighted on its first render, from cache", async () => {
		const code = "SELECT 1 FROM turns;";
		const first = render(<ChatMarkdown text={block("sql", code)} />);
		await waitFor(() => expect(tokens().length).toBeGreaterThan(0));
		first.unmount();

		// A remount is what scrolling does. Without the cache this would flash plain
		// again; with it the very first commit is already highlighted.
		render(<ChatMarkdown text={block("sql", code)} />);
		expect(tokens().length).toBeGreaterThan(0);
	});

	it("toggles wrapping for long lines without re-tokenizing", async () => {
		const user = userEvent.setup();
		render(<ChatMarkdown text={block("sh", "echo one && echo two && echo three")} />);
		const wrap = screen.getByRole("button", { name: /wrap long lines/i });
		const wrapper = document.querySelector(".chat-code");

		expect(wrapper).toHaveAttribute("data-wrap", "false");
		await user.click(wrap);
		expect(wrapper).toHaveAttribute("data-wrap", "true");
		expect(wrap).toHaveAttribute("aria-pressed", "true");

		await user.click(wrap);
		expect(wrapper).toHaveAttribute("data-wrap", "false");
	});
});


describe("ActivityTitle", () => {
	it("keeps code delimiters inside multi-backtick code spans", () => {
		const { container } = render(<ActivityTitle text={"Edit ``file`name.ts``"} />);
		expect(container.querySelector("code")).toHaveTextContent("file`name.ts");
	});

	it("keeps disclosure titles inline and non-interactive", () => {
		const { container } = render(
			<button><ActivityTitle text={'# **Edit** [file](https://example.com) `path.ts` ![image](https://example.com/image.png) <input autofocus />'} /></button>,
		);
		expect(screen.getByRole("button")).toHaveTextContent("Edit file path.ts");
		expect(container.querySelector("strong")).toHaveTextContent("Edit");
		expect(container.querySelector("a, img, input, p, h1, pre")).toBeNull();
	});
});
