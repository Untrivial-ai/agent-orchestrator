import { describe, expect, it } from "vitest";
import { NO_CHAT_AGENT_NOTICE, SPAWN_OFFLINE_NOTICE, spawnNotices, type SpawnNoticeInput } from "./spawnNotices";

const base: SpawnNoticeInput = {
	offline: false,
	mode: "chat",
	loading: false,
	catalogLoaded: true,
	catalogError: null,
	agentCount: 2,
};

describe("spawnNotices", () => {
	it("shows one line while the desktop is unreachable, whatever else failed", () => {
		expect(spawnNotices({ ...base, offline: true, catalogLoaded: false, catalogError: "Couldn't reach your desktop.", agentCount: 0, modelError: "Couldn't reach your desktop." }))
			.toEqual([SPAWN_OFFLINE_NOTICE]);
	});

	it("does not blame the install when the catalog failed to load", () => {
		const notices = spawnNotices({ ...base, catalogLoaded: false, catalogError: "Couldn't reach your desktop.", agentCount: 0 });
		expect(notices).not.toContain(NO_CHAT_AGENT_NOTICE);
		expect(notices).toEqual(["Couldn't reach your desktop."]);
	});

	it("says no agent supports Chat only when the catalog answered empty", () => {
		expect(spawnNotices({ ...base, agentCount: 0 })).toEqual([NO_CHAT_AGENT_NOTICE]);
		expect(spawnNotices({ ...base, agentCount: 0, mode: "tui" })).toEqual([]);
		expect(spawnNotices({ ...base, agentCount: 0, loading: true })).toEqual([]);
	});

	it("lets a rejected password through, since no reconnect will fix it", () => {
		const rescan = "Your desktop rejected this phone's password. Re-scan the pairing code on your desktop.";
		expect(spawnNotices({ ...base, offline: false, catalogLoaded: false, catalogError: rescan, agentCount: 0 })).toEqual([rescan]);
	});

	it("collapses the same failure reported by two requests", () => {
		const copy = "Couldn't reach your desktop.";
		expect(spawnNotices({ ...base, catalogError: copy, modelError: copy })).toEqual([copy]);
	});
});
