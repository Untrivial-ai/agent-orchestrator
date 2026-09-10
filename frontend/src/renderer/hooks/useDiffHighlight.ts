import { useMemo, useReducer } from "react";
import { highlight, highlightSync } from "../lib/code-highlight";
import { composeLineRuns, languageForPath, splitHastByLine, type DiffRun } from "../lib/diff-highlight";
import type { GrammarName } from "../lib/code-highlight";
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

// Composed syntax + word-diff runs for one side (old or new) of every row, parallel
// to `rows`. A row that isn't part of this side's hunk sequences (e.g. a pure add
// on the old side) simply keeps the plain/segment-only fallback — nothing ever
// reads it, since callers pick old for del rows and new for add/context rows.
function highlightSide(
	hunks: Hunk[],
	rows: DiffRow[],
	pickSide: (hunk: Hunk) => HunkLine[],
	lang: GrammarName | undefined,
	reread: () => void,
): DiffRun[][] {
	const runs: DiffRun[][] = rows.map((row) => composeLineRuns(undefined, row.segments, row.text));
	if (!lang) return runs;

	for (const hunk of hunks) {
		const side = pickSide(hunk);
		if (side.length === 0) continue;
		const blob = side.map((line) => line.text).join("\n");
		const tree = highlightSync(blob, lang);
		if (!tree) {
			// Only schedule a recompute if this attempt could actually change the
			// outcome. If it resolves to another "unavailable" (engine failed to
			// load, blob too large, ...) there is nothing new to render, and
			// rereading anyway would recompute this same failed lookup forever.
			void highlight(blob, lang).then((resolved) => {
				if (resolved) reread();
			});
			continue;
		}
		const perLine = splitHastByLine(tree, side.length);
		side.forEach((line, i) => {
			const row = rows[line.rowIndex];
			runs[line.rowIndex] = composeLineRuns(perLine[i], row.segments, row.text);
		});
	}
	return runs;
}

export type DiffHighlight = {
	/** Meaningful for context and del rows — the old-file syntax state. */
	oldSide: DiffRun[][];
	/** Meaningful for context and add rows — the new-file syntax state. */
	newSide: DiffRun[][];
};

// Composed syntax + word-diff runs for every row in a diff, split by old/new side.
// Old and new are tokenized independently, each against its own file's language
// (a rename that changes extension can legitimately use a different grammar per
// side) and each producing its own colors — a context line's syntax role can
// legitimately differ between old and new when the change alters multi-line state
// (opens or closes a comment/string) around it, so a single shared answer per row
// would be wrong for one side of a split diff. Colors a row as soon as its
// hunk-side blob can be tokenized; a diff that opens before the grammar chunk has
// loaded renders in plain/segment-only form first and pops in colored once loading
// resolves, matching HighlightedCode's existing chat-code-block behavior.
export function useDiffHighlight(rows: DiffRow[], path: string, previousPath: string | undefined): DiffHighlight {
	const oldLang = useMemo(() => languageForPath(previousPath ?? path), [previousPath, path]);
	const newLang = useMemo(() => languageForPath(path), [path]);
	const [rereadCount, reread] = useReducer((count: number) => count + 1, 0);

	return useMemo(() => {
		const hunks = groupHunks(rows);
		return {
			oldSide: highlightSide(hunks, rows, (hunk) => hunk.old, oldLang, reread),
			newSide: highlightSide(hunks, rows, (hunk) => hunk.new, newLang, reread),
		};
		// rereadCount forces a recompute once a pending highlight() resolves; the
		// underlying highlightSync cache makes that recompute cheap (already-loaded
		// blobs hit the cache instead of re-tokenizing).
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [rows, oldLang, newLang, rereadCount]);
}
