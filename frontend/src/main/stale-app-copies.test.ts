// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { lstat } from "node:fs/promises";
import {
	AO_BUNDLE_ID,
	findStaleAppCopies,
	isUnchangedStaleAppCopy,
	readBundleMetadata,
	restoreStagedAppCopy,
	retireStaleAppCopies,
	retireStaleMacAppCopies,
	stageStaleAppCopy,
	type BundleMetadata,
	type StaleAppCopy,
} from "./stale-app-copies";

const RUNNING_PATH = "/Applications/Agent Orchestrator.app";
const RUNNING_VERSION = "0.13.1-nightly.202609121623";
const DOWNLOADS_COPY = "/Users/user/Downloads/Agent Orchestrator.app";
const DESKTOP_COPY = "/Users/user/Desktop/Agent Orchestrator.app";
const STAGED_PATH = "/Users/user/Downloads/Agent Orchestrator.app.ao-retiring-test";
const ORIGINAL_IDENTITY = { device: 1, inode: 10 };

describe("readBundleMetadata", () => {
	const temporaryDirectories: string[] = [];

	afterEach(async () => {
		await Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
	});

	it("reads the exact identifier and version from Info.plist", async () => {
		const directory = await mkdtemp(path.join(os.tmpdir(), "ao-stale-copy-"));
		temporaryDirectories.push(directory);
		const bundle = path.join(directory, "Agent Orchestrator.app");
		await mkdir(path.join(bundle, "Contents"), { recursive: true });
		await writeFile(path.join(bundle, "Contents", "Info.plist"), `<?xml version="1.0"?>
<plist><dict>
<key>CFBundleIdentifier</key><string>${AO_BUNDLE_ID}</string>
<key>CFBundleShortVersionString</key><string>0.10.3</string>
</dict></plist>`);

		await expect(readBundleMetadata(bundle)).resolves.toEqual({
			bundleId: AO_BUNDLE_ID,
			version: "0.10.3",
		});
	});
});

describe("stageStaleAppCopy", () => {
	const temporaryDirectories: string[] = [];

	afterEach(async () => {
		await Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
	});

	async function makeBundle(): Promise<{ directory: string; bundle: string; copy: StaleAppCopy }> {
		const directory = await mkdtemp(path.join(os.tmpdir(), "ao-stage-"));
		temporaryDirectories.push(directory);
		const bundle = path.join(directory, "Agent Orchestrator.app");
		await mkdir(bundle, { recursive: true });
		const stats = await lstat(bundle);
		return { directory, bundle, copy: { path: bundle, version: "0.10.3", device: stats.dev, inode: stats.ino } };
	}

	it("moves the bundle to a private sibling path and can restore it", async () => {
		const { bundle, copy } = await makeBundle();
		const staged = await stageStaleAppCopy(copy);
		expect(staged).not.toBeNull();
		expect(staged).not.toBe(bundle);
		await expect(lstat(bundle)).rejects.toThrow();
		const stagedStats = await lstat(staged as string);
		expect(stagedStats.ino).toBe(copy.inode);

		await restoreStagedAppCopy(copy, staged as string);
		await expect(lstat(staged as string)).rejects.toThrow();
		await expect(lstat(bundle)).resolves.toBeDefined();
	});

	it("returns null when the candidate no longer exists", async () => {
		const { directory } = await makeBundle();
		const missing: StaleAppCopy = { path: path.join(directory, "Gone.app"), version: "0.10.3", device: 1, inode: 1 };
		await expect(stageStaleAppCopy(missing)).resolves.toBeNull();
	});
});

