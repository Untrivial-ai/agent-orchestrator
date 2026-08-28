import { renderHook, waitFor } from "@testing-library/react";
import { act } from "react";
import { describe, expect, it, vi } from "vitest";

import {
	capturePendingFileAttachmentsForSession,
	discardCapturedPendingFileAttachments,
	discardPendingFileAttachments,
	discardPendingFileAttachmentsForSession,
	MAX_ATTACHMENTS,
	MAX_ATTACHMENT_BYTES,
	MAX_ATTACHMENTS_BYTES,
	purgeFileAttachmentsForSession,
	useFileAttachments,
	type FileAttachment,
} from "./useFileAttachments";
import { chatDraftScopeKey } from "../lib/chat-drafts";

const file = (name: string, bytes = 8, type = "text/plain") =>
	new File([new Uint8Array(bytes).fill(1)], name, { type });

const mb = 1024 * 1024;

describe("useFileAttachments", () => {
	it("stages a supported file", async () => {
		const { result } = renderHook(() => useFileAttachments());
		await act(async () => {
			await result.current.addFiles([file("notes.txt")]);
		});
		expect(result.current.attachments).toHaveLength(1);
		expect(result.current.attachments[0]?.mimeType).toBe("text/plain");
		expect(result.current.attachments[0]?.name).toBe("notes.txt");
		expect(result.current.error).toBeNull();
	});

	it("keeps the whole read-and-stage window pending and serializes concurrent batches", async () => {
		const releases: Array<() => void> = [];
		let activePreparations = 0;
		let maxActivePreparations = 0;
		const prepareAttachments = vi.fn(
			(attachments: FileAttachment[]) =>
				new Promise<FileAttachment[]>((resolve) => {
					activePreparations += 1;
					maxActivePreparations = Math.max(maxActivePreparations, activePreparations);
					releases.push(() => {
						activePreparations -= 1;
						resolve(
							attachments.map((attachment) => ({
								...attachment,
								stagedPath: `.ao/attachments/${attachment.name}`,
							})),
						);
					});
				}),
		);
		const { result } = renderHook(() => useFileAttachments({ prepareAttachments }));
		let first!: Promise<void>;
		let second!: Promise<void>;
		act(() => {
			first = result.current.addFiles([file("first.txt")]);
			second = result.current.addFiles([file("second.txt")]);
		});

		expect(result.current.preparing).toBe(true);
		expect(result.current.attachments).toHaveLength(0);
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(1));
		await act(async () => releases.shift()?.());
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(2));
		expect(result.current.preparing).toBe(true);
		await act(async () => {
			releases.shift()?.();
			await Promise.all([first, second]);
		});

		expect(maxActivePreparations).toBe(1);
		expect(result.current.preparing).toBe(false);
		expect(result.current.attachments.map((attachment) => attachment.name)).toEqual([
			"first.txt",
			"second.txt",
		]);
	});

	it("waits for every rapidly queued batch before returning the settled payload", async () => {
		const releases: Array<() => void> = [];
		const prepareAttachments = vi.fn(
			(attachments: FileAttachment[]) =>
				new Promise<FileAttachment[]>((resolve) => {
					releases.push(() => resolve(attachments));
				}),
		);
		const { result } = renderHook(() => useFileAttachments({ prepareAttachments }));
		let first!: Promise<void>;
		let second!: Promise<void>;
		let settled: Awaited<ReturnType<typeof result.current.toSettledPayload>> | undefined;
		act(() => {
			first = result.current.addFiles([file("first.txt")]);
			second = result.current.addFiles([file("second.txt")]);
			void result.current.toSettledPayload().then((payload) => {
				settled = payload;
			});
		});

		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(1));
		await act(async () => releases.shift()?.());
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(2));
		expect(settled).toBeUndefined();

		await act(async () => {
			releases.shift()?.();
			await Promise.all([first, second]);
		});
		await waitFor(() => expect(settled).toHaveLength(2));
		expect(settled?.every((attachment) => attachment.mimeType === "text/plain")).toBe(true);
	});

	it("does not resurrect pending attachments after their session is explicitly discarded", async () => {
		const sessionId = "discard-pending-attachments";
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const prepareAttachments = vi.fn(
			() =>
				new Promise<FileAttachment[]>((resolve) => {
					finishStaging = resolve;
				}),
		);
		const first = renderHook(() =>
			useFileAttachments({ initialKey: sessionId, prepareAttachments }),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = first.result.current.addFiles([file("discard-me.txt")]);
		});
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(1));

		act(() => discardPendingFileAttachments(sessionId));
		await act(async () => {
			finishStaging([
				{
					id: "discarded",
					mimeType: "text/plain",
					bytes: 8,
					name: "discard-me.txt",
					data: "AQ==",
					stagedPath: ".ao/attachments/discard-me.txt",
				},
			]);
			await pending;
		});
		expect(first.result.current.attachments).toEqual([]);
		expect(first.result.current.preparing).toBe(false);
		first.unmount();

		const replacement = renderHook(() => useFileAttachments({ initialKey: sessionId }));
		expect(replacement.result.current.attachments).toEqual([]);
		expect(replacement.result.current.preparing).toBe(false);
	});

	it("ignores a discarded completion that resolves after a replacement attachment", async () => {
		const sessionId = "discard-out-of-order-attachments";
		const firstKey = chatDraftScopeKey({
			sessionId,
			incarnation: "2026-08-25T09:00:00.000Z",
		});
		const replacementKey = chatDraftScopeKey({
			sessionId,
			incarnation: "2026-08-26T09:00:00.000Z",
		});
		let finishOld!: (attachments: FileAttachment[]) => void;
		const first = renderHook(() =>
			useFileAttachments({
				initialKey: firstKey,
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishOld = resolve;
					}),
			}),
		);
		let oldPending!: Promise<void>;
		act(() => {
			oldPending = first.result.current.addFiles([file("old.txt")]);
		});
		await waitFor(() => expect(first.result.current.preparing).toBe(true));
		await waitFor(() => expect(finishOld).toBeTypeOf("function"));
		act(() => purgeFileAttachmentsForSession(sessionId));
		first.unmount();

		const replacement = renderHook(() =>
			useFileAttachments({
				initialKey: replacementKey,
				prepareAttachments: async (attachments) =>
					attachments.map((attachment) => ({
						...attachment,
						stagedPath: `.ao/attachments/${attachment.name}`,
					})),
			}),
		);
		await act(async () => {
			await replacement.result.current.addFiles([file("new.txt")]);
		});
		expect(replacement.result.current.attachments.map((attachment) => attachment.name)).toEqual([
			"new.txt",
		]);

		await act(async () => {
			finishOld([
				{
					id: "old",
					mimeType: "text/plain",
					bytes: 8,
					name: "old.txt",
					stagedPath: ".ao/attachments/old.txt",
				},
			]);
			await oldPending;
		});
		expect(replacement.result.current.attachments.map((attachment) => attachment.name)).toEqual([
			"new.txt",
		]);
	});

	it("cancels pending work for every incarnation of one logical session", async () => {
		const sessionId = "discard-all-session-incarnations";
		const releases = new Map<string, (attachments: FileAttachment[]) => void>();
		const hook = (key: string, name: string) =>
			renderHook(() =>
				useFileAttachments({
					initialKey: key,
					prepareAttachments: () =>
						new Promise<FileAttachment[]>((resolve) => releases.set(name, resolve)),
				}),
			);
		const first = hook(
			chatDraftScopeKey({ sessionId, incarnation: "2026-08-25T09:00:00.000Z" }),
			"first",
		);
		const replacement = hook(
			chatDraftScopeKey({ sessionId, incarnation: "2026-08-26T09:00:00.000Z" }),
			"replacement",
		);
		const other = hook(
			chatDraftScopeKey({ sessionId: "other-session", incarnation: "2026-08-26T09:00:00.000Z" }),
			"other",
		);
		let firstPending!: Promise<void>;
		let replacementPending!: Promise<void>;
		let otherPending!: Promise<void>;
		act(() => {
			firstPending = first.result.current.addFiles([file("first.txt")]);
			replacementPending = replacement.result.current.addFiles([file("replacement.txt")]);
			otherPending = other.result.current.addFiles([file("other.txt")]);
		});
		await waitFor(() => expect(releases.size).toBe(3));

		act(() => discardPendingFileAttachmentsForSession(sessionId));
		await act(async () => {
			releases.get("first")?.([{ id: "first", mimeType: "text/plain", bytes: 8, name: "first.txt", stagedPath: ".ao/attachments/first.txt" }]);
			releases.get("replacement")?.([{ id: "replacement", mimeType: "text/plain", bytes: 8, name: "replacement.txt", stagedPath: ".ao/attachments/replacement.txt" }]);
			releases.get("other")?.([{ id: "other", mimeType: "text/plain", bytes: 8, name: "other.txt", stagedPath: ".ao/attachments/other.txt" }]);
			await Promise.all([firstPending, replacementPending, otherPending]);
		});

		expect(first.result.current.attachments).toEqual([]);
		expect(replacement.result.current.attachments).toEqual([]);
		expect(other.result.current.attachments.map((attachment) => attachment.name)).toEqual([
			"other.txt",
		]);
	});

	it("discards only work captured before confirmation and preserves later attachments", async () => {
		const sessionId = "discard-confirmed-attachment-work-only";
		const key = chatDraftScopeKey({
			sessionId,
			incarnation: "2026-08-26T12:00:00.000Z",
		});
		const releases: Array<(attachments: FileAttachment[]) => void> = [];
		const prepareAttachments = vi.fn(
			() =>
				new Promise<FileAttachment[]>((resolve) => {
					releases.push(resolve);
				}),
		);
		const staging = renderHook(() =>
			useFileAttachments({ initialKey: key, prepareAttachments }),
		);
		let beforeConfirmation!: Promise<void>;
		act(() => {
			beforeConfirmation = staging.result.current.addFiles([file("before-confirmation.txt")]);
		});
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(1));

		const confirmedWork = capturePendingFileAttachmentsForSession(sessionId);
		let afterConfirmation!: Promise<void>;
		act(() => {
			afterConfirmation = staging.result.current.addFiles([file("after-confirmation.txt")]);
			discardCapturedPendingFileAttachments(confirmedWork);
		});
		expect(staging.result.current.preparing).toBe(true);

		await act(async () => {
			releases[0]?.([
				{
					id: "before-confirmation",
					mimeType: "text/plain",
					bytes: 8,
					name: "before-confirmation.txt",
					stagedPath: ".ao/attachments/before-confirmation.txt",
				},
			]);
			await beforeConfirmation;
		});
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(2));
		await act(async () => {
			releases[1]?.([
				{
					id: "after-confirmation",
					mimeType: "text/plain",
					bytes: 8,
					name: "after-confirmation.txt",
					stagedPath: ".ao/attachments/after-confirmation.txt",
				},
			]);
			await afterConfirmation;
		});

		expect(staging.result.current.preparing).toBe(false);
		expect(staging.result.current.attachments.map((attachment) => attachment.name)).toEqual([
			"after-confirmation.txt",
		]);
	});

	it("purges shared descriptors for a recreated logical session", async () => {
		const sessionId = "purge-shared-session-descriptors";
		const key = chatDraftScopeKey({
			sessionId,
			incarnation: "2026-08-25T09:00:00.000Z",
		});
		const first = renderHook(() => useFileAttachments({ initialKey: key }));
		await act(async () => {
			await first.result.current.addFiles([file("old.txt")]);
		});
		expect(first.result.current.attachments).toHaveLength(1);
		first.unmount();

		act(() => purgeFileAttachmentsForSession(sessionId));
		const staleRemount = renderHook(() => useFileAttachments({ initialKey: key }));
		expect(staleRemount.result.current.attachments).toEqual([]);
		expect(staleRemount.result.current.preparing).toBe(false);
	});

	it("rejects unsupported SVG files with inline feedback", async () => {
		const { result } = renderHook(() => useFileAttachments());
		await act(async () => {
			await result.current.addFiles([file("vector.svg", 8, "image/svg+xml")]);
		});
		expect(result.current.attachments).toHaveLength(0);
		expect(result.current.error).toMatch(/svg/i);
	});

	it("rejects a single oversized file before reading it", async () => {
		const { result } = renderHook(() => useFileAttachments());
		await act(async () => {
			await result.current.addFiles([file("huge.bin", MAX_ATTACHMENT_BYTES + 1, "application/octet-stream")]);
		});
		expect(result.current.attachments).toHaveLength(0);
		expect(result.current.error).toMatch(/under/i);
	});

	it("enforces the count cap", async () => {
		const { result } = renderHook(() => useFileAttachments());
		await act(async () => {
			await result.current.addFiles(Array.from({ length: MAX_ATTACHMENTS + 2 }, (_, i) => file(`f-${i}.txt`)));
		});
		expect(result.current.attachments).toHaveLength(MAX_ATTACHMENTS);
		expect(result.current.error).toMatch(/up to/i);
	});

	it("skips a file that exceeds the total cap without dropping later smaller files", async () => {
		// Regression probe for the break-vs-continue cap bug: one file that does not
		// fit into the remaining budget aborted the whole staging loop, silently
		// dropping every smaller file staged after it in the same batch.
		const { result } = renderHook(() => useFileAttachments());
		await act(async () => {
			await result.current.addFiles([
				file("a.txt", 9 * mb),
				file("b.txt", 9 * mb),
				file("c.txt", 9 * mb),
				file("d.txt", 5 * mb),
			]);
		});
		// a + b (18 MB) fit; c would push past MAX_ATTACHMENTS_BYTES and only it is
		// refused; d (23 MB total) still fits and must survive the batch.
		expect(result.current.attachments.map((a) => a.name)).toEqual(["a.txt", "b.txt", "d.txt"]);
		expect(result.current.attachments.reduce((sum, a) => sum + a.bytes, 0)).toBeLessThanOrEqual(
			MAX_ATTACHMENTS_BYTES,
		);
		expect(result.current.error).toMatch(/total under/i);
	});
});
