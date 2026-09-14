import { renderHook, waitFor } from "@testing-library/react";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

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
import {
	chatDraftScopeKey,
	readChatSessionDraft,
	writeChatAttachments,
} from "../lib/chat-drafts";

const file = (name: string, bytes = 8, type = "text/plain") =>
	new File([new Uint8Array(bytes).fill(1)], name, { type });

const mb = 1024 * 1024;

afterEach(() => {
	vi.unstubAllGlobals();
});

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

	it("restores a durable staged descriptor as ready without inventing native bytes", async () => {
		const restored: FileAttachment = {
			id: "restored",
			mimeType: "image/png",
			bytes: 4,
			name: "original.png",
			status: "ready",
			stagedPath: ".ao/attachments/attachment-restored.png",
		};
		const { result } = renderHook(() =>
			useFileAttachments({ initialAttachments: [restored], initialKey: "restored-session" }),
		);

		expect(result.current.attachments).toEqual([restored]);
		expect(result.current.hasUndurableAttachments).toBe(false);
		await expect(result.current.toSettledPayload()).resolves.toEqual([]);
	});

	it("publishes Reading immediately and waits for read plus durable staging", async () => {
		let finishRead!: () => void;
		class SlowFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(selected: File) {
				finishRead = () => {
					this.result = `data:${selected.type};base64,AQID`;
					this.onload?.();
				};
			}
		}
		vi.stubGlobal("FileReader", SlowFileReader);
		const prepareAttachments = vi.fn(async (attachments: FileAttachment[]) =>
			attachments.map((attachment) => ({
				...attachment,
				stagedPath: `.ao/attachments/${attachment.name}`,
			})),
		);
		const { result } = renderHook(() => useFileAttachments({ prepareAttachments }));
		let adding!: Promise<void>;
		act(() => {
			adding = result.current.addFiles([file("slow.txt")]);
		});

		expect(result.current.attachments).toMatchObject([
			{ name: "slow.txt", status: "reading" },
		]);
		expect(result.current.preparing).toBe(true);
		let settled = false;
		const payloads = result.current.toSettledPayload().then((value) => {
			settled = true;
			return value;
		});
		await act(async () => Promise.resolve());
		expect(settled).toBe(false);
		expect(prepareAttachments).not.toHaveBeenCalled();

		await act(async () => {
			finishRead();
			await adding;
		});
		await expect(payloads).resolves.toEqual([
			{ mimeType: "text/plain", data: "AQID", name: "slow.txt" },
		]);
		expect(result.current.attachments[0]).toMatchObject({
			status: "ready",
			stagedPath: ".ao/attachments/slow.txt",
		});
		expect(result.current.preparing).toBe(false);
	});

	it("releases a source File after durable staging without dropping fresh native bytes", async () => {
		const prepareAttachments = vi.fn(async (attachments: FileAttachment[]) =>
			attachments.map((attachment) => ({
				...attachment,
				stagedPath: `.ao/attachments/${attachment.name}`,
			})),
		);
		const owner = renderHook(() =>
			useFileAttachments({
				initialKey: "release-durable-source",
				prepareAttachments,
			}),
		);
		await act(async () => {
			await owner.result.current.addFiles([file("durable.txt")]);
		});
		const id = owner.result.current.attachments[0]?.id ?? "";
		await expect(owner.result.current.toSettledPayload()).resolves.toEqual([
			{ mimeType: "text/plain", data: "AQEBAQEBAQE=", name: "durable.txt" },
		]);
		expect(prepareAttachments).toHaveBeenCalledTimes(1);

		owner.unmount();
		const restored = renderHook(() =>
			useFileAttachments({
				initialKey: "release-durable-source",
				prepareAttachments,
			}),
		);
		await waitFor(() => expect(restored.result.current.attachments).toHaveLength(1));
		let retried = true;
		await act(async () => {
			retried = await restored.result.current.retry(id);
		});
		expect(retried).toBe(false);
		expect(prepareAttachments).toHaveBeenCalledTimes(1);
		act(() => restored.result.current.clear());
	});

	it("rereads a retained source to recover staging after a warm remount", async () => {
		const prepareAttachments = vi
			.fn<(attachments: FileAttachment[]) => Promise<FileAttachment[]>>()
			.mockRejectedValueOnce(new Error("first staging attempt failed"))
			.mockImplementation(async (attachments) =>
				attachments.map((attachment) => ({
					...attachment,
					stagedPath: `.ao/attachments/${attachment.name}`,
				})),
			);
		const owner = renderHook(() =>
			useFileAttachments({
				initialKey: "recover-staging-after-warm-remount",
				prepareAttachments,
			}),
		);
		await act(async () => {
			await owner.result.current.addFiles([file("recover.txt")]);
		});
		expect(owner.result.current.attachments).toMatchObject([
			{ name: "recover.txt", status: "ready" },
		]);
		expect(owner.result.current.attachments[0]?.stagedPath).toBeUndefined();
		expect(owner.result.current.error).toMatch(/couldn.t be saved/i);
		owner.unmount();

		const restored = renderHook(() =>
			useFileAttachments({
				initialKey: "recover-staging-after-warm-remount",
				prepareAttachments,
			}),
		);
		await waitFor(() =>
			expect(restored.result.current.attachments).toMatchObject([
				{ name: "recover.txt", status: "ready" },
			]),
		);
		expect(restored.result.current.attachments[0]?.data).toBeUndefined();
		expect(restored.result.current.attachments[0]?.stagedPath).toBeUndefined();
		let settled: unknown;
		await act(async () => {
			settled = await restored.result.current.toSettledPayload();
		});
		expect(settled).toEqual([]);
		expect(prepareAttachments).toHaveBeenCalledTimes(2);
		expect(prepareAttachments.mock.calls[1]?.[0]).toMatchObject([
			{ mimeType: "text/plain", data: "AQEBAQEBAQE=", name: "recover.txt" },
		]);
		expect(restored.result.current.attachments[0]).toMatchObject({
			status: "ready",
			stagedPath: ".ao/attachments/recover.txt",
		});
		expect(restored.result.current.attachments[0]?.data).toBeUndefined();
		expect(restored.result.current.error).toBeNull();
		act(() => restored.result.current.clear());
	});

	it("keeps a failed read visible for retry and removal", async () => {
		let failRead!: () => void;
		let finishRetry!: () => void;
		let reads = 0;
		class RetryableFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(selected: File) {
				reads += 1;
				if (reads === 1) {
					failRead = () => {
						this.error = new Error("disk read failed");
						this.onerror?.();
					};
					return;
				}
				finishRetry = () => {
					this.result = `data:${selected.type};base64,UkVUUll`;
					this.onload?.();
				};
			}
		}
		vi.stubGlobal("FileReader", RetryableFileReader);
		const { result } = renderHook(() => useFileAttachments());
		let adding!: Promise<void>;
		act(() => {
			adding = result.current.addFiles([file("retry.txt")]);
		});
		await act(async () => {
			failRead();
			await adding;
		});

		expect(result.current.attachments).toMatchObject([
			{ name: "retry.txt", status: "failed" },
		]);
		expect(result.current.error).toMatch(/retry or remove/i);
		await expect(result.current.toSettledPayload()).rejects.toThrow(/retry or remove/i);

		const id = result.current.attachments[0]?.id ?? "";
		let retrying!: Promise<boolean>;
		act(() => {
			retrying = result.current.retry(id);
		});
		expect(result.current.attachments[0]).toMatchObject({ status: "reading" });
		await act(async () => {
			finishRetry();
			expect(await retrying).toBe(true);
		});
		await expect(result.current.toSettledPayload()).resolves.toEqual([
			{ mimeType: "text/plain", data: "UkVUUll", name: "retry.txt" },
		]);

		act(() => result.current.remove(id));
		expect(result.current.attachments).toEqual([]);
	});

	it("serializes retry staging with its original add and stages each file once", async () => {
		let failFirstRead!: () => void;
		let finishFirstRetry!: () => void;
		let finishSecondRead!: () => void;
		const readAttempts = new Map<string, number>();
		class OverlappingFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(selected: File) {
				const attempt = (readAttempts.get(selected.name) ?? 0) + 1;
				readAttempts.set(selected.name, attempt);
				if (selected.name === "first.txt" && attempt === 1) {
					failFirstRead = () => {
						this.error = new Error("first read failed");
						this.onerror?.();
					};
					return;
				}
				const finish = () => {
					this.result = `data:${selected.type};base64,UkVBRFk=`;
					this.onload?.();
				};
				if (selected.name === "first.txt") finishFirstRetry = finish;
				else finishSecondRead = finish;
			}
		}
		vi.stubGlobal("FileReader", OverlappingFileReader);

		let activePreparations = 0;
		let maxActivePreparations = 0;
		const stagedNames: string[] = [];
		const releases: Array<() => void> = [];
		const prepareAttachments = vi.fn(
			(attachments: FileAttachment[]) =>
				new Promise<FileAttachment[]>((resolve) => {
					activePreparations += 1;
					maxActivePreparations = Math.max(maxActivePreparations, activePreparations);
					stagedNames.push(...attachments.map(({ name }) => name));
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
		const { result } = renderHook(() =>
			useFileAttachments({
				initialKey: "serialize-retry-with-original-add",
				prepareAttachments,
			}),
		);
		let adding!: Promise<void>;
		act(() => {
			adding = result.current.addFiles([file("first.txt"), file("second.txt")]);
		});
		await act(async () => failFirstRead());
		await waitFor(() =>
			expect(result.current.attachments[0]).toMatchObject({ status: "failed" }),
		);

		const firstID = result.current.attachments[0]?.id ?? "";
		let retrying!: Promise<boolean>;
		act(() => {
			retrying = result.current.retry(firstID);
		});
		await act(async () => finishFirstRetry());
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(1));
		await act(async () => finishSecondRead());

		await act(async () => releases.shift()?.());
		await waitFor(() => expect(prepareAttachments).toHaveBeenCalledTimes(2));
		await act(async () => {
			releases.shift()?.();
			await Promise.all([adding, retrying]);
		});

		expect(maxActivePreparations).toBe(1);
		expect(stagedNames.filter((name) => name === "first.txt")).toHaveLength(1);
		expect(stagedNames.filter((name) => name === "second.txt")).toHaveLength(1);
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
		expect(result.current.attachments).toMatchObject([
			{ name: "first.txt", status: "reading" },
		]);
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
					releases.push(() =>
					resolve(
						attachments.map((attachment) => ({
							...attachment,
							stagedPath: `.ao/attachments/${attachment.name}`,
						})),
					),
				);
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

	it("does not resurrect a captured read after its owning surface unmounts", async () => {
		const sessionId = "discard-captured-reader-after-unmount";
		let finishRead!: () => void;
		class SlowFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(selected: File) {
				finishRead = () => {
					this.result = `data:${selected.type};base64,Q0FQVFVS`;
					this.onload?.();
				};
			}
		}
		vi.stubGlobal("FileReader", SlowFileReader);
		const owner = renderHook(() => useFileAttachments({ initialKey: sessionId }));
		let pending!: Promise<void>;
		act(() => {
			pending = owner.result.current.addFiles([file("captured.txt")]);
		});
		expect(owner.result.current.attachments).toMatchObject([
			{ name: "captured.txt", status: "reading" },
		]);
		const captured = capturePendingFileAttachmentsForSession(sessionId);
		owner.unmount();
		act(() => discardCapturedPendingFileAttachments(captured));

		await act(async () => {
			finishRead();
			await pending;
		});
		const replacement = renderHook(() => useFileAttachments({ initialKey: sessionId }));
		expect(replacement.result.current.attachments).toEqual([]);
		expect(replacement.result.current.preparing).toBe(false);
	});

	it("discards captured work that settles undurable before the transition completes", async () => {
		const sessionId = "discard-captured-settled-undurable-attachment";
		let finishRead!: () => void;
		class SlowFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(selected: File) {
				finishRead = () => {
					this.result = `data:${selected.type};base64,VU5EVVJBQkxF`;
					this.onload?.();
				};
			}
		}
		vi.stubGlobal("FileReader", SlowFileReader);
		const prepareAttachments = vi.fn().mockRejectedValue(new Error("disk full"));
		const owner = renderHook(() =>
			useFileAttachments({ initialKey: sessionId, prepareAttachments }),
		);
		let adding!: Promise<void>;
		act(() => {
			adding = owner.result.current.addFiles([file("captured-undurable.txt")]);
		});
		expect(owner.result.current.attachments).toMatchObject([
			{ name: "captured-undurable.txt", status: "reading" },
		]);
		const captured = capturePendingFileAttachmentsForSession(sessionId);

		await act(async () => {
			finishRead();
			await adding;
		});
		expect(prepareAttachments).toHaveBeenCalledTimes(1);
		expect(owner.result.current.attachments).toMatchObject([
			{ name: "captured-undurable.txt", status: "ready" },
		]);
		expect(owner.result.current.attachments[0]?.stagedPath).toBeUndefined();

		act(() => discardCapturedPendingFileAttachments(captured));
		expect(owner.result.current.attachments).toEqual([]);
		owner.unmount();

		const replacement = renderHook(() => useFileAttachments({ initialKey: sessionId }));
		expect(replacement.result.current.attachments).toEqual([]);
		expect(replacement.result.current.preparing).toBe(false);
	});

	it("preserves retry work started after an earlier pending boundary was captured", async () => {
		const sessionId = "preserve-post-confirmation-retry";
		let failFirstRead!: () => void;
		let finishFirstRetry!: () => void;
		let finishSecondRead!: () => void;
		const reads = new Map<string, number>();
		class BoundaryFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(selected: File) {
				const attempt = (reads.get(selected.name) ?? 0) + 1;
				reads.set(selected.name, attempt);
				if (selected.name === "first.txt" && attempt === 1) {
					failFirstRead = () => {
						this.error = new Error("first read failed");
						this.onerror?.();
					};
					return;
				}
				const finish = () => {
					this.result = `data:${selected.type};base64,UkVUUll=`;
					this.onload?.();
				};
				if (selected.name === "first.txt") finishFirstRetry = finish;
				else finishSecondRead = finish;
			}
		}
		vi.stubGlobal("FileReader", BoundaryFileReader);
		const owner = renderHook(() => useFileAttachments({ initialKey: sessionId }));
		let adding!: Promise<void>;
		act(() => {
			adding = owner.result.current.addFiles([file("first.txt"), file("second.txt")]);
		});
		await act(async () => failFirstRead());
		await waitFor(() =>
			expect(owner.result.current.attachments[0]).toMatchObject({ status: "failed" }),
		);

		const captured = capturePendingFileAttachmentsForSession(sessionId);
		const firstId = owner.result.current.attachments[0]?.id ?? "";
		let retrying!: Promise<boolean>;
		act(() => {
			retrying = owner.result.current.retry(firstId);
			discardCapturedPendingFileAttachments(captured);
		});
		expect(owner.result.current.attachments).toMatchObject([
			{ name: "first.txt", status: "reading" },
		]);

		await act(async () => {
			finishFirstRetry();
			expect(await retrying).toBe(true);
			finishSecondRead();
			await adding;
		});
		expect(owner.result.current.attachments).toMatchObject([
			{ name: "first.txt", status: "ready" },
		]);
	});

	it("preserves an undurable retry that settles after capture but before discard", async () => {
		const sessionId = "preserve-settled-post-confirmation-retry";
		const prepareAttachments = vi.fn().mockRejectedValue(new Error("disk full"));
		const owner = renderHook(() =>
			useFileAttachments({ initialKey: sessionId, prepareAttachments }),
		);
		await act(async () => {
			await owner.result.current.addFiles([file("retry-after-confirmation.txt")]);
		});
		const id = owner.result.current.attachments[0]?.id ?? "";
		const captured = capturePendingFileAttachmentsForSession(sessionId);

		let retried = true;
		await act(async () => {
			retried = await owner.result.current.retry(id);
		});
		expect(retried).toBe(false);
		expect(prepareAttachments).toHaveBeenCalledTimes(2);
		act(() => discardCapturedPendingFileAttachments(captured));

		expect(owner.result.current.attachments).toMatchObject([
			{ id, name: "retry-after-confirmation.txt", status: "ready" },
		]);
		expect(owner.result.current.attachments[0]?.stagedPath).toBeUndefined();
		act(() => owner.result.current.clear());
	});

	it("preserves a captured staged descriptor that becomes locally persisted before discard", async () => {
		const sessionId = "preserve-persisted-after-confirmation";
		const prepareAttachments = vi.fn(async (attachments: FileAttachment[]) =>
			attachments.map((attachment) => ({
				...attachment,
				stagedPath: `.ao/attachments/${attachment.name}`,
			})),
		);
		const failPersistence = vi.fn(() => false);
		const owner = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				prepareAttachments,
				onAttachmentsChange: failPersistence,
			}),
		);
		await act(async () => {
			await owner.result.current.addFiles([file("persisted-after-confirmation.txt")]);
		});
		const captured = capturePendingFileAttachmentsForSession(sessionId);

		const persistenceRetry = vi.fn(() => true);
		const replacement = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				onAttachmentsChange: persistenceRetry,
			}),
		);
		await waitFor(() => expect(persistenceRetry).toHaveBeenCalled());
		act(() => discardCapturedPendingFileAttachments(captured));

		expect(owner.result.current.attachments).toMatchObject([
			{
				name: "persisted-after-confirmation.txt",
				stagedPath: ".ao/attachments/persisted-after-confirmation.txt",
			},
		]);
		expect(replacement.result.current.attachments).toHaveLength(1);
		act(() => replacement.result.current.clear());
	});

	it("keeps a failed clear tombstone through confirmation and repairs stale local storage", async () => {
		const sessionId = "repair-confirmed-failed-attachment-clear";
		const persisted = {
			id: "clear-after-storage-failure",
			path: ".ao/attachments/clear-after-storage-failure.txt",
			name: "clear-after-failure.txt",
			mimeType: "text/plain",
			bytes: 8,
		};
		writeChatAttachments(sessionId, [persisted]);
		const durableStorage = window.localStorage;
		let storageAvailable = false;
		const storage = {
			getItem: durableStorage.getItem.bind(durableStorage),
			setItem: durableStorage.setItem.bind(durableStorage),
			removeItem: (key: string) => {
				if (!storageAvailable && key.includes(encodeURIComponent(sessionId))) {
					throw new DOMException("full", "QuotaExceededError");
				}
				durableStorage.removeItem(key);
			},
		} as Storage;
		const persistAttachments = (attachments: FileAttachment[]) =>
			writeChatAttachments(
				sessionId,
				attachments.flatMap((attachment) =>
					attachment.stagedPath
						? [{
								id: attachment.id,
								path: attachment.stagedPath,
								name: attachment.name,
								mimeType: attachment.mimeType,
								bytes: attachment.bytes,
							}]
						: [],
				),
				storage,
			).ok;
		const restored = (): FileAttachment[] =>
			readChatSessionDraft(sessionId, storage).composer.attachments.map((attachment) => ({
				id: attachment.id,
				name: attachment.name,
				mimeType: attachment.mimeType,
				bytes: attachment.bytes,
				status: "ready",
				stagedPath: attachment.path,
			}));
		const owner = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				initialAttachments: restored(),
				onAttachmentsChange: persistAttachments,
			}),
		);
		act(() => owner.result.current.clear());
		expect(owner.result.current.attachments).toEqual([]);
		expect(readChatSessionDraft(sessionId, durableStorage).composer.attachments).toEqual([
			persisted,
		]);
		const confirmed = capturePendingFileAttachmentsForSession(sessionId);
		act(() => discardCapturedPendingFileAttachments(confirmed));
		owner.unmount();

		storageAvailable = true;
		const replacement = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				initialAttachments: restored(),
				onAttachmentsChange: persistAttachments,
			}),
		);
		expect(replacement.result.current.attachments).toEqual([]);
		await waitFor(() =>
			expect(readChatSessionDraft(sessionId, durableStorage).composer.attachments).toEqual([]),
		);
		replacement.unmount();

		const cold = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				initialAttachments: restored(),
				onAttachmentsChange: persistAttachments,
			}),
		);
		expect(cold.result.current.attachments).toEqual([]);
	});

	it("does not reapply an old removal confirmation after later persistence succeeds", async () => {
		const sessionId = "later-persistence-wins-old-removal-confirmation";
		const attachment: FileAttachment = {
			id: "later-current-version",
			name: "later-current-version.txt",
			mimeType: "text/plain",
			bytes: 8,
			status: "ready",
			stagedPath: ".ao/attachments/later-current-version.txt",
		};
		const failPersistence = vi.fn(() => false);
		const owner = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				initialAttachments: [attachment],
				onAttachmentsChange: failPersistence,
			}),
		);
		act(() => owner.result.current.clear());
		const earlierConfirmation = capturePendingFileAttachmentsForSession(sessionId);

		const persistCurrentAbsence = vi.fn(() => true);
		const persistenceRetry = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				initialAttachments: [attachment],
				onAttachmentsChange: persistCurrentAbsence,
			}),
		);
		await waitFor(() => expect(persistCurrentAbsence).toHaveBeenCalledWith([]));
		act(() => discardCapturedPendingFileAttachments(earlierConfirmation));
		owner.unmount();
		persistenceRetry.unmount();

		const laterCurrent = renderHook(() =>
			useFileAttachments({
				initialKey: sessionId,
				initialAttachments: [attachment],
				onAttachmentsChange: persistCurrentAbsence,
			}),
		);
		expect(laterCurrent.result.current.attachments).toEqual([attachment]);
		act(() => laterCurrent.result.current.clear());
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

	it("discards a pending file read when its draft is cleared", async () => {
		const readers: FileReader[] = [];
		const read = vi.spyOn(FileReader.prototype, "readAsDataURL").mockImplementation(function (this: FileReader) {
			readers.push(this);
		});
		try {
			const { result } = renderHook(() => useFileAttachments());
			let pending!: Promise<void>;
			act(() => {
				pending = result.current.addFiles([file("discarded.png", 8, "image/png")]);
			});
			expect(result.current.hasPendingReads()).toBe(true);
			act(() => result.current.clear());
			expect(result.current.hasPendingReads()).toBe(false);
			await act(async () => {
				Object.defineProperty(readers[0], "result", { value: "data:image/png;base64,AQ==" });
				readers[0].dispatchEvent(new ProgressEvent("load"));
				await pending;
			});
			expect(result.current.attachments).toEqual([]);
			expect(await result.current.toSettledPayload()).toEqual([]);
		} finally {
			read.mockRestore();
		}
	});
	it("rejects an awaiting delivery after confirmed discard without cancelling later files", async () => {
		const sessionId = "discard-awaiting-delivery";
		let finish!: () => void;
		let staged: FileAttachment[] = [];
		const prepare = vi.fn((attachments: FileAttachment[]) => new Promise<FileAttachment[]>((resolve) => {
			staged = attachments;
			finish = () => resolve(attachments.map((attachment) => ({ ...attachment, stagedPath: `.ao/attachments/${attachment.name}` })));
		}));
		const { result } = renderHook(() => useFileAttachments({ initialKey: sessionId, prepareAttachments: prepare }));
		let adding!: Promise<void>;
		act(() => { adding = result.current.addFiles([file("discarded.txt")]); });
		await waitFor(() => expect(prepare).toHaveBeenCalledOnce());
		const delivery = result.current.toSettledPayload().then(() => "sent", (error: Error) => error.message);
		act(() => discardCapturedPendingFileAttachments(capturePendingFileAttachmentsForSession(sessionId)));
		await act(async () => { finish(); await adding; });
		expect(await delivery).toMatch(/discarded before delivery/);
		expect(result.current.attachments).toEqual([]);
		expect(staged).toHaveLength(1);
		prepare.mockImplementation(async (attachments) => attachments.map((attachment) => ({ ...attachment, stagedPath: `.ao/attachments/${attachment.name}` })));
		await act(async () => { await result.current.addFiles([file("later.txt")]); });
		expect(await result.current.toSettledPayload()).toMatchObject([{ name: "later.txt" }]);
		purgeFileAttachmentsForSession(sessionId);
	});

});
