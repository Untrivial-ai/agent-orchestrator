// These are AO-owned prompt suffixes, not general markdown. Chat and spawn used
// slightly different wording, and older conversations used "Attached images";
// accepting every shipped form lets the transcript improve without rewriting
// its durable history.
const ATTACHMENT_REFERENCE_BLOCK =
	/(?:^|\n\n)(?:Attached files \(read these files in the workspace(?: for context)?\)|Attached images \(read these files in the workspace for visual context\)):\n((?:- [^\n]+(?:\n|$))+)$/;
const STAGED_ATTACHMENT_PATH = /^\.ao\/attachments\/(?:attachment|image)-[A-Za-z0-9][A-Za-z0-9._-]*$/;
export const IMAGE_ATTACHMENT_PATH = /\.(?:png|jpe?g|gif|webp|bmp)$/i;

export function stagedAttachmentParts(text: string): { body: string; attachments: string[] } {
	const match = ATTACHMENT_REFERENCE_BLOCK.exec(text);
	if (!match?.[1]) return { body: text, attachments: [] };

	const attachments = match[1]
		.trimEnd()
		.split("\n")
		.map((line) => line.slice(2));
	// Only reinterpret paths AO itself stages. A user can write an identically
	// worded example about docs/screenshot.png; that prose must remain untouched.
	if (attachments.length === 0 || attachments.some((path) => !STAGED_ATTACHMENT_PATH.test(path))) {
		return { body: text, attachments: [] };
	}
	// The match begins at the generated separator, so slicing at its index
	// removes only AO-owned text and preserves the authored body byte-for-byte.
	return { body: text.slice(0, match.index), attachments };
}

export function attachmentName(path: string): string {
	return path.slice(path.lastIndexOf("/") + 1);
}

export function attachmentURL(apiBaseUrl: string, sessionId: string, path: string): string {
	const route = `/api/v1/sessions/${encodeURIComponent(sessionId)}/preview/files/${path
		.split("/")
		.map(encodeURIComponent)
		.join("/")}`;
	return apiBaseUrl ? new URL(route, apiBaseUrl).toString() : route;
}
