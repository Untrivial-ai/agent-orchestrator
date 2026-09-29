import { describe, expect, it } from "vitest";

import {
	hasWorkerActionDrawable,
	workerActionGlyph,
	workerActionSymbol,
	workerContextActions,
	type WorkerActionState,
} from "./worker-action-model";

const state = (over: Partial<WorkerActionState> = {}): WorkerActionState => ({
	pinned: false,
	terminated: false,
	stopped: false,
	hasPr: false,
	...over,
});

const ids = (over: Partial<WorkerActionState> = {}) => workerContextActions(state(over)).map((a) => a.id);

describe("workerContextActions", () => {
	it("offers the always-available actions on a plain running worker", () => {
		expect(ids()).toEqual(["open", "pin", "rename", "delete"]);
	});

	it("flips pin to unpin for a pinned worker", () => {
		expect(ids({ pinned: true })).toContain("unpin");
		expect(ids({ pinned: true })).not.toContain("pin");
	});

	it("offers the pull request only when there is one", () => {
		expect(ids({ hasPr: true })).toContain("openPr");
		expect(ids()).not.toContain("openPr");
	});

	// The chat screen already makes this choice: a terminated AO session is
	// restored, a merely stopped agent is resumed. Offering both would push that
	// distinction onto the user.
	it("offers resume for a stopped agent and restore for a terminated session", () => {
		expect(ids({ stopped: true })).toContain("resume");
		expect(ids({ stopped: true })).not.toContain("restore");
		expect(ids({ terminated: true })).toContain("restore");
		expect(ids({ terminated: true })).not.toContain("resume");
	});

	it("prefers restore when a session is both terminated and stopped", () => {
		expect(ids({ terminated: true, stopped: true })).toContain("restore");
		expect(ids({ terminated: true, stopped: true })).not.toContain("resume");
	});

	// Regression fence: there is no stop or pause endpoint. kill() is what Delete
	// calls, so a "stop" entry would be a second name for the same destructive
	// action.
	it("never offers an action without an endpoint behind it", () => {
		const everything = [
			...ids(),
			...ids({ pinned: true, hasPr: true, stopped: true }),
			...ids({ terminated: true, hasPr: true }),
		];
		expect(everything).not.toContain("stop");
		expect(everything).not.toContain("pause");
	});

	it("keeps the destructive action last and marked", () => {
		for (const over of [{}, { pinned: true, hasPr: true }, { terminated: true }]) {
			const actions = workerContextActions(state(over));
			const last = actions.at(-1);
			expect(last?.id).toBe("delete");
			expect(last?.destructive).toBe(true);
			expect(actions.filter((a) => a.destructive)).toHaveLength(1);
		}
	});

	it("opens with Open, so the menu's first item is the row's own tap", () => {
		expect(ids({ terminated: true, hasPr: true, pinned: true })[0]).toBe("open");
	});
});

describe("workerActionSymbol", () => {
	it("gives every action an SF Symbol, since iOS resolves them by name", () => {
		for (const action of workerContextActions(state({ hasPr: true, stopped: true }))) {
			expect(workerActionSymbol(action.id).length).toBeGreaterThan(0);
		}
	});

	it("distinguishes pin from unpin", () => {
		expect(workerActionSymbol("pin")).not.toBe(workerActionSymbol("unpin"));
	});
});

describe("workerActionGlyph", () => {
	// Android renders its own Modal, not the native menu, so these are what that
	// list actually shows. Every action needs one or a row renders iconless beside
	// rows that don't.
	it("gives every action a glyph", () => {
		for (const id of ["pin", "unpin", "rename", "delete", "open", "openPr", "resume", "restore"] as const) {
			expect(workerActionGlyph(id).name).toBeTruthy();
		}
	});

	// The swipe rail already offers pin and unpin with a pushpin; a menu that drew
	// the same action differently would read as a different action.
	// The renderer pins with Lucide's Pin/PinOff, so the rail does too — the same
	// pushpin the desktop shows, not a filled Material one.
	it("pins with the same pushpin the desktop uses", () => {
		expect(workerActionGlyph("pin")).toEqual({ family: "feather", name: "pin" });
		expect(workerActionGlyph("unpin")).toEqual({ family: "feather", name: "pin-off" });
	});

	// Destructive and recovery actions are the ones you must not confuse.
	it("keeps delete, resume and restore distinct", () => {
		const names = [workerActionGlyph("delete"), workerActionGlyph("resume"), workerActionGlyph("restore")].map((glyph) => glyph.name);
		expect(new Set(names).size).toBe(3);
	});
});

describe("hasWorkerActionDrawable", () => {
	// Android and the fallback menu require() a bundled drawable, and a missing
	// file is a bundle-time failure rather than a blank icon — so this list has to
	// track assets/icons exactly. Every action now ships one, so the Android menu
	// reads like the iOS one rather than a half-iconned list.
	it("claims an icon for every action", () => {
		for (const id of ["pin", "unpin", "rename", "delete", "open", "openPr", "resume", "restore"] as const) {
			expect(hasWorkerActionDrawable(id)).toBe(true);
		}
	});
});
