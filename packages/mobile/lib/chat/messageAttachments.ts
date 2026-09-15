const API = "/api/v1";

// AO-owned prompt suffixes, not general markdown. Desktop chat, spawn, older
// image-only prompts and earlier mobile builds each shipped their own wording;
// durable history keeps all of them, so the transcript must accept every form.
// Mirrors frontend/src/renderer/components/chat/messageAttachments.ts.
const ATTACHMENT_REFERENCE_BLOCK =
	/(?:^|\n\n)(?:Attached files \(read these files in the workspace(?: for context)?\)|Attached images \(read these files in the workspace for visual context\)|Attached files are available in the worktree):\n((?:- [^\n]+(?:\n|$))+)$/;
const STAGED_ATTACHMENT_PATH = /^\.ao\/attachments\/(?:attachment|image)-[A-Za-z0-9][A-Za-z0-9._-]*$/;
const IMAGE_ATTACHMENT_PATH = /\.(?:png|jpe?g|gif|webp|bmp)$/i;

export function stagedAttachmentParts(text: string): { body: string; attachments: string[] } {
	const match = ATTACHMENT_REFERENCE_BLOCK.exec(text);
	if (!match?.[1]) return { body: text, attachments: [] };
	const attachments = match[1].trimEnd().split("\n").map((line) => line.slice(2));
	// Only reinterpret paths AO itself stages: prose quoting the same wording about
	// docs/screenshot.png must stay readable text.
	if (attachments.some((path) => !STAGED_ATTACHMENT_PATH.test(path))) return { body: text, attachments: [] };
	return { body: text.slice(0, match.index), attachments };
}

/** Appends staged paths using desktop's wording, so either surface renders them. */
export function withAttachmentReferences(text: string, paths: string[]): string {
	if (paths.length === 0) return text;
	const body = text.trim();
	return `${body}${body ? "\n\n" : ""}Attached files (read these files in the workspace):\n${paths.map((path) => `- ${path}`).join("\n")}`;
}

export type AttachmentImageSource = { uri: string; headers: Record<string, string> };

/**
 * Whether two sources are the same image load. A recorded failure only sticks to
 * the load that failed: a new address (endpoint race) or a rotated password is a
 * fresh attempt. Compared by value so the credential never becomes a React key.
 */
export function isSameAttachmentLoad(a: AttachmentImageSource | undefined, b: AttachmentImageSource): boolean {
	return a !== undefined && a.uri === b.uri && a.headers.Authorization === b.headers.Authorization;
}

/** Image tiles stay compact so a message still reads as a message; a lone image gets more room. */
export function attachmentTileSize(imageCount: number): number {
	return imageCount > 1 ? 104 : 160;
}

export function isImageAttachment(path: string): boolean {
	return IMAGE_ATTACHMENT_PATH.test(path);
}

export function attachmentName(path: string): string {
	return path.slice(path.lastIndexOf("/") + 1);
}

/** The daemon's preview-files route for a workspace path, each segment escaped. */
export function attachmentFilePath(sessionId: string, path: string): string {
	return `${API}/sessions/${encodeURIComponent(sessionId)}/preview/files/${path.split("/").map(encodeURIComponent).join("/")}`;
}
