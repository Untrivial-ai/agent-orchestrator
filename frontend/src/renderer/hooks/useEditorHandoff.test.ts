import { describe, expect, it } from "vitest";
import { editorHandoffErrorMessage } from "./useEditorHandoff";

// Electron wraps ipcMain rejections; the topbar used to render the wrapper
// verbatim, so a missing worktree read as "Error invoking remote method
// 'editorHandoff:open': Error: Session workspace is not available".
describe("editorHandoffErrorMessage", () => {
	it("strips the Electron remote-method wrapper", () => {
		const raw = new Error(
			"Error invoking remote method 'editorHandoff:open': Error: Session workspace is not available",
		);
		expect(editorHandoffErrorMessage(raw)).toBe("Session workspace is not available");
	});

	it("strips the wrapper for other channels and error classes", () => {
		expect(
			editorHandoffErrorMessage(
				new Error("Error invoking remote method 'editorHandoff:getState': TypeError: boom"),
			),
		).toBe("boom");
	});

	it("leaves an already-clean message alone", () => {
		expect(editorHandoffErrorMessage(new Error("That editor is not installed. Choose another option."))).toBe(
			"That editor is not installed. Choose another option.",
		);
	});

	it("keeps the original when stripping would leave nothing", () => {
		const raw = new Error("Error invoking remote method 'editorHandoff:open': Error:");
		expect(editorHandoffErrorMessage(raw)).toBe(raw.message);
	});

	it("returns null for non-Error rejections", () => {
		expect(editorHandoffErrorMessage("nope")).toBeNull();
		expect(editorHandoffErrorMessage(undefined)).toBeNull();
	});
});
