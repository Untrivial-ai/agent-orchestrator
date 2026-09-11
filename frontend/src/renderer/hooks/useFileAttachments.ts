import { useCallback, useRef, useState } from "react";

// Client-side mirror of the backend spawn caps in
// backend/internal/httpd/controllers/sessions.go (maxAttachments /
// maxAttachmentBytes / maxAttachmentsBytes). Enforced here too so the user gets
// inline feedback at paste/drop time instead of a late rejection after submit.
export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024;
export const MAX_ATTACHMENTS_BYTES = 25 * 1024 * 1024;

const mb = (bytes: number) => Math.round(bytes / (1024 * 1024));

/** A single file staged for a task/orchestrator brief. */
export type FileAttachment = {
	/** Stable id for list keys and removal. */
	id: string;
	/** Browser-reported MIME type (e.g. "image/png", "text/plain"). */
	mimeType: string;
	/** Decoded byte size (from File.size), used to enforce the total-size cap. */
	bytes: number;
	/** File name for display. */
	name: string;
	/** data: URL used to render the thumbnail preview (for images only). */
	dataUrl?: string;
	/** Base64 payload without the "data:...;base64," prefix, for upload. */
	data: string;
};

/** Attachment payload shape accepted by the spawn API. */
export type FileAttachmentPayload = {
	mimeType: string;
	data: string;
	name?: string;
};

// Client-side mirror of the backend image-preview allowlist. Non-image files can
// still be attached; they render with the generic file icon.
const SUPPORTED_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/bmp"]);

export const isSupportedImageAttachment = (type: string) =>
	SUPPORTED_IMAGE_TYPES.has(type.toLowerCase().trim());

const readFileAsBase64 = (file: File): Promise<{ dataUrl: string; data: string }> =>
	new Promise((resolve, reject) => {
		const reader = new FileReader();
		reader.onerror = () => reject(reader.error ?? new Error("Failed to read file"));
		reader.onload = () => {
			const dataUrl = typeof reader.result === "string" ? reader.result : "";
			const comma = dataUrl.indexOf(",");
			if (!dataUrl || comma < 0) {
				reject(new Error("Unreadable file data"));
				return;
			}
			resolve({
				dataUrl,
				data: dataUrl.slice(comma + 1),
			});
		};
		reader.readAsDataURL(file);
	});

/**
 * useFileAttachments stages files pasted, dropped, or picked into a brief and
 * exposes them as upload-ready payloads. Count and size caps (mirroring the backend)
 * are enforced here, and rejections / unreadable files surface through `error` for
 * inline feedback.
 *
 * Supports all file types except SVG (blocked for security). Images show thumbnails,
 * other files show a generic file icon.
 */
export function useFileAttachments() {
	const [attachments, setAttachments] = useState<FileAttachment[]>([]);
	const [error, setError] = useState<string | null>(null);
	const attachmentsRef = useRef<FileAttachment[]>([]);
	const pendingReadsRef = useRef<Set<Promise<unknown>>>(new Set());
	const generationRef = useRef(0);

	const addFiles = useCallback(async (files: Iterable<File>) => {
		const generation = generationRef.current;
		// Filter out directories - they have type "" and size 0 in most browsers
		const validFiles = Array.from(files).filter((file) => {
			// Exclude directories (they typically have no type and size 0)
			// Also exclude items that might be folders based on common patterns
			if (file.type === "" && file.name.endsWith("/")) return false;
			// Some browsers report directories as size 0 with empty type
			if (file.type === "" && file.size === 0) return false;
			return true;
		});

		if (validFiles.length === 0) return;

		const errors = new Set<string>();
		// Block SVG files for security (active content)
		const blockedFiles = validFiles.filter(
			(file) => file.type.toLowerCase().trim() === "image/svg+xml",
		);
		if (blockedFiles.length > 0) {
			errors.add("SVG files are not supported for security reasons.");
		}
		const valid = validFiles.filter(
			(file) => file.type.toLowerCase().trim() !== "image/svg+xml",
		);

		// Reject oversized files before the (async) read.
		const readable = valid.filter((file) => {
			if (file.size > MAX_ATTACHMENT_BYTES) {
				errors.add(`Each file must be under ${mb(MAX_ATTACHMENT_BYTES)} MB.`);
				return false;
			}
			return true;
		});

		const pendingReads = Promise.all(
			readable.map((file) =>
				readFileAsBase64(file).catch(() => null).then((result) => ({ file, result })),
			),
		);
		pendingReadsRef.current.add(pendingReads);
		const results = await pendingReads;
		pendingReadsRef.current.delete(pendingReads);
		if (generation !== generationRef.current) return;

		const fresh: FileAttachment[] = [];
		for (const { file, result } of results) {
			if (!result) {
				errors.add(`Some files couldn't be read and were skipped.`);
				continue;
			}
			const isImage = file.type.startsWith("image/") && isSupportedImageAttachment(file.type);
			fresh.push({
				id:
					typeof crypto !== "undefined" && "randomUUID" in crypto
						? crypto.randomUUID()
						: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
				mimeType: file.type || "application/octet-stream",
				bytes: file.size,
				name: file.name,
				dataUrl: isImage ? result.dataUrl : undefined,
				data: result.data,
			});
		}

		const accepted = [...attachmentsRef.current];
		let total = accepted.reduce((sum, a) => sum + a.bytes, 0);
		for (const a of fresh) {
			if (accepted.length >= MAX_ATTACHMENTS) {
				errors.add(`You can attach up to ${MAX_ATTACHMENTS} files.`);
				break;
			}
			if (total + a.bytes > MAX_ATTACHMENTS_BYTES) {
				// Only this file is refused: the remaining budget cannot absorb it,
				// but a later file in the same batch still can. Aborting here (break)
				// would silently drop every smaller file staged after it.
				errors.add(`Attachments must total under ${mb(MAX_ATTACHMENTS_BYTES)} MB.`);
				continue;
			}
			accepted.push(a);
			total += a.bytes;
		}
		if (accepted.length !== attachmentsRef.current.length) {
			attachmentsRef.current = accepted;
			setAttachments(accepted);
		}
		setError(errors.size > 0 ? Array.from(errors).join(" ") : null);
	}, []);

	const remove = useCallback((id: string) => {
		const next = attachmentsRef.current.filter((a) => a.id !== id);
		attachmentsRef.current = next;
		setAttachments(next);
		setError(null);
	}, []);

	const clear = useCallback(() => {
		generationRef.current++;
		pendingReadsRef.current.clear();
		attachmentsRef.current = [];
		setAttachments([]);
		setError(null);
	}, []);

	const toSettledPayload = useCallback(async (): Promise<FileAttachmentPayload[]> => {
		while (pendingReadsRef.current.size > 0) {
			await Promise.allSettled(Array.from(pendingReadsRef.current));
		}
		return attachmentsRef.current.map(({ mimeType, data }) => ({ mimeType, data }));
	}, []);
	// Read from the same ref as toSettledPayload so a submit resumed after FileReader
	// completion can cache staged paths without waiting for another React render.
	const attachmentSignature = useCallback(
		() => attachmentsRef.current.map((attachment) => attachment.id).join(":"),
		[],
	);
	const hasPendingReads = useCallback(() => pendingReadsRef.current.size > 0, []);

	return {
		attachments,
		error,
		addFiles,
		remove,
		clear,
		toSettledPayload,
		attachmentSignature,
		hasPendingReads,
	};
}
