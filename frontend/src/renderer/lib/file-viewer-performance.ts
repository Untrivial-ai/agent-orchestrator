export type FileViewerPerformanceMark =
	| "files-click"
	| "files-shell-painted"
	| "manifest-ready"
	| "tree-painted"
	| "file-selected"
	| "file-header-painted"
	| "file-content-painted";

export type FileViewerPerformanceEntry = Readonly<{ name: FileViewerPerformanceMark; timestamp: number }>;

const MAX_ENTRIES = 128;
let enabled = false;
let entries: FileViewerPerformanceEntry[] = [];

// Development diagnostics are opt-in and memory-only. Entries intentionally
// contain no session IDs, repository names, or file paths.
export function setFileViewerPerformanceEnabled(next: boolean): void {
	enabled = next;
	if (!next) entries = [];
}

export function markFileViewerPerformance(name: FileViewerPerformanceMark, now = performance.now()): void {
	if (!enabled) return;
	entries.push(Object.freeze({ name, timestamp: now }));
	if (entries.length > MAX_ENTRIES) entries = entries.slice(entries.length - MAX_ENTRIES);
}

export function getFileViewerPerformanceEntries(): readonly FileViewerPerformanceEntry[] {
	return entries.slice();
}
