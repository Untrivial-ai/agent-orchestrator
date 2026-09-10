// @vitest-environment node
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { parseOpenFolderPathArg } from "./open-folder-arg";

const tempRoots: string[] = [];

async function tempDir(): Promise<string> {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-open-folder-arg-"));
	tempRoots.push(dir);
	return dir;
}

beforeEach(() => {
	tempRoots.length = 0;
});

afterEach(async () => {
	await Promise.all(tempRoots.map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("parseOpenFolderPathArg", () => {
	it("returns the resolved path of a real directory in argv", async () => {
		const dir = await tempDir();
		expect(parseOpenFolderPathArg(["electron.exe", dir])).toBe(path.resolve(dir));
	});

	it("returns undefined when no argv entry is a real directory", () => {
		expect(parseOpenFolderPathArg(["electron.exe", "C:\\app\\main.js", "--installed-via=msi"])).toBeUndefined();
	});

	it("ignores flag-like entries even if a path happens to follow the dash", () => {
		expect(parseOpenFolderPathArg(["-x", "--no-sandbox", "--installed-via=msi"])).toBeUndefined();
	});

	it("ignores URL-scheme entries such as the ao-app:// deep link", () => {
		expect(parseOpenFolderPathArg(["electron.exe", "ao-app://callback?token=abc"])).toBeUndefined();
	});

	it("skips a file path (not a directory) before finding the real directory", async () => {
		const dir = await tempDir();
		expect(parseOpenFolderPathArg(["electron.exe", __filename, dir])).toBe(path.resolve(dir));
	});

	// Regression: `electron-forge start` (dev) spawns Electron with its own app
	// path in argv[1] (e.g. "." resolved to the frontend build dir), a REAL
	// directory. Without excluding it, every dev cold start and second-instance
	// relaunch would misread AO's own source tree as a dropped project folder.
	describe("when running unpackaged (process.defaultApp)", () => {
		const originalDefaultApp = process.defaultApp;

		function setDefaultApp(value: boolean | undefined) {
			Object.defineProperty(process, "defaultApp", { configurable: true, value });
		}

		beforeEach(() => {
			setDefaultApp(true);
		});

		afterEach(() => {
			setDefaultApp(originalDefaultApp);
		});

		it("does not treat Electron's own bootstrap app path as a dropped folder", async () => {
			const appDir = await tempDir();
			expect(parseOpenFolderPathArg(["electron.exe", appDir])).toBeUndefined();
		});

		it("still finds a genuinely dropped folder listed after the bootstrap app path", async () => {
			const appDir = await tempDir();
			const dropped = await tempDir();
			expect(parseOpenFolderPathArg(["electron.exe", appDir, dropped])).toBe(path.resolve(dropped));
		});
	});
});
