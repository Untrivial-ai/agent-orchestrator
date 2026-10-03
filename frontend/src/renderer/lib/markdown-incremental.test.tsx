/*
 * Adapted from pingdotgg/t3code, markdown-incremental.test.tsx
 * e8545b293b79cdd73d30ef921db149ef1b5abee0.
 *
 * MIT License
 *
 * Copyright (c) 2026 T3 Tools Inc.
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */

import type { Root } from "mdast";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Plugin } from "unified";
import { describe, expect, it } from "vitest";

import { createIncrementalMarkdownPlugin } from "./markdown-incremental";
import { remarkSessionLinks } from "./session-links";

function render(source: string, incremental?: Plugin<[], Root>, parsedSources?: string[]) {
	let tree: Root | undefined;
	const observeParsing: Plugin<[], Root> = function () {
		const original = this.parser;
		if (original) {
			this.parser = (text, file) => {
				parsedSources?.push(text);
				return original(text, file);
			};
		}
	};
	const capture: Plugin<[], Root> = () => (root) => {
		tree = structuredClone(root);
	};
	const html = renderToStaticMarkup(
		<ReactMarkdown
			remarkPlugins={[
				observeParsing,
				capture,
				remarkGfm,
				remarkSessionLinks,
				...(incremental ? [incremental] : []),
			]}
		>
			{source}
		</ReactMarkdown>,
	);
	return { html, tree };
}

const prefix = "# Before\n\n```ts\nconst values = [1, 2];\n```\n\n";

describe("incremental Markdown parsing", () => {
	it("parses only the live suffix after a completed top-level fence", () => {
		const incremental = createIncrementalMarkdownPlugin();
		const parsedSources: string[] = [];
		expect(render(prefix + "Answer", incremental, parsedSources)).toEqual(render(prefix + "Answer"));
		parsedSources.length = 0;
		const next = prefix + "Answer more";
		expect(render(next, incremental, parsedSources)).toEqual(render(next));
		expect(parsedSources).toEqual(["Answer more"]);
	});

	it.each([
		"a\n===\n\nb\n---\n",
		"- first\n\n  continued\n\n- next\n",
		"> quoted\n>\n> ```js\n> abc\n> ```\n\nend",
		"<div>\nhello\n\n</div>\n\nend",
		"[ref]\n\n[ref]: /later",
		"a[^x]\n\n[^x]: note",
		"a | b\n--|--\na | b\n",
		"```\na\n```\n\nnext\n\n~~~\nb\n~~~\n\nmore",
		"\n\n\tcode\n\nmore",
		"text <https://example.com> *bold*",
		"> [!NOTE]\n> alert\n\n- [ ] task",
		"\uFEFFtext after a byte-order mark",
	])("preserves the parse tree, positions, and HTML while streaming %j", (tail) => {
		const source = prefix + tail;
		const incremental = createIncrementalMarkdownPlugin();
		for (let end = 0; end <= source.length; end++) {
			const text = source.slice(0, end);
			expect(render(text, incremental), `prefix ${end}`).toEqual(render(text));
		}
	});

	it.each(["\r\n", "\r"])("preserves partial %j line endings", (newline) => {
		const source = (prefix + "next\n\n```\nlast\n```\n\nend").replaceAll("\n", newline);
		const incremental = createIncrementalMarkdownPlugin();
		for (let end = 0; end <= source.length; end++) {
			const text = source.slice(0, end);
			expect(render(text, incremental)).toEqual(render(text));
		}
	});

	it("updates earlier references when definitions arrive after the cached prefix", () => {
		const before = "[later] and footnote[^note]\n\n" + prefix;
		const incremental = createIncrementalMarkdownPlugin();
		for (const tail of ["text", "[later]: /target", "[later]: /target\n\n[^note]: a note"]) {
			expect(render(before + tail, incremental)).toEqual(render(before + tail));
		}
	});

	it("handles edits, replacements, and repeated renders without leaking transformed nodes", () => {
		const incremental = createIncrementalMarkdownPlugin();
		const documents = [
			prefix + "- first\n - second",
			prefix + "> [!NOTE]\n> transformed alert",
			prefix + "plain text",
			"replacement without fences",
			prefix.replace("Before", "Edited") + "edited prefix",
			prefix + "plain text",
			prefix + "plain text",
		];
		for (const document of documents) {
			expect(render(document, incremental)).toEqual(render(document));
		}
	});

	it("does not freeze unclosed, nested, indented, or mismatched fences", () => {
		const prefixes = [
			"```\nopen\n\n",
			"````\n```\n\n",
			"> ```\n> code\n> ```\n\n",
			"- ```\n  code\n  ```\n\n",
			"    ```\n    code\n    ```\n\n",
			"<script>\n```\ncode\n```\n\n",
		];
		for (const start of prefixes) {
			const incremental = createIncrementalMarkdownPlugin();
			for (const tail of ["", "text", "\n```\n", "\n```\n\nnext"]) {
				expect(render(start + tail, incremental)).toEqual(render(start + tail));
			}
		}
	});
});