describe("findStaleAppCopies", () => {
	function dependencies(options: {
		identities?: Record<string, { device: number; inode: number } | null>;
		metadata?: Record<string, BundleMetadata>;
	} = {}) {
		return {
			fileIdentity: vi.fn(async (candidate: string) => options.identities?.[candidate] ?? null),
			readMetadata: vi.fn(async (candidate: string) => {
				const metadata = options.metadata?.[candidate];
				if (!metadata) throw new Error("unreadable");
				return metadata;
			}),
		};
	}

	it("finds valid older AO copies only in Downloads and Desktop", async () => {
		const deps = dependencies({
			identities: {
				[DOWNLOADS_COPY]: ORIGINAL_IDENTITY,
				[DESKTOP_COPY]: { device: 1, inode: 11 },
			},
			metadata: {
				[DOWNLOADS_COPY]: { bundleId: AO_BUNDLE_ID, version: "0.10.3" },
				[DESKTOP_COPY]: { bundleId: AO_BUNDLE_ID, version: "0.12.0" },
			},
		});

		await expect(findStaleAppCopies({
			runningVersion: RUNNING_VERSION,
			homeDir: "/Users/user",
		}, deps)).resolves.toEqual([
			{ path: DOWNLOADS_COPY, version: "0.10.3", ...ORIGINAL_IDENTITY },
			{ path: DESKTOP_COPY, version: "0.12.0", device: 1, inode: 11 },
		]);
		expect(deps.fileIdentity).toHaveBeenCalledTimes(2);
	});

	it.each([
		["another app", ORIGINAL_IDENTITY, { bundleId: "com.example.other", version: "0.10.3" }],
		["an unreadable version", ORIGINAL_IDENTITY, { bundleId: AO_BUNDLE_ID, version: null }],
		["an invalid version", ORIGINAL_IDENTITY, { bundleId: AO_BUNDLE_ID, version: "broken" }],
		["the same version", ORIGINAL_IDENTITY, { bundleId: AO_BUNDLE_ID, version: RUNNING_VERSION }],
		["a newer version", ORIGINAL_IDENTITY, { bundleId: AO_BUNDLE_ID, version: "0.14.0" }],
		["a symlink or regular file", null, { bundleId: AO_BUNDLE_ID, version: "0.10.3" }],
	] as const)("leaves %s untouched", async (_label, identity, metadata) => {
		const deps = dependencies({
			identities: { [DOWNLOADS_COPY]: identity },
			metadata: { [DOWNLOADS_COPY]: metadata },
		});

		await expect(findStaleAppCopies({
			runningVersion: RUNNING_VERSION,
			homeDir: "/Users/user",
		}, deps)).resolves.toEqual([]);
	});

	it("does nothing when the running version is invalid", async () => {
		const deps = dependencies();
		await expect(findStaleAppCopies({
			runningVersion: "development",
			homeDir: "/Users/user",
		}, deps)).resolves.toEqual([]);
		expect(deps.fileIdentity).not.toHaveBeenCalled();
	});
});

describe("isUnchangedStaleAppCopy", () => {
	const stale: StaleAppCopy = {
		path: DOWNLOADS_COPY,
		version: "0.10.3",
		...ORIGINAL_IDENTITY,
	};

	function dependencies(identity: { device: number; inode: number } | null, metadata: BundleMetadata) {
		return {
			fileIdentity: async () => identity,
			readMetadata: async () => metadata,
		};
	}

	it("accepts a bundle whose plist version differs textually but canonicalises to the captured value", async () => {
		await expect(isUnchangedStaleAppCopy(
			stale,
			STAGED_PATH,
			RUNNING_VERSION,
			dependencies(ORIGINAL_IDENTITY, { bundleId: AO_BUNDLE_ID, version: " v0.10.3 " }),
		)).resolves.toBe(true);
	});

	it("rejects a bundle whose canonical version no longer matches the captured value", async () => {
		await expect(isUnchangedStaleAppCopy(
			stale,
			STAGED_PATH,
			RUNNING_VERSION,
			dependencies(ORIGINAL_IDENTITY, { bundleId: AO_BUNDLE_ID, version: "0.11.0" }),
		)).resolves.toBe(false);
	});

	it("inspects the staging path, not the original path", async () => {
		const fileIdentity = vi.fn(async () => ORIGINAL_IDENTITY);
		await isUnchangedStaleAppCopy(stale, STAGED_PATH, RUNNING_VERSION, {
			fileIdentity,
			readMetadata: async () => ({ bundleId: AO_BUNDLE_ID, version: "0.10.3" }),
		});
		expect(fileIdentity).toHaveBeenCalledWith(STAGED_PATH);
	});
});

