import { hydratePartialDiff, type FileDiffLoadedFiles, type FileDiffMetadata } from "@pierre/diffs";

/** Unchanged lines requested around each change in review patches (git's `--unified`). */
export const REVIEW_CONTEXT_LINES = 3;

/**
 * A diff parsed from a patch can't tell whether the file continues after its
 * last hunk, so Pierre always ends it with a "More unchanged context may be
 * available" row that loads the full file. git writes up to
 * REVIEW_CONTEXT_LINES unchanged lines after the last change, so fewer than
 * that (or a "No newline at end of file" marker) proves the file ends there
 * and the row could only ever expand to nothing.
 */
export function endsAtLastHunk(fileDiff: FileDiffMetadata): boolean {
	if (!fileDiff.isPartial || (fileDiff.type !== "change" && fileDiff.type !== "rename-changed")) return false;
	const lastHunk = fileDiff.hunks[fileDiff.hunks.length - 1];
	if (!lastHunk) return false;
	if (lastHunk.noEOFCRAdditions || lastHunk.noEOFCRDeletions) return true;
	const tail = lastHunk.hunkContent[lastHunk.hunkContent.length - 1];
	return tail?.type === "change" || (tail?.type === "context" && tail.lines < REVIEW_CONTEXT_LINES);
}

// 32-bit FNV-1a over the given strings (each terminated so ["ab"] ≠ ["a", "b"]).
function hashLines(lines: readonly string[]): number {
	let hash = 0x811c9dc5;
	for (const line of lines) {
		for (let index = 0; index < line.length; index++) hash = Math.imul(hash ^ line.charCodeAt(index), 0x01000193);
		hash = Math.imul(hash ^ 0x0a, 0x01000193);
	}
	return hash >>> 0;
}

const identities = new WeakMap<FileDiffMetadata, string>();

/**
 * Identity of a file's patch content: hunk positions plus a hash of every
 * patch line. Parsing a new workspace version yields new metadata objects, so
 * this is what tells "same diff" apart from "changed diff".
 */
export function patchIdentity(fileDiff: FileDiffMetadata): string {
	const cached = identities.get(fileDiff);
	if (cached) return cached;
	const hunks = fileDiff.hunks.map((hunk) => `${hunk.deletionStart},${hunk.deletionCount},${hunk.additionStart},${hunk.additionCount}`).join(";");
	const lines = hashLines([...fileDiff.deletionLines, "\u0000", ...fileDiff.additionLines]);
	const identity = `${fileDiff.type}|${fileDiff.prevName ?? ""}|${hunks}|${lines.toString(36)}`;
	identities.set(fileDiff, identity);
	return identity;
}

/**
 * A number that changes whenever the diff's content, or its patch-only vs
 * full-contents state, changes. CodeView only re-reads an item when its
 * version changes, so this is what lets updated or hydrated diffs through
 * while unchanged ones (new objects, same content) are left alone.
 */
export function diffContentVersion(fileDiff: FileDiffMetadata): number {
	return hashLines([fileDiff.isPartial ? "patch" : "full", patchIdentity(fileDiff)]);
}

const hydratedCopies = new WeakMap<FileDiffMetadata, { files: FileDiffLoadedFiles; diff: FileDiffMetadata | null }>();

/**
 * A full-content copy of a partial diff (the parsed patch itself is left
 * untouched), or null when the fetched contents don't fit the patch. Memoized
 * per partial diff + contents so re-renders hand CodeView the same object.
 */
export function hydratedCopy(fileDiff: FileDiffMetadata, files: FileDiffLoadedFiles): FileDiffMetadata | null {
	const cached = hydratedCopies.get(fileDiff);
	if (cached?.files === files) return cached.diff;
	let diff: FileDiffMetadata | null = null;
	try {
		if (fileDiff.isPartial) diff = hydratePartialDiff("clone", fileDiff, files);
	} catch {
		diff = null;
	}
	hydratedCopies.set(fileDiff, { files, diff });
	return diff;
}
