import { afterEach, describe, expect, it, vi } from "vitest";
import { copyFile, mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	bundledTmuxBinaryPath,
	stableBundledTmuxBinaryPath,
	stageBundledTmuxBinary,
} from "./bundled-tmux";

describe("bundledTmuxBinaryPath", () => {
	it.each(["darwin", "linux"] as const)("uses the packaged tmux on %s", (platform) => {
		expect(bundledTmuxBinaryPath(true, "/opt/ao/resources", platform)).toBe(
			"/opt/ao/resources/tmux/bin/tmux",
		);
	});

	it("does not override tmux in development", () => {
		expect(bundledTmuxBinaryPath(false, "/opt/ao/resources", "darwin")).toBeNull();
	});

	it("does not require tmux on Windows", () => {
		expect(bundledTmuxBinaryPath(true, "C:\\AO\\resources", "win32")).toBeNull();
	});
});

describe("stableBundledTmuxBinaryPath", () => {
	it.each(["darwin", "linux"] as const)("uses durable versioned AO storage on %s", (platform) => {
		expect(stableBundledTmuxBinaryPath(true, "/home/me/.ao", "0.10.3", platform, "arm64")).toBe(
			`/home/me/.ao/runtime/tmux/0.10.3-${platform}-arm64/tmux`,
		);
	});

	it("sanitizes version components rather than allowing path traversal", () => {
		expect(stableBundledTmuxBinaryPath(true, "/home/me/.ao", "../next build", "linux", "x64")).toBe(
			"/home/me/.ao/runtime/tmux/.._next_build-linux-x64/tmux",
		);
	});

	it("does not stage a Windows binary", () => {
		expect(stableBundledTmuxBinaryPath(true, "C:\\AO", "0.10.3", "win32", "x64")).toBeNull();
	});
});

describe("stageBundledTmuxBinary", () => {
	const temporaryDirectories: string[] = [];

	afterEach(async () => {
		await Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
	});

	async function fixture(): Promise<{ source: string; destination: string }> {
		const root = await mkdtemp(path.join(os.tmpdir(), "ao-bundled-tmux-"));
		temporaryDirectories.push(root);
		const source = path.join(root, "resources", "tmux", "bin", "tmux");
		const destination = stableBundledTmuxBinaryPath(true, path.join(root, ".ao"), "0.13.0", "linux", "x64");
		await mkdir(path.dirname(source), { recursive: true });
		if (!destination) throw new Error("expected a Linux staging path");
		return { source, destination };
	}

	it("re-stages changed bundled content without an app version bump", async () => {
		const { source, destination } = await fixture();
		await writeFile(source, "tmux-old");
		await expect(stageBundledTmuxBinary(source, destination)).resolves.toBe(true);
		await expect(readFile(destination, "utf8")).resolves.toBe("tmux-old");

		// Keep both the app version and file size unchanged so the content check,
		// rather than the stable path or size check, has to detect the new build.
		await writeFile(source, "tmux-new");
		await expect(stageBundledTmuxBinary(source, destination)).resolves.toBe(true);
		await expect(readFile(destination, "utf8")).resolves.toBe("tmux-new");
	});

	it("does not re-stage when the bundled content is unchanged", async () => {
		const { source, destination } = await fixture();
		await writeFile(source, "unchanged");
		const copy = vi.fn(async (from: string, to: string) => copyFile(from, to));
		const move = vi.fn(async (from: string, to: string) => rename(from, to));

		await expect(stageBundledTmuxBinary(source, destination, { copyFile: copy, rename: move })).resolves.toBe(true);
		await expect(stageBundledTmuxBinary(source, destination, { copyFile: copy, rename: move })).resolves.toBe(false);
		expect(copy).toHaveBeenCalledTimes(1);
		expect(move).toHaveBeenCalledTimes(1);
	});

	it("keeps the previous binary intact when a re-stage is interrupted", async () => {
		const { source, destination } = await fixture();
		await writeFile(source, "tmux-old");
		await stageBundledTmuxBinary(source, destination);
		await writeFile(source, "tmux-new");

		const interruptedCopy = async (_from: string, temporary: string): Promise<void> => {
			await writeFile(temporary, "partial");
			throw new Error("copy interrupted");
		};
		await expect(
			stageBundledTmuxBinary(source, destination, { copyFile: interruptedCopy }),
		).rejects.toThrow("copy interrupted");

		await expect(readFile(destination, "utf8")).resolves.toBe("tmux-old");
		await expect(readdir(path.dirname(destination))).resolves.toEqual(["tmux"]);
	});
});
