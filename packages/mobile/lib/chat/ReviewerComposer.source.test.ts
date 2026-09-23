import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./ReviewerComposer.tsx", import.meta.url), "utf8");

describe("reviewer attachment composer", () => {
	it("supports photos and bounded embedded text files", () => {
		expect(source).toContain("ImagePicker.launchImageLibraryAsync");
		expect(source).toContain("DocumentPicker.getDocumentAsync");
		expect(source).toContain("MAX_FILE_BYTES = 500_000");
		expect(source).toContain("<ChatAttachmentMenu");
	});

	it("only sends or interrupts and has no ordinary session actions", () => {
		expect(source).toContain("await onSend(");
		expect(source).toContain("onInterrupt()");
		for (const unsupported of ["onSteer", "onPromoteQueuedTurn", "onRollback", "onSettings", "onConfigOption"]) {
			expect(source).not.toContain(unsupported);
		}
	});
});
