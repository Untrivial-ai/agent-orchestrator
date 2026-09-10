import { renderHook } from "@testing-library/react";
import { act } from "react";
import { describe, expect, it, vi } from "vitest";

import {
	MAX_ATTACHMENTS,
	MAX_ATTACHMENT_BYTES,
	MAX_ATTACHMENTS_BYTES,
	useFileAttachments,
} from "./useFileAttachments";

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
});
