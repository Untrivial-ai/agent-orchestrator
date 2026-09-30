import { afterEach, describe, expect, it, vi } from "vitest";
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rename, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	bundledTmuxBinaryPath,
	retainedBundledTmuxBinaryPath,
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
		await expect(readFile(retainedBundledTmuxBinaryPath(destination), "utf8")).resolves.toBe("tmux-old");
	});

	it("never overwrites the first retained client across multiple content changes", async () => {
		const { source, destination } = await fixture();
		await writeFile(source, "tmux-a");
		await stageBundledTmuxBinary(source, destination);
		await writeFile(source, "tmux-b");
		await stageBundledTmuxBinary(source, destination);
		await writeFile(source, "tmux-c");
		await stageBundledTmuxBinary(source, destination);

		await expect(readFile(destination, "utf8")).resolves.toBe("tmux-c");
		await expect(readFile(retainedBundledTmuxBinaryPath(destination), "utf8")).resolves.toBe("tmux-a");
	});

	it("retains B after server replacement makes A obsolete before the next content change", async () => {
		const { source, destination } = await fixture();
		const retained = retainedBundledTmuxBinaryPath(destination);
		await writeFile(source, "tmux-a");
		await stageBundledTmuxBinary(source, destination);
		await writeFile(source, "tmux-b");
		await stageBundledTmuxBinary(source, destination);
		await expect(readFile(retained, "utf8")).resolves.toBe("tmux-a");

		// A current-client probe reaches the replacement server running B, so the
		// backend clears A's association and managed file before the next stage.
		await rm(retained);
		await writeFile(source, "tmux-c");
		await stageBundledTmuxBinary(source, destination);

		await expect(readFile(destination, "utf8")).resolves.toBe("tmux-c");
		await expect(readFile(retained, "utf8")).resolves.toBe("tmux-b");
	});

	it("makes the retained copy executable even if the outgoing binary lost its execute bits", async () => {
		const { source, destination } = await fixture();
		await writeFile(source, "tmux-a");
		await stageBundledTmuxBinary(source, destination);
		await chmod(destination, 0o644);
		await writeFile(source, "tmux-b");

		await expect(stageBundledTmuxBinary(source, destination)).resolves.toBe(true);

		const retainedStats = await stat(retainedBundledTmuxBinaryPath(destination));
		expect(retainedStats.mode & 0o111).not.toBe(0);
		expect(retainedStats.mode & 0o777).toBe(0o755);
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

	it("repairs a missing executable mode in place when the contents already match", async () => {
		const { source, destination } = await fixture();
		await mkdir(path.dirname(destination), { recursive: true });
		await writeFile(source, "matching-tmux");
		await writeFile(destination, "matching-tmux");
		await chmod(destination, 0o644);

		const repairMode = vi.fn((target: string, mode: number) => chmod(target, mode));
		const copy = vi.fn(async (from: string, to: string) => copyFile(from, to));
		const move = vi.fn(async (from: string, to: string) => rename(from, to));
		await expect(
			stageBundledTmuxBinary(source, destination, { chmod: repairMode, copyFile: copy, rename: move }),
		).resolves.toBe(false);

		expect(repairMode).toHaveBeenCalledTimes(1);
		expect(repairMode).toHaveBeenCalledWith(destination, 0o755);
		expect(copy).not.toHaveBeenCalled();
		expect(move).not.toHaveBeenCalled();
		expect((await stat(destination)).mode & 0o111).not.toBe(0);
	});

	it("does no filesystem mutation when matching contents are already executable", async () => {
		const { source, destination } = await fixture();
		await mkdir(path.dirname(destination), { recursive: true });
		await writeFile(source, "matching-tmux");
		await writeFile(destination, "matching-tmux");
		await chmod(destination, 0o755);

		const repairMode = vi.fn(async (target: string, mode: number) => chmod(target, mode));
		const copy = vi.fn(async (from: string, to: string) => copyFile(from, to));
		const move = vi.fn(async (from: string, to: string) => rename(from, to));
		await expect(
			stageBundledTmuxBinary(source, destination, { chmod: repairMode, copyFile: copy, rename: move }),
		).resolves.toBe(false);

		expect(repairMode).not.toHaveBeenCalled();
		expect(copy).not.toHaveBeenCalled();
		expect(move).not.toHaveBeenCalled();
	});

	it("skips executable-mode inspection and chmod on Windows", async () => {
		const { source, destination } = await fixture();
		await mkdir(path.dirname(destination), { recursive: true });
		await writeFile(source, "matching-tmux");
		await writeFile(destination, "matching-tmux");

		const modeRead = vi.fn();
		const inspect = vi.fn(async (target: string) => {
			const stats = await stat(target);
			return new Proxy(stats, {
				get(current, property, receiver) {
					if (property === "mode") {
						modeRead();
						throw new Error("Windows cache hit should not inspect executable mode");
					}
					return Reflect.get(current, property, receiver);
				},
			});
		});
		const repairMode = vi.fn(async (target: string, mode: number) => chmod(target, mode));
		await expect(
			stageBundledTmuxBinary(source, destination, {
				platform: "win32",
				stat: inspect,
				chmod: repairMode,
			}),
		).resolves.toBe(false);

		expect(modeRead).not.toHaveBeenCalled();
		expect(repairMode).not.toHaveBeenCalled();
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

	it("keeps both usable clients when staging is interrupted after retained capture", async () => {
		const { source, destination } = await fixture();
		await writeFile(source, "tmux-old");
		await stageBundledTmuxBinary(source, destination);
		await writeFile(source, "tmux-new");

		let copies = 0;
		const interruptAfterCapture = async (from: string, temporary: string): Promise<void> => {
			copies++;
			if (copies === 2) {
				await writeFile(temporary, "partial-new");
				throw new Error("staging interrupted");
			}
			await copyFile(from, temporary);
		};
		await expect(
			stageBundledTmuxBinary(source, destination, { copyFile: interruptAfterCapture }),
		).rejects.toThrow("staging interrupted");

		await expect(readFile(destination, "utf8")).resolves.toBe("tmux-old");
		await expect(readFile(retainedBundledTmuxBinaryPath(destination), "utf8")).resolves.toBe("tmux-old");
	});
});
