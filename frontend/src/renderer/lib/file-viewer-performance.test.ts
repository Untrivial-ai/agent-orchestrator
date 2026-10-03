import { afterEach, describe, expect, it } from "vitest";
import {
	getFileViewerPerformanceEntries,
	markFileViewerPerformance,
	setFileViewerPerformanceEnabled,
} from "./file-viewer-performance";

describe("file viewer performance recorder", () => {
	afterEach(() => setFileViewerPerformanceEnabled(false));

	it("is disabled by default and clears diagnostics when disabled", () => {
		markFileViewerPerformance("files-click", 1);
		expect(getFileViewerPerformanceEntries()).toEqual([]);
		setFileViewerPerformanceEnabled(true);
		markFileViewerPerformance("files-click", 2);
		expect(getFileViewerPerformanceEntries()).toEqual([{ name: "files-click", timestamp: 2 }]);
		setFileViewerPerformanceEnabled(false);
		expect(getFileViewerPerformanceEntries()).toEqual([]);
	});

	it("keeps a bounded path-free history", () => {
		setFileViewerPerformanceEnabled(true);
		for (let index = 0; index < 140; index++) markFileViewerPerformance("tree-painted", index);
		const entries = getFileViewerPerformanceEntries();
		expect(entries).toHaveLength(128);
		expect(entries[0]).toEqual({ name: "tree-painted", timestamp: 12 });
		expect(JSON.stringify(entries)).not.toContain("path");
	});
});
