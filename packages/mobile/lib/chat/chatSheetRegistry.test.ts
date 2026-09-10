import { beforeEach, describe, expect, it, vi } from "vitest";
import { chatSheetRoute, parkChatSheet, readChatSheet, releaseChatSheet, resetChatSheets } from "./chatSheetRegistry";

beforeEach(resetChatSheets);

describe("chat sheet registry", () => {
	it("keeps non-serializable sheet data alive only until release", () => {
		const onSelect = vi.fn();
		const key = parkChatSheet({ kind: "conversation-map", markers: [], onSelect });
		expect(readChatSheet(key)).toMatchObject({ kind: "conversation-map", onSelect });
		releaseChatSheet(key);
		expect(readChatSheet(key)).toBeUndefined();
	});

	it.each([
		["turn-settings", "/sheets/chat-settings"],
		["conversation-map", "/sheets/conversation-map"],
		["composer-picker", "/sheets/composer-picker"],
	] as const)("routes %s through a native sheet route", (kind, pathname) => {
		const route = chatSheetRoute({ kind } as never);
		expect(route.pathname).toBe(pathname);
		expect(readChatSheet(route.params.sheetKey)?.kind).toBe(kind);
	});
});
