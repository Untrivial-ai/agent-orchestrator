import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { parseUnifiedDiff } from "../lib/diff-parser";
import * as codeHighlight from "../lib/code-highlight";
import { useDiffHighlight } from "./useDiffHighlight";

function classesOf(runs: { className?: string }[]): string[] {
	return runs.flatMap((run) => run.className?.split(" ") ?? []);
}

afterEach(() => {
	vi.restoreAllMocks();
});

describe("useDiffHighlight", () => {
	it("falls back to plain text runs for a file whose extension has no bundled grammar", () => {
		const rows = parseUnifiedDiff("@@ -1,1 +1,1 @@\n-old\n+new\n");
		const { result } = renderHook(() => useDiffHighlight(rows, "notes.unknown", undefined));
		expect(result.current.newSide).toHaveLength(rows.length);
		expect(result.current.oldSide).toHaveLength(rows.length);
		// No grammar resolved -> never colored, but the existing word-diff highlight
		// (the "changed" flag) must still come through unaffected.
		const delIndex = rows.findIndex((r) => r.kind === "del");
		const delRun = result.current.oldSide[delIndex];
		expect(classesOf(delRun)).toEqual([]);
		expect(delRun.some((run) => run.changed)).toBe(true);
	});

	it("colors a del line using state carried over from a preceding context line", async () => {
		// "old body" is only recognizable as a comment when tokenized together with
		// the opening "/* start" context line in the same call — this is the
		// per-hunk-side (not per-line) tokenization the design depends on.
		const diff = "@@ -1,4 +1,4 @@\n /* start\n-old body\n+new body\n end */\n";
		const rows = parseUnifiedDiff(diff);
		const { result, rerender } = renderHook(({ r, p }) => useDiffHighlight(r, p, undefined), {
			initialProps: { r: rows, p: "src/App.ts" },
		});

		await waitFor(
			() => {
				const delIndex = rows.findIndex((r) => r.kind === "del");
				const classes = classesOf(result.current.oldSide[delIndex]);
				expect(classes.some((c) => c === "hljs-comment")).toBe(true);
			},
			{ timeout: 5000 },
		);

		// The paired add line resolves through the new-side blob the same way.
		const addIndex = rows.findIndex((r) => r.kind === "add");
		expect(classesOf(result.current.newSide[addIndex])).toContain("hljs-comment");

		// A second render with the identical rows array and path must not throw and
		// must keep returning the same shape (memoized — no re-tokenization needed).
		act(() => rerender({ r: rows, p: "src/App.ts" }));
		expect(result.current.newSide).toHaveLength(rows.length);
	});

	it("still applies the intra-line word-diff highlight on a colored line", async () => {
		const diff = "@@ -1,1 +1,1 @@\n-const value = 0;\n+const value = 1;\n";
		const rows = parseUnifiedDiff(diff);
		const { result } = renderHook(() => useDiffHighlight(rows, "src/App.ts", undefined));

		await waitFor(() => {
			const delIndex = rows.findIndex((r) => r.kind === "del");
			expect(classesOf(result.current.oldSide[delIndex]).length).toBeGreaterThan(0);
		});

		const delIndex = rows.findIndex((r) => r.kind === "del");
		const addIndex = rows.findIndex((r) => r.kind === "add");
		const changedDel = result.current.oldSide[delIndex].filter((run) => run.changed);
		const changedAdd = result.current.newSide[addIndex].filter((run) => run.changed);
		expect(changedDel.map((run) => run.text).join("")).toBe("0");
		expect(changedAdd.map((run) => run.text).join("")).toBe("1");
	});

	it("keeps old-side and new-side syntax state independent for a shared context row", async () => {
		// The change opens a comment that only exists on the old side: "shared
		// context" is inside a /* ... */ block on the old side, but not on the new
		// side (the new side never opens a comment before it). A single shared
		// `runs[rowIndex]` can only hold one answer; split view needs both.
		const diff = "@@ -1,3 +1,3 @@\n-/* old-only\n+const value = 1;\n shared context\n end */\n";
		const rows = parseUnifiedDiff(diff);
		const { result } = renderHook(() => useDiffHighlight(rows, "src/App.ts", undefined));

		const contextIndex = rows.findIndex((r) => r.text === "shared context");
		await waitFor(() => {
			expect(classesOf(result.current.oldSide[contextIndex])).toContain("hljs-comment");
		});
		expect(classesOf(result.current.newSide[contextIndex])).not.toContain("hljs-comment");
	});

	it("does not retry forever once the engine reports highlighting unavailable", async () => {
		const highlightSyncSpy = vi.spyOn(codeHighlight, "highlightSync").mockReturnValue(undefined);
		const highlightSpy = vi
			.spyOn(codeHighlight, "highlight")
			.mockResolvedValueOnce(undefined)
			.mockResolvedValueOnce(undefined)
			.mockReturnValue(new Promise(() => {})); // any further call would hang the test if made

		const rows = parseUnifiedDiff("@@ -1,1 +1,1 @@\n-const before = 0;\n+const after = 1;\n");
		renderHook(() => useDiffHighlight(rows, "src/App.ts", undefined));

		// One highlight() call per hunk side (old, new); both resolve to undefined
		// (highlighting unavailable) so there is nothing to retry from.
		await waitFor(() => expect(highlightSpy).toHaveBeenCalledTimes(2));
		await new Promise((resolve) => setTimeout(resolve, 100));
		expect(highlightSpy).toHaveBeenCalledTimes(2);
		expect(highlightSyncSpy).toHaveBeenCalled();
	});

	it("uses the previous extension's grammar for the old side of an extension-changing rename", async () => {
		const diff = "@@ -1,1 +1,1 @@\n-const oldValue = 0;\n+# heading\n";
		const rows = parseUnifiedDiff(diff);
		const { result } = renderHook(() => useDiffHighlight(rows, "docs/new.md", "src/old.ts"));

		const delIndex = rows.findIndex((r) => r.kind === "del");
		await waitFor(() => {
			expect(classesOf(result.current.oldSide[delIndex])).toContain("hljs-keyword");
		});
	});
});
