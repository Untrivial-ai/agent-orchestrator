import { describe, expect, it } from "vitest";
import { activateSessionFile, closeAllSessionFiles, closeSessionFile, openSessionFile } from "./session-file-tabs";

describe("session file tabs", () => {
	it("opens each path once and activates it", () => {
		const opened = openSessionFile({ openPaths: [], activePath: null }, "src/App.tsx");
		expect(openSessionFile(opened, "src/App.tsx")).toEqual({
			openPaths: ["src/App.tsx"],
			activePath: "src/App.tsx",
		});
	});

	it("returns to the nearest file and then the agent surface when closing", () => {
		expect(closeSessionFile({ openPaths: ["a.ts", "b.ts"], activePath: "a.ts" }, "a.ts")).toEqual({
			openPaths: ["b.ts"],
			activePath: "b.ts",
		});
		expect(closeSessionFile({ openPaths: ["a.ts"], activePath: "a.ts" }, "a.ts")).toEqual({
			openPaths: [],
			activePath: null,
		});
	});

	it("keeps files open while the agent surface is active and closes all explicitly", () => {
		expect(activateSessionFile({ openPaths: ["a.ts"], activePath: "a.ts" }, null)).toEqual({
			openPaths: ["a.ts"],
			activePath: null,
		});
		expect(closeAllSessionFiles()).toEqual({ openPaths: [], activePath: null });
	});
});
