import { describe, expect, it } from "vitest";
import type { Root } from "hast";
import { composeLineRuns, languageForPath, splitHastByLine } from "./diff-highlight";
import type { DiffSegment } from "./diff-parser";

// Hand-built hast fixtures — small enough to not need hastscript, and pinning the
// exact shape splitHastByLine walks (root -> element[className] -> text).
function el(className: string, children: Root["children"]): Root["children"][number] {
	return { type: "element", tagName: "span", properties: { className: [className] }, children } as never;
}
function text(value: string): Root["children"][number] {
	return { type: "text", value } as never;
}
function root(children: Root["children"]): Root {
	return { type: "root", children };
}

describe("languageForPath", () => {
	it("resolves common extensions to the grammar highlight.js already bundles", () => {
		expect(languageForPath("src/App.tsx")).toBe("typescript");
		expect(languageForPath("main.go")).toBe("go");
		expect(languageForPath("README.md")).toBe("markdown");
		expect(languageForPath("script.py")).toBe("python");
	});

	it("is case-insensitive, matching canonicalLanguage", () => {
		expect(languageForPath("data.JSON")).toBe("json");
	});

	it("returns undefined for a file with no extension, never a guess", () => {
		expect(languageForPath("Makefile")).toBeUndefined();
	});

	it("returns undefined for an extension with no bundled grammar", () => {
		expect(languageForPath("vendor/pkg.unknown")).toBeUndefined();
	});

	it("uses the last dot in the filename, not in a parent directory", () => {
		expect(languageForPath("src/v1.2/main.rs")).toBe("rust");
	});
});

describe("splitHastByLine", () => {
	it("splits a single line into its leaf runs", () => {
		const tree = root([el("hljs-keyword", [text("func")]), text(" main() {}")]);
		expect(splitHastByLine(tree, 1)).toEqual([
			[
				{ text: "func", className: "hljs-keyword" },
				{ text: " main() {}", className: undefined },
			],
		]);
	});

	it("reopens an ancestor class on every line it spans", () => {
		// One element wraps a 3-line multi-line string, exactly like a template
		// literal or triple-quoted string tokenized by highlight.js in one call.
		const tree = root([el("hljs-string", [text("`line1\nline2\nline3`")])]);
		expect(splitHastByLine(tree, 3)).toEqual([
			[{ text: "`line1", className: "hljs-string" }],
			[{ text: "line2", className: "hljs-string" }],
			[{ text: "line3`", className: "hljs-string" }],
		]);
	});

	it("handles plain text mixed with classed elements across lines", () => {
		const tree = root([text("a\n"), el("hljs-comment", [text("// b")])]);
		expect(splitHastByLine(tree, 2)).toEqual([
			[{ text: "a", className: undefined }],
			[{ text: "// b", className: "hljs-comment" }],
		]);
	});

	it("produces an empty line array for a blank source line", () => {
		const tree = root([text("a\n\nb")]);
		expect(splitHastByLine(tree, 3)).toEqual([
			[{ text: "a", className: undefined }],
			[],
			[{ text: "b", className: undefined }],
		]);
	});
});

describe("composeLineRuns", () => {
	const seg = (text: string, changed: boolean): DiffSegment => ({ text, changed });

	it("falls back to plain text when neither tokens nor segments are available", () => {
		expect(composeLineRuns(undefined, undefined, "const value = 1;")).toEqual([
			{ text: "const value = 1;", className: undefined, changed: false },
		]);
	});

	it("falls back to a single space for a blank line", () => {
		expect(composeLineRuns(undefined, undefined, "")).toEqual([{ text: " ", changed: false }]);
	});

	it("applies only segment highlighting when tokens are unavailable (unsupported language)", () => {
		const segments = [seg("const value = ", false), seg("1", true), seg(";", false)];
		expect(composeLineRuns(undefined, segments, "const value = 1;")).toEqual([
			{ text: "const value = ", className: undefined, changed: false },
			{ text: "1", className: undefined, changed: true },
			{ text: ";", className: undefined, changed: false },
		]);
	});

	it("applies only syntax color when segments are unavailable (context row)", () => {
		const tokens = [
			{ text: "const", className: "hljs-keyword" },
			{ text: " value = 1;", className: undefined },
		];
		expect(composeLineRuns(tokens, undefined, "const value = 1;")).toEqual([
			{ text: "const", className: "hljs-keyword", changed: false },
			{ text: " value = 1;", className: undefined, changed: false },
		]);
	});

	it("composes a token spanning multiple segments", () => {
		// "value1" is one hljs token but split by the segment boundary after "value".
		const tokens = [{ text: "value1", className: "hljs-variable" }];
		const segments = [seg("value", false), seg("1", true)];
		expect(composeLineRuns(tokens, segments, "value1")).toEqual([
			{ text: "value", className: "hljs-variable", changed: false },
			{ text: "1", className: "hljs-variable", changed: true },
		]);
	});

	it("composes a segment spanning multiple tokens", () => {
		// "a+b" is one changed segment but two hljs tokens (identifier, operator, identifier).
		const tokens = [
			{ text: "a", className: "hljs-variable" },
			{ text: "+", className: "hljs-operator" },
			{ text: "b", className: "hljs-variable" },
		];
		const segments = [seg("a+b", true)];
		expect(composeLineRuns(tokens, segments, "a+b")).toEqual([
			{ text: "a", className: "hljs-variable", changed: true },
			{ text: "+", className: "hljs-operator", changed: true },
			{ text: "b", className: "hljs-variable", changed: true },
		]);
	});

	it("composes boundary-aligned tokens and segments without an off-by-one", () => {
		const tokens = [
			{ text: "old", className: "hljs-variable" },
			{ text: "New", className: "hljs-variable" },
		];
		const segments = [seg("old", false), seg("New", true)];
		expect(composeLineRuns(tokens, segments, "oldNew")).toEqual([
			{ text: "old", className: "hljs-variable", changed: false },
			{ text: "New", className: "hljs-variable", changed: true },
		]);
	});
});