describe("retireStaleAppCopies", () => {
	const stale: StaleAppCopy = {
		path: DOWNLOADS_COPY,
		version: "0.10.3",
		...ORIGINAL_IDENTITY,
	};

	it("requires confirmation before staging a copy", async () => {
		const stage = vi.fn(async () => STAGED_PATH);
		const trashItem = vi.fn(async () => undefined);
		await retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => false,
			stage,
			revalidate: async () => true,
			restore: vi.fn(),
			trashItem,
			reportFailures: vi.fn(),
		});
		expect(stage).not.toHaveBeenCalled();
		expect(trashItem).not.toHaveBeenCalled();
	});

	it("trashes the staged path once validation passes", async () => {
		const trashItem = vi.fn(async () => undefined);
		await retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => true,
			stage: async () => STAGED_PATH,
			revalidate: async () => true,
			restore: vi.fn(),
			trashItem,
			reportFailures: vi.fn(),
		});
		expect(trashItem).toHaveBeenCalledWith(STAGED_PATH);
	});

	it("restores and reports a staged copy that macOS could not move to Trash", async () => {
		const restore = vi.fn(async () => undefined);
		const reportFailures = vi.fn(async () => undefined);
		await retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => true,
			stage: async () => STAGED_PATH,
			revalidate: async () => true,
			restore,
			trashItem: async () => { throw new Error("denied"); },
			reportFailures,
		});
		expect(restore).toHaveBeenCalledWith(stale, STAGED_PATH);
		expect(reportFailures).toHaveBeenCalledWith([DOWNLOADS_COPY]);
	});

	it("restores without trashing when the staged bundle fails revalidation", async () => {
		const restore = vi.fn(async () => undefined);
		const trashItem = vi.fn(async () => undefined);
		await retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => true,
			stage: async () => STAGED_PATH,
			revalidate: async () => false,
			restore,
			trashItem,
			reportFailures: vi.fn(),
		});
		expect(trashItem).not.toHaveBeenCalled();
		expect(restore).toHaveBeenCalledWith(stale, STAGED_PATH);
	});

	it("skips a candidate that could not be moved aside", async () => {
		const revalidate = vi.fn(async () => true);
		const trashItem = vi.fn(async () => undefined);
		await retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => true,
			stage: async () => null,
			revalidate,
			restore: vi.fn(),
			trashItem,
			reportFailures: vi.fn(),
		});
		expect(revalidate).not.toHaveBeenCalled();
		expect(trashItem).not.toHaveBeenCalled();
	});

	it("trashes only the staged bundle even if the original path is swapped after revalidation", async () => {
		// The whole point of staging: once revalidation passes, a replacement
		// renamed back into the original path cannot become what gets trashed.
		let originalPath = DOWNLOADS_COPY;
		const trashItem = vi.fn(async () => undefined);
		await retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => true,
			stage: async () => STAGED_PATH,
			revalidate: async () => {
				// A replacement lands at the original path after we validate.
				originalPath = `${DOWNLOADS_COPY}#replacement`;
				return true;
			},
			restore: vi.fn(),
			trashItem,
			reportFailures: vi.fn(),
		});
		expect(trashItem).toHaveBeenCalledWith(STAGED_PATH);
		expect(trashItem).not.toHaveBeenCalledWith(originalPath);
	});

	it("leaves a replacement untouched when the bundle changes while confirmation is open", async () => {
		let approve: (() => void) | undefined;
		let markConfirmStarted: (() => void) | undefined;
		const confirmStarted = new Promise<void>((resolve) => {
			markConfirmStarted = resolve;
		});
		const confirm = new Promise<boolean>((resolve) => {
			approve = () => resolve(true);
		});
		let identity = ORIGINAL_IDENTITY;
		const dependencies = {
			fileIdentity: async () => identity,
			readMetadata: async () => ({ bundleId: AO_BUNDLE_ID, version: "0.10.3" }),
		};
		const restore = vi.fn(async () => undefined);
		const trashItem = vi.fn(async () => undefined);
		const retirement = retireStaleAppCopies({
			findCopies: async () => [stale],
			confirm: async () => {
				markConfirmStarted?.();
				return confirm;
			},
			stage: async () => STAGED_PATH,
			revalidate: (copy, stagedPath) => isUnchangedStaleAppCopy(copy, stagedPath, RUNNING_VERSION, dependencies),
			restore,
			trashItem,
			reportFailures: vi.fn(),
		});

		await confirmStarted;
		identity = { device: 1, inode: 99 };
		approve?.();
		await retirement;

		expect(trashItem).not.toHaveBeenCalled();
		expect(restore).toHaveBeenCalledWith(stale, STAGED_PATH);
	});
});

describe("retireStaleMacAppCopies", () => {
	function runtime(overrides: Record<string, unknown> = {}) {
		return {
			platform: "darwin",
			isPackaged: true,
			runningPath: RUNNING_PATH,
			runningVersion: RUNNING_VERSION,
			findCopies: vi.fn(async () => []),
			confirm: vi.fn(async () => false),
			trashItem: vi.fn(async () => undefined),
			reportFailures: vi.fn(async () => undefined),
			...overrides,
		};
	}

	it.each([
		{ platform: "linux" },
		{ isPackaged: false },
		{ runningPath: DOWNLOADS_COPY },
		{ runningVersion: "development" },
	])("does not inspect files outside the maintained packaged macOS app: %o", async (override) => {
		const options = runtime(override);
		await retireStaleMacAppCopies(options);
		expect(options.findCopies).not.toHaveBeenCalled();
	});

	it("checks once after the maintained packaged macOS app starts", async () => {
		const options = runtime();
		await retireStaleMacAppCopies(options);
		expect(options.findCopies).toHaveBeenCalledOnce();
	});
});
