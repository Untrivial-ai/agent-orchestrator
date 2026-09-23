import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { usePierreFileHighlightReady } from "./usePierreFileHighlight";

const highlighter = vi.hoisted(() => ({
	loaded: false,
	preload: vi.fn<() => Promise<void>>(),
}));

vi.mock("@pierre/diffs", () => ({
	getFiletypeFromFileName: (path: string) => path.endsWith(".ts") ? "typescript" : "text",
	getHighlighterIfLoaded: () => highlighter.loaded ? {} : undefined,
	preloadHighlighter: () => highlighter.preload(),
}));

describe("usePierreFileHighlightReady", () => {
	beforeEach(() => {
		highlighter.loaded = false;
		highlighter.preload.mockReset();
	});

	it("holds the first source render until its lazy grammar settles", async () => {
		let finish!: () => void;
		highlighter.preload.mockImplementation(() => new Promise<void>((resolve) => {
			finish = resolve;
		}));
		const { result } = renderHook(() => usePierreFileHighlightReady("src/App.ts"));

		expect(result.current).toBe(false);
		expect(highlighter.preload).toHaveBeenCalledTimes(1);
		act(() => finish());
		await waitFor(() => expect(result.current).toBe(true));
	});

	it("renders immediately when the grammar is already warm", () => {
		highlighter.loaded = true;
		const { result } = renderHook(() => usePierreFileHighlightReady("src/App.ts"));

		expect(result.current).toBe(true);
		expect(highlighter.preload).not.toHaveBeenCalled();
	});

	it("falls back to readable plain text when grammar loading fails", async () => {
		vi.spyOn(console, "warn").mockImplementation(() => undefined);
		highlighter.preload.mockRejectedValue(new Error("grammar unavailable"));
		const { result } = renderHook(() => usePierreFileHighlightReady("src/App.ts"));

		await waitFor(() => expect(result.current).toBe(true));
	});
});
