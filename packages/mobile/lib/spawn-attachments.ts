export type SpawnAttachment = {
	name: string;
	mimeType: string;
	data: string;
	bytes: number;
};

export type PickedSpawnAttachment = Omit<SpawnAttachment, "data" | "bytes"> & {
	bytes?: number;
	readData: () => Promise<string>;
};

const MAX_ATTACHMENTS = 8;
const MAX_FILE_BYTES = 50 * 1024 * 1024;
const MAX_TOTAL_BYTES = 100 * 1024 * 1024;

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
			error ??= `${item.name} must be under 50 MB.`;
			continue;
		}
		if (totalBytes + item.bytes > MAX_TOTAL_BYTES) {
			error ??= "Attachments must total under 100 MB.";
			continue;
		}
		attachments.push(item);
		totalBytes += item.bytes;
	}

	return { attachments, error };
}

export async function readSpawnAttachments(
	existing: readonly SpawnAttachment[],
	incoming: readonly PickedSpawnAttachment[],
): Promise<{ attachments: SpawnAttachment[]; error?: string }> {
	let attachments = [...existing];
	let error: string | undefined;

	for (const item of incoming) {
		if (item.bytes === 0) {
			error ??= `${item.name} is empty.`;
			continue;
		}
		if (typeof item.bytes !== "number" || !Number.isSafeInteger(item.bytes) || item.bytes < 0) {
			error ??= `Could not determine the size of ${item.name}.`;
			continue;
		}
		const candidate: SpawnAttachment = { name: item.name, mimeType: item.mimeType, bytes: item.bytes, data: "" };
		const next = appendSpawnAttachments(attachments, [candidate]);
		if (next.error) {
			error ??= next.error;
			continue;
		}
		try {
			candidate.data = await item.readData();
		} catch (cause) {
			error ??= cause instanceof Error ? cause.message : `Could not read ${item.name}.`;
			continue;
		}
		if (!candidate.data.trim()) {
			error ??= `${item.name} is empty.`;
			continue;
		}
		attachments = next.attachments;
	}

	return { attachments, error };
}
