import { describe, expect, it } from "vitest";
import { appendSpawnAttachments } from "./spawn-attachments";

describe("appendSpawnAttachments", () => {
	it("keeps accepted files and reports files that exceed the per-file limit", () => {
		const result = appendSpawnAttachments(
			[{ name: "brief.txt", mimeType: "text/plain", data: "YQ==", bytes: 1 }],
			[
				{ name: "notes.md", mimeType: "text/markdown", data: "Yg==", bytes: 1 },
				{ name: "archive.zip", mimeType: "application/zip", data: "Yw==", bytes: 10 * 1024 * 1024 + 1 },
			],
		);

		expect(result.attachments.map((item) => item.name)).toEqual(["brief.txt", "notes.md"]);
		expect(result.error).toBe("archive.zip must be under 10 MB.");
	});

	it("rejects SVG files before they reach the daemon", () => {
		const result = appendSpawnAttachments([], [
			{ name: "diagram.svg", mimeType: "image/svg+xml", data: "PHN2Zy8+", bytes: 6 },
		]);

		expect(result.attachments).toEqual([]);
		expect(result.error).toBe("diagram.svg is not a supported attachment type.");
	});
});
