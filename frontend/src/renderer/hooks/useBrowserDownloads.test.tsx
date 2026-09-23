import { act, renderHook, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { BrowserDownloadsState } from "../../shared/browser-downloads";
import { useBrowserDownloads } from "./useBrowserDownloads";

it("surfaces nonfatal download errors from initial and live state", async () => {
	let changed: ((state: BrowserDownloadsState) => void) | undefined;
	window.ao!.browser.downloads.list = vi.fn(async () => ({
		downloads: [],
		error: "Could not prepare the Downloads folder.",
	}));
	window.ao!.browser.downloads.onChanged = vi.fn((listener) => {
		changed = listener;
		return () => undefined;
	});

	const { result } = renderHook(() => useBrowserDownloads());
	await waitFor(() => expect(result.current.error).toBe("Could not prepare the Downloads folder."));

	act(() => changed?.({ downloads: [], error: "Download storage is unavailable." }));
	expect(result.current.error).toBe("Download storage is unavailable.");
});
