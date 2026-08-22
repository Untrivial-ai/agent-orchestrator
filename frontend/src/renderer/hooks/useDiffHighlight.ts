import { useMemo, useReducer } from "react";
import { highlight, highlightSync } from "../lib/code-highlight";
import { composeLineRuns, languageForPath, splitHastByLine, type DiffRun } from "../lib/diff-highlight";
import type { DiffRow } from "../lib/diff-parser";

type HunkLine = { text: string; rowIndex: number };
type Hunk = { old: HunkLine[]; new: HunkLine[] };

// Groups rows into per-hunk old/new line sequences. A hunk's old-side sequence
// (context + del, in order) and new-side sequence (context + add, in order) each
// reconstruct a contiguous excerpt of the real file, which is what lets tokenizing
// each side as one blob carry highlight.js's state (inside a string, a block
// comment, ...) correctly across line breaks.
function groupHunks(rows: DiffRow[]): Hunk[] {
	const hunks: Hunk[] = [];
	let current: Hunk | null = null;
	rows.forEach((row, rowIndex) => {
		if (row.kind === "hunk") {
			current = { old: [], new: [] };
			hunks.push(current);
			return;
		}
		if (!current) return;
		if (row.kind === "context") {
			current.old.push({ text: row.text, rowIndex });
			current.new.push({ text: row.text, rowIndex });
		} else if (row.kind === "del") {
			current.old.push({ text: row.text, rowIndex });
		} else if (row.kind === "add") {
			current.new.push({ text: row.text, rowIndex });
		}
	});
	return hunks;
}

// Composed syntax + word-diff runs for every row in a diff, parallel to `rows`.
// Colors a row as soon as its hunk-side blob can be tokenized; a diff that opens
// before the grammar chunk has loaded renders in plain/segment-only form first and
// pops in colored once loading resolves, matching HighlightedCode's existing
// chat-code-block behavior rather than inventing a new loading pattern.
export function useDiffHighlight(rows: DiffRow[], path: string): DiffRun[][] {
	const lang = useMemo(() => languageForPath(path), [path]);
	const [rereadCount, reread] = useReducer((count: number) => count + 1, 0);

	return useMemo(() => {
		const runs: DiffRun[][] = rows.map((row) => composeLineRuns(undefined, row.segments, row.text));
		if (!lang) return runs;

		for (const hunk of groupHunks(rows)) {
			for (const side of [hunk.old, hunk.new]) {
				if (side.length === 0) continue;
				const blob = side.map((line) => line.text).join("\n");
				const tree = highlightSync(blob, lang);
				if (!tree) {
					void highlight(blob, lang).then(() => reread());
					continue;
				}
				const perLine = splitHastByLine(tree, side.length);
				side.forEach((line, i) => {
					const row = rows[line.rowIndex];
					runs[line.rowIndex] = composeLineRuns(perLine[i], row.segments, row.text);
				});
			}
		}
		return runs;
		// rereadCount forces a recompute once a pending highlight() resolves; the
		// underlying highlightSync cache makes that recompute cheap (already-loaded
		// blobs hit the cache instead of re-tokenizing).
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [rows, lang, rereadCount]);
}
