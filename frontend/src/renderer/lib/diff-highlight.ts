// Syntax coloring for diff lines, layered on top of the structural diff
// (diff-parser.ts) and the chat surface's highlight.js engine (code-highlight.ts).
// Pure, no React, no DOM — the sync/async orchestration lives in
// hooks/useDiffHighlight.ts.

import type { Root, RootContent } from "hast";
import { canonicalLanguage, type GrammarName } from "./code-highlight";
import type { DiffSegment } from "./diff-parser";

/** One leaf run of a tokenized diff line: its text and highlight.js class, if any. */
export type LineToken = { text: string; className?: string };

/** One rendered run of a diff line: text, optional syntax class, and whether it changed. */
export type DiffRun = { text: string; className?: string; changed: boolean };

// A file extension is already what canonicalLanguage's alias table expects (it
// resolves fence labels like "ts"/"go"/"py", which are the same strings as the
// extensions themselves) — no separate map to maintain. A path with no extension,
// or one canonicalLanguage doesn't recognize, resolves to undefined: those rows
// render exactly as they do today, never a guess.
export function languageForPath(path: string): GrammarName | undefined {
	const base = path.slice(path.lastIndexOf("/") + 1);
	const dot = base.lastIndexOf(".");
	if (dot === -1) return undefined;
	return canonicalLanguage(base.slice(dot + 1));
}

// Splits a hast tree produced by tokenizing a whole multi-line blob in one call
// into one leaf-run array per source line. Walking the tree once and only
// resetting the line accumulator (never the ancestor class list) on a newline is
// what lets an element spanning multiple lines — a multi-line string or block
// comment — correctly "reopen" on every line it covers.
export function splitHastByLine(root: Root, lineCount: number): LineToken[][] {
	const lines: LineToken[][] = Array.from({ length: lineCount }, () => []);
	let lineIndex = 0;

	function pushRun(text: string, className: string | undefined): void {
		if (text === "") return;
		const line = lines[lineIndex];
		if (!line) return; // defensive: more line breaks than the caller expected
		const last = line[line.length - 1];
		if (last && last.className === className) last.text += text;
		else line.push({ text, className });
	}

	function walk(nodes: readonly RootContent[], classNames: readonly string[]): void {
		for (const node of nodes) {
			if (node.type === "text") {
				const parts = node.value.split("\n");
				const className = classNames.length ? classNames.join(" ") : undefined;
				parts.forEach((part, i) => {
					if (i > 0) lineIndex += 1;
					pushRun(part, className);
				});
			} else if (node.type === "element") {
				const raw = node.properties?.className;
				const names = Array.isArray(raw) ? raw.map(String) : [];
				walk(node.children, names.length ? [...classNames, ...names] : classNames);
			}
		}
	}

	walk(root.children, []);
	return lines;
}

type TextRun = { text: string; className?: string };

// Merges two independent partitions of the same string — highlight.js's token
// boundaries and the existing LCS word-diff's segment boundaries — into their
// finest joint partition, via a two-pointer walk that splits whichever run ends
// first. Both inputs are guaranteed (by construction, see composeLineRuns) to
// cover exactly the same text, so this never needs to guess where one ends.
function mergeRuns(tokenRuns: TextRun[], segRuns: DiffSegment[]): DiffRun[] {
	const result: DiffRun[] = [];
	let ti = 0;
	let si = 0;
	let tOffset = 0;
	let sOffset = 0;
	while (ti < tokenRuns.length && si < segRuns.length) {
		const t = tokenRuns[ti];
		const s = segRuns[si];
		const take = Math.min(t.text.length - tOffset, s.text.length - sOffset);
		const text = t.text.slice(tOffset, tOffset + take);
		pushMerged(result, text, t.className, s.changed);
		tOffset += take;
		sOffset += take;
		if (tOffset >= t.text.length) {
			ti += 1;
			tOffset = 0;
		}
		if (sOffset >= s.text.length) {
			si += 1;
			sOffset = 0;
		}
	}
	return result;
}

function pushMerged(result: DiffRun[], text: string, className: string | undefined, changed: boolean): void {
	if (text === "") return;
	const last = result[result.length - 1];
	if (last && last.className === className && last.changed === changed) last.text += text;
	else result.push({ text, className, changed });
}

// Composes highlight.js's per-line tokens with the existing intra-line word-diff
// segments into what a diff row actually renders. Degenerates exactly to today's
// output when an input is missing: no tokens -> segments-only (color-free, same as
// the old DiffLineSegments); no segments -> colored, unhighlighted line; neither ->
// today's plain-text fallback.
export function composeLineRuns(
	tokens: LineToken[] | undefined,
	segments: DiffSegment[] | undefined,
	text: string,
): DiffRun[] {
	if (text === "") return [{ text: " ", changed: false }];
	const tokenRuns: TextRun[] = tokens && tokens.length > 0 ? tokens : [{ text, className: undefined }];
	const segRuns: DiffSegment[] = segments && segments.length > 0 ? segments : [{ text, changed: false }];
	return mergeRuns(tokenRuns, segRuns);
}
