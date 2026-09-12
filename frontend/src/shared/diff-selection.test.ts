import { describe, expect, it } from "vitest";
import {
	MAX_DIFF_SELECTION_MESSAGE_LENGTH,
	formatDiffSelectionMessage,
	type DiffSelectionPayload,
} from "./diff-selection";

describe("formatDiffSelectionMessage", () => {
	it("includes opaque revision context when the review surface provides it", () => {
		const message = formatDiffSelectionMessage({
			instruction: "Explain",
			filePath: "src/app.ts",
			lines: [{ kind: "add", oldNo: null, newNo: 4, text: "return true;" }],
			scope: "staged",
			workspaceVersion: "workspace-123",
			fileFingerprint: "file-456",
		});

		expect(message).toContain("Comparison scope: staged");
		expect(message).toContain("Workspace version: workspace-123");
		expect(message).toContain("File fingerprint: file-456");
	});

	it("formats a simple selection of context, add, and del lines with line range", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Explain this change",
			filePath: "src/app.ts",
			lines: [
				{ kind: "context", oldNo: 10, newNo: 10, text: "export function greet(name: string) {" },
				{ kind: "add", oldNo: null, newNo: 11, text: "return `Hello, ${name}!`;" },
				{ kind: "del", oldNo: 12, newNo: null, text: "console.log(name);" },
				{ kind: "context", oldNo: 13, newNo: 12, text: "}" },
			],
		};

		const message = formatDiffSelectionMessage(payload);

		expect(message).toContain("Explain this change");
		expect(message).toContain("src/app.ts");
		expect(message).toContain("lines 10-12");
		expect(message).toContain(" export function greet(name: string) {");
		expect(message).toContain("+ return `Hello, ${name}!`;");
		expect(message).toContain("- console.log(name);");
		expect(message).toContain(" }");
	});

	it("derives line range from new numbers when available", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Make changes",
			filePath: "test.js",
			lines: [
				{ kind: "add", oldNo: null, newNo: 5, text: "new line 1" },
				{ kind: "add", oldNo: null, newNo: 6, text: "new line 2" },
				{ kind: "add", oldNo: null, newNo: 7, text: "new line 3" },
			],
		};

		const message = formatDiffSelectionMessage(payload);

		expect(message).toContain("lines 5-7");
	});

	it("falls back to old numbers for pure-deletion selections with no newNo", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Review these deletions",
			filePath: "src/old.ts",
			lines: [
				{ kind: "del", oldNo: 20, newNo: null, text: "remove this" },
				{ kind: "del", oldNo: 21, newNo: null, text: "and this too" },
				{ kind: "del", oldNo: 22, newNo: null, text: "and this" },
			],
		};

		const message = formatDiffSelectionMessage(payload);

		expect(message).toContain("lines 20-22");
	});

	it("prefers new-line numbering when mixed add/del/context lines are present", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Explain",
			filePath: "mixed.js",
			lines: [
				{ kind: "del", oldNo: 5, newNo: null, text: "old line" },
				{ kind: "add", oldNo: null, newNo: 5, text: "new line" },
				{ kind: "context", oldNo: 6, newNo: 6, text: "context" },
			],
		};

		const message = formatDiffSelectionMessage(payload);

		// Should use new numbers (5-6) since at least one line has a newNo
		expect(message).toContain("lines 5-6");
	});

	it("includes diff markers (space for context, + for add, - for del)", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Review",
			filePath: "file.ts",
			lines: [
				{ kind: "context", oldNo: 1, newNo: 1, text: "unchanged line" },
				{ kind: "add", oldNo: null, newNo: 2, text: "added line" },
				{ kind: "del", oldNo: 3, newNo: null, text: "deleted line" },
			],
		};

		const message = formatDiffSelectionMessage(payload);

		// Check that diff markers are preserved
		expect(message).toContain(" unchanged line");
		expect(message).toContain("+ added line");
		expect(message).toContain("- deleted line");
	});

	it("reflects both canned and user-typed instructions verbatim", () => {
		const cannedPayload: DiffSelectionPayload = {
			instruction: "Explain this change",
			filePath: "file.ts",
			lines: [{ kind: "context", oldNo: 1, newNo: 1, text: "line" }],
		};

		const cannedMessage = formatDiffSelectionMessage(cannedPayload);
		expect(cannedMessage).toContain("Explain this change");

		const userPayload: DiffSelectionPayload = {
			instruction: "This is my custom instruction to fix the bug",
			filePath: "file.ts",
			lines: [{ kind: "context", oldNo: 1, newNo: 1, text: "line" }],
		};

		const userMessage = formatDiffSelectionMessage(userPayload);
		expect(userMessage).toContain("This is my custom instruction to fix the bug");
	});

	it("keeps the total message below the length cap", () => {
		const longText = "x".repeat(500);
		const payload: DiffSelectionPayload = {
			instruction: "Make changes: " + "a".repeat(200),
			filePath: "very/long/file/path/that/is/quite/long.ts",
			lines: Array.from({ length: 100 }, (_, i) => ({
				kind: "add" as const,
				oldNo: null,
				newNo: i + 1,
				text: longText,
			})),
		};

		const message = formatDiffSelectionMessage(payload);

		expect(message.length).toBeLessThanOrEqual(MAX_DIFF_SELECTION_MESSAGE_LENGTH);
		expect(message).toContain("[truncated]");
	});

	it("handles a single line selection", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Explain",
			filePath: "single.ts",
			lines: [{ kind: "add", oldNo: null, newNo: 42, text: "console.log('test');" }],
		};

		const message = formatDiffSelectionMessage(payload);

		expect(message).toContain("lines 42-42");
		expect(message).toContain("+ console.log('test');");
	});

	it("handles empty text lines", () => {
		const payload: DiffSelectionPayload = {
			instruction: "Review",
			filePath: "blank.ts",
			lines: [
				{ kind: "context", oldNo: 1, newNo: 1, text: "function test() {" },
				{ kind: "add", oldNo: null, newNo: 2, text: "" },
				{ kind: "context", oldNo: 2, newNo: 3, text: "}" },
			],
		};

		const message = formatDiffSelectionMessage(payload);

		// Empty line should still have the marker
		expect(message).toContain("+");
	});

	it("preserves instruction and file path for truncated messages", () => {
		const longText = "x".repeat(2000);
		const payload: DiffSelectionPayload = {
			instruction: "Critical instruction",
			filePath: "src/critical.ts",
			lines: Array.from({ length: 50 }, (_, i) => ({
				kind: "add" as const,
				oldNo: null,
				newNo: i + 1,
				text: longText,
			})),
		};

		const message = formatDiffSelectionMessage(payload);

		expect(message).toContain("Critical instruction");
		expect(message).toContain("src/critical.ts");
		expect(message).toContain("[truncated]");
	});
});
