import { parsePatchFiles, type FileDiffMetadata } from "@pierre/diffs";
import { describe, expect, it } from "vitest";
import { endsAtLastHunk, hydratedCopy, patchIdentity } from "./trailingContext";

function parse(path: string, body: string): FileDiffMetadata {
	const patch = `diff --git a/${path} b/${path}\nindex 1111111..2222222 100644\n--- a/${path}\n+++ b/${path}\n${body}`;
	const [file] = parsePatchFiles(patch, `test:${path}:${body.length}`, true).flatMap((entry) => entry.files);
	if (!file) throw new Error("patch did not parse");
	return file;
}

const appendedAtEnd = "@@ -3,3 +3,5 @@\n line3\n line4\n line5\n+added6\n+added7\n";

describe("endsAtLastHunk", () => {
	it("is true when the last hunk ends on a change (no trailing context left)", () => {
		expect(endsAtLastHunk(parse("README.md", appendedAtEnd))).toBe(true);
	});

	it("is true when fewer than three unchanged lines follow the last change", () => {
		expect(endsAtLastHunk(parse("a.txt", "@@ -1,4 +1,4 @@\n line1\n-line2\n+LINE2\n line3\n line4\n"))).toBe(true);
	});

	it("is false when a full three lines of context follow, since the file may continue", () => {
		expect(endsAtLastHunk(parse("a.txt", "@@ -1,5 +1,5 @@\n line1\n-line2\n+LINE2\n line3\n line4\n line5\n"))).toBe(false);
	});

	it("is true when the patch marks the end of the file even after full context", () => {
		expect(endsAtLastHunk(parse("a.txt", "@@ -1,4 +1,4 @@\n-a\n+b\n c\n d\n e\n\\ No newline at end of file\n"))).toBe(true);
	});

	it("ignores new files, which never get the trailing row", () => {
		const patch = "diff --git a/NEW.md b/NEW.md\nnew file mode 100644\nindex 0000000..3333333\n--- /dev/null\n+++ b/NEW.md\n@@ -0,0 +1,2 @@\n+one\n+two\n";
		const [file] = parsePatchFiles(patch, "test:new", true).flatMap((entry) => entry.files);
		expect(file && endsAtLastHunk(file)).toBe(false);
	});
});

describe("patchIdentity", () => {
	it("matches for the same patch parsed twice and differs when a line changes", () => {
		const first = parse("README.md", appendedAtEnd);
		const again = parse("README.md", `${appendedAtEnd}`);
		const edited = parse("README.md", appendedAtEnd.replace("+added7", "+changed7"));
		expect(again).not.toBe(first);
		expect(patchIdentity(again)).toBe(patchIdentity(first));
		expect(patchIdentity(edited)).not.toBe(patchIdentity(first));
	});
});

describe("hydratedCopy", () => {
	it("returns a memoized full-content copy and leaves the parsed patch partial", () => {
		const partial = parse("README.md", appendedAtEnd);
		const oldContents = "line1\nline2\nline3\nline4\nline5\n";
		const files = {
			oldFile: { name: "README.md", contents: oldContents },
			newFile: { name: "README.md", contents: `${oldContents}added6\nadded7\n` },
		};
		const hydrated = hydratedCopy(partial, files);
		expect(hydrated).not.toBeNull();
		expect(hydrated?.isPartial).toBe(false);
		expect(hydrated?.additionLines).toHaveLength(7);
		expect(partial.isPartial).toBe(true);
		expect(hydratedCopy(partial, files)).toBe(hydrated);
	});
});
