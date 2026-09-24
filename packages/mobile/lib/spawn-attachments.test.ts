import { describe, expect, it, vi } from "vitest";
import { appendSpawnAttachments, readSpawnAttachments } from "./spawn-attachments";

describe("appendSpawnAttachments", () => {
	it("keeps accepted files and reports files that exceed the per-file limit", () => {
		const result = appendSpawnAttachments(
			[{ name: "brief.txt", mimeType: "text/plain", data: "YQ==", bytes: 1 }],
			[
				{ name: "notes.md", mimeType: "text/markdown", data: "Yg==", bytes: 1 },
				{ name: "archive.zip", mimeType: "application/zip", data: "Yw==", bytes: 50 * 1024 * 1024 + 1 },
			],
		);

		expect(result.attachments.map((item) => item.name)).toEqual(["brief.txt", "notes.md"]);
		expect(result.error).toBe("archive.zip must be under 50 MB.");
	});

	it("rejects SVG files before they reach the daemon", () => {
		const result = appendSpawnAttachments([], [
			{ name: "diagram.svg", mimeType: "image/svg+xml", data: "PHN2Zy8+", bytes: 6 },
		]);

		expect(result.attachments).toEqual([]);
		expect(result.error).toBe("diagram.svg is not a supported attachment type.");
	});
});

describe("readSpawnAttachments", () => {
	it("retains encoded data for a file reported at 11 MiB", async () => {
		const readData = vi.fn(async () => "YQ==");
		const result = await readSpawnAttachments([], [
			{ name: "video.mov", mimeType: "video/quicktime", bytes: 11 * 1024 * 1024, readData },
		]);

		expect(readData).toHaveBeenCalledOnce();
		expect(result.error).toBeUndefined();
		expect(result.attachments.map(({ mimeType, data }) => ({ mimeType, data }))).toEqual([
			{ mimeType: "video/quicktime", data: "YQ==" },
		]);
	});

	it("rejects files before reading when size, total, or count is invalid", async () => {
		const readData = vi.fn(async () => "YQ==");
		const existing = [
			{ name: "first.mov", mimeType: "video/quicktime", data: "YQ==", bytes: 50 * 1024 * 1024 },
			{ name: "second.mov", mimeType: "video/quicktime", data: "YQ==", bytes: 50 * 1024 * 1024 },
		];
		const result = await readSpawnAttachments(existing, [
			{ name: "empty.mov", mimeType: "video/quicktime", bytes: 0, readData },
			{ name: "unknown.mov", mimeType: "video/quicktime", readData },
			{ name: "huge.mov", mimeType: "video/quicktime", bytes: 50 * 1024 * 1024 + 1, readData },
			{ name: "extra.mov", mimeType: "video/quicktime", bytes: 1, readData },
		]);

		expect(readData).not.toHaveBeenCalled();
		expect(result.attachments).toEqual(existing);
		expect(result.error).toBe("empty.mov is empty.");

		const full = await readSpawnAttachments(Array(8).fill(existing[0]), [
			{ name: "ninth.mov", mimeType: "video/quicktime", bytes: 1, readData },
		]);
		expect(full.error).toBe("You can attach up to 8 files.");
		expect(readData).not.toHaveBeenCalled();

		const emptyRead = await readSpawnAttachments([], [
			{ name: "missing.mov", mimeType: "video/quicktime", bytes: 1, readData: async () => "" },
		]);
		expect(emptyRead.attachments).toEqual([]);
		expect(emptyRead.error).toBe("missing.mov is empty.");
	});
});
