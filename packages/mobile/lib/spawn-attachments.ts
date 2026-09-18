export type SpawnAttachment = {
	name: string;
	mimeType: string;
	data: string;
	bytes: number;
};

const MAX_ATTACHMENTS = 8;
const MAX_FILE_BYTES = 10 * 1024 * 1024;
const MAX_TOTAL_BYTES = 25 * 1024 * 1024;

export function appendSpawnAttachments(
	existing: readonly SpawnAttachment[],
	incoming: readonly SpawnAttachment[],
): { attachments: SpawnAttachment[]; error?: string } {
	const attachments = [...existing];
	let totalBytes = attachments.reduce((sum, item) => sum + item.bytes, 0);
	let error: string | undefined;

	for (const item of incoming) {
		if (attachments.length >= MAX_ATTACHMENTS) {
			error ??= `You can attach up to ${MAX_ATTACHMENTS} files.`;
			break;
		}
		if (item.mimeType.trim().toLowerCase() === "image/svg+xml") {
			error ??= `${item.name} is not a supported attachment type.`;
			continue;
		}
		if (item.bytes > MAX_FILE_BYTES) {
			error ??= `${item.name} must be under 10 MB.`;
			continue;
		}
		if (totalBytes + item.bytes > MAX_TOTAL_BYTES) {
			error ??= "Attachments must total under 25 MB.";
			continue;
		}
		attachments.push(item);
		totalBytes += item.bytes;
	}

	return { attachments, error };
}
