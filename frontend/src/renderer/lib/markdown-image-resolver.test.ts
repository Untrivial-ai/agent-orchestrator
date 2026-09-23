import { describe, expect, it } from "vitest";
import {
	isAbsoluteMarkdownAssetSrc,
	resolveMarkdownAssetPath,
	resolveMarkdownImageSrc,
} from "./markdown-image-resolver";

describe("isAbsoluteMarkdownAssetSrc", () => {
	it("treats scheme-qualified and root-relative sources as absolute", () => {
		expect(isAbsoluteMarkdownAssetSrc("https://example.com/a.png")).toBe(true);
		expect(isAbsoluteMarkdownAssetSrc("data:image/png;base64,AAA")).toBe(true);
		expect(isAbsoluteMarkdownAssetSrc("/assets/a.png")).toBe(true);
	});

	it("treats worktree-relative sources as relative", () => {
		expect(isAbsoluteMarkdownAssetSrc("./a.png")).toBe(false);
		expect(isAbsoluteMarkdownAssetSrc("../a.png")).toBe(false);
		expect(isAbsoluteMarkdownAssetSrc("assets/a.png")).toBe(false);
	});
});

describe("resolveMarkdownAssetPath", () => {
	it("resolves against the markdown file's own directory", () => {
		expect(resolveMarkdownAssetPath("docs/guide.md", "./assets/flow.png")).toBe("docs/assets/flow.png");
		expect(resolveMarkdownAssetPath("docs/guide.md", "assets/flow.png")).toBe("docs/assets/flow.png");
	});

	it("resolves a file at the workspace root", () => {
		expect(resolveMarkdownAssetPath("README.md", "./logo.png")).toBe("logo.png");
	});

	it("walks up for `..`", () => {
		expect(resolveMarkdownAssetPath("docs/deep/guide.md", "../assets/flow.png")).toBe("docs/assets/flow.png");
	});

	it("clamps at the workspace root instead of escaping it", () => {
		expect(resolveMarkdownAssetPath("docs/guide.md", "../../../../etc/passwd")).toBe("etc/passwd");
	});

	it("drops a trailing query or fragment", () => {
		expect(resolveMarkdownAssetPath("docs/guide.md", "./flow.png?v=2")).toBe("docs/flow.png");
		expect(resolveMarkdownAssetPath("docs/guide.md", "./flow.svg#layer")).toBe("docs/flow.svg");
	});

	it("decodes URI-encoded filenames before building the workspace path", () => {
		expect(resolveMarkdownAssetPath("docs/guide.md", "./flow%20chart.png")).toBe("docs/flow chart.png");
	});

	it("does not throw on malformed percent encoding", () => {
		expect(() => resolveMarkdownAssetPath("docs/guide.md", "./bad%2name.png")).not.toThrow();
	});
});

describe("resolveMarkdownImageSrc", () => {
	it("passes an absolute source through untouched", () => {
		expect(resolveMarkdownImageSrc("s1", "README.md", "https://example.com/a.png", 7)).toBe(
			"https://example.com/a.png",
		);
	});

	it("returns undefined for an empty source", () => {
		expect(resolveMarkdownImageSrc("s1", "README.md", undefined, 7)).toBeUndefined();
		expect(resolveMarkdownImageSrc("s1", "README.md", "", 7)).toBeUndefined();
	});

	it("points a relative source at the workspace blob route", () => {
		const url = resolveMarkdownImageSrc("session-1", "docs/guide.md", "./assets/flow%20chart.png", 7);
		expect(url).toContain("/api/v1/sessions/session-1/workspace/file/blob?");
		expect(url).toContain("path=docs%2Fassets%2Fflow+chart.png");
		expect(url).toContain("side=after");
	});

	it("carries the version so an edited image is refetched rather than served from cache", () => {
		const before = resolveMarkdownImageSrc("session-1", "docs/guide.md", "./flow.png", 100);
		const after = resolveMarkdownImageSrc("session-1", "docs/guide.md", "./flow.png", 200);
		expect(before).toContain("v=100");
		expect(after).toContain("v=200");
		expect(before).not.toBe(after);
	});
});
