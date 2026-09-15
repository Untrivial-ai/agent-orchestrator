import { describe, expect, it } from "vitest";
import { attachmentFilePath, isImageAttachment, isSameAttachmentLoad, stagedAttachmentParts, withAttachmentReferences } from "./messageAttachments";

describe("mobile Chat staged attachments", () => {
	it("strips the desktop composer suffix so the image can render instead of the raw path list", () => {
		const text = "Look at this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-a1b2c3.png\n- .ao/attachments/attachment-d4e5f6.pdf";
		expect(stagedAttachmentParts(text)).toEqual({
			body: "Look at this",
			attachments: [".ao/attachments/attachment-a1b2c3.png", ".ao/attachments/attachment-d4e5f6.pdf"],
		});
	});

	it("accepts every AO-shipped wording, including mobile's own and legacy image-only prompts", () => {
		for (const header of [
			"Attached files (read these files in the workspace for context):",
			"Attached images (read these files in the workspace for visual context):",
			"Attached files are available in the worktree:",
		]) {
			expect(stagedAttachmentParts(`Hi\n\n${header}\n- .ao/attachments/attachment-1.png`)).toEqual({
				body: "Hi",
				attachments: [".ao/attachments/attachment-1.png"],
			});
		}
	});

	it("handles an attachment-only message", () => {
		expect(stagedAttachmentParts("Attached files (read these files in the workspace):\n- .ao/attachments/image-xyz.jpeg")).toEqual({
			body: "",
			attachments: [".ao/attachments/image-xyz.jpeg"],
		});
	});

	it("leaves prose that merely quotes the wording about non-staged files untouched", () => {
		const text = "Docs example\n\nAttached files (read these files in the workspace):\n- docs/screenshot.png";
		expect(stagedAttachmentParts(text)).toEqual({ body: text, attachments: [] });
		expect(stagedAttachmentParts("plain message")).toEqual({ body: "plain message", attachments: [] });
	});

	it("round-trips the suffix mobile sends, using the wording desktop can also render", () => {
		const text = withAttachmentReferences("  Fix this  ", [".ao/attachments/attachment-9.png"]);
		expect(text).toBe("Fix this\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-9.png");
		expect(stagedAttachmentParts(text)).toEqual({ body: "Fix this", attachments: [".ao/attachments/attachment-9.png"] });
		expect(withAttachmentReferences("unchanged", [])).toBe("unchanged");
	});

	it("treats a failed image load as stale once the URL or credential changes", () => {
		const failed = { uri: "http://h:3011/api/v1/sessions/s/preview/files/a.png", headers: { Authorization: "Bearer old" } };
		expect(isSameAttachmentLoad(failed, { uri: failed.uri, headers: { Authorization: "Bearer old" } })).toBe(true);
		expect(isSameAttachmentLoad(failed, { uri: failed.uri, headers: { Authorization: "Bearer rotated" } })).toBe(false);
		expect(isSameAttachmentLoad(failed, { uri: "http://other:3011/api/v1/sessions/s/preview/files/a.png", headers: failed.headers })).toBe(false);
		expect(isSameAttachmentLoad(undefined, failed)).toBe(false);
	});

	it("builds the escaped preview-files route and recognises image paths", () => {
		expect(attachmentFilePath("sess 1", ".ao/attachments/attachment-a.png")).toBe(
			"/api/v1/sessions/sess%201/preview/files/.ao/attachments/attachment-a.png",
		);
		expect(isImageAttachment(".ao/attachments/attachment-a.JPG")).toBe(true);
		expect(isImageAttachment(".ao/attachments/attachment-a.webp")).toBe(true);
		expect(isImageAttachment(".ao/attachments/attachment-a.pdf")).toBe(false);
	});
});
