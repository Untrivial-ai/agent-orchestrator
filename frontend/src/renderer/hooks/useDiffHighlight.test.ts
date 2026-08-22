import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { parseUnifiedDiff } from "../lib/diff-parser";
import { useDiffHighlight } from "./useDiffHighlight";

function classesOf(runs: { className?: string }[]): string[] {
	return runs.flatMap((run) => run.className?.split(" ") ?? []);
}

describe("useDiffHighlight", () => {
	it("falls back to plain text runs for a file whose extension has no bundled grammar", () => {
		const rows = parseUnifiedDiff("@@ -1,1 +1,1 @@\n-old\n+new\n");
		const { result } = renderHook(() => useDiffHighlight(rows, "notes.unknown"));
		expect(result.current).toHaveLength(rows.length);
		// No grammar resolved -> never colored, but the existing word-diff highlight
		// (the "changed" flag) must still come through unaffected.
		const delRun = result.current[rows.findIndex((r) => r.kind === "del")];
		expect(classesOf(delRun)).toEqual([]);
		expect(delRun.some((run) => run.changed)).toBe(true);
	});

	it("colors a del line using state carried over from a preceding context line", async () => {
		// "old body" is only recognizable as a comment when tokenized together with
		// the opening "/* start" context line in the same call — this is the
		// per-hunk-side (not per-line) tokenization the design depends on.
		const diff = "@@ -1,4 +1,4 @@\n /* start\n-old body\n+new body\n end */\n";
		const rows = parseUnifiedDiff(diff);
		const { result, rerender } = renderHook(({ r, p }) => useDiffHighlight(r, p), {
			initialProps: { r: rows, p: "src/App.ts" },
		});

		await waitFor(
			() => {
				const delIndex = rows.findIndex((r) => r.kind === "del");
				const classes = classesOf(result.current[delIndex]);
				expect(classes.some((c) => c === "hljs-comment")).toBe(true);
			},
			{ timeout: 5000 },
		);

		// The paired add line resolves through the new-side blob the same way.
		const addIndex = rows.findIndex((r) => r.kind === "add");
		expect(classesOf(result.current[addIndex])).toContain("hljs-comment");

		// A second render with the identical rows array and path must not throw and
		// must keep returning the same shape (memoized — no re-tokenization needed).
		act(() => rerender({ r: rows, p: "src/App.ts" }));
		expect(result.current).toHaveLength(rows.length);
	});

	it("still applies the intra-line word-diff highlight on a colored line", async () => {
		const diff = "@@ -1,1 +1,1 @@\n-const value = 0;\n+const value = 1;\n";
		const rows = parseUnifiedDiff(diff);
		const { result } = renderHook(() => useDiffHighlight(rows, "src/App.ts"));

		await waitFor(() => {
			const delIndex = rows.findIndex((r) => r.kind === "del");
			expect(classesOf(result.current[delIndex]).length).toBeGreaterThan(0);
		});

		const delIndex = rows.findIndex((r) => r.kind === "del");
		const addIndex = rows.findIndex((r) => r.kind === "add");
		const changedDel = result.current[delIndex].filter((run) => run.changed);
		const changedAdd = result.current[addIndex].filter((run) => run.changed);
		expect(changedDel.map((run) => run.text).join("")).toBe("0");
		expect(changedAdd.map((run) => run.text).join("")).toBe("1");
	});
});
