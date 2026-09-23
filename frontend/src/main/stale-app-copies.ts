import { randomUUID } from "node:crypto";
import { lstat, readFile, rename } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import semver from "semver";

export const AO_BUNDLE_ID = "dev.agent-orchestrator.desktop";
export const MAINTAINED_MAC_APP_PATH = "/Applications/Agent Orchestrator.app";

export interface BundleMetadata {
	bundleId: string | null;
	version: string | null;
}

export interface StaleAppCopy {
	path: string;
	version: string;
	device: number;
	inode: number;
}

interface DiscoveryDependencies {
	fileIdentity: (candidate: string) => Promise<{ device: number; inode: number } | null>;
	readMetadata: (candidate: string) => Promise<BundleMetadata>;
}

interface StagingDependencies {
	rename: (from: string, to: string) => Promise<void>;
}

interface RetirementDependencies {
	findCopies: () => Promise<StaleAppCopy[]>;
	confirm: (copies: StaleAppCopy[]) => Promise<boolean>;
	stage: (copy: StaleAppCopy) => Promise<string | null>;
	revalidate: (copy: StaleAppCopy, stagedPath: string) => Promise<boolean>;
	restore: (copy: StaleAppCopy, stagedPath: string) => Promise<void>;
	trashItem: (candidate: string) => Promise<void>;
	reportFailures: (paths: string[]) => Promise<void>;
}

interface MacRetirementOptions
	extends Omit<RetirementDependencies, "findCopies" | "stage" | "revalidate" | "restore"> {
	platform: NodeJS.Platform | string;
	isPackaged: boolean;
	runningPath: string;
	runningVersion: string;
	homeDir?: string;
	findCopies?: () => Promise<StaleAppCopy[]>;
	stage?: (copy: StaleAppCopy) => Promise<string | null>;
	revalidate?: (copy: StaleAppCopy, stagedPath: string) => Promise<boolean>;
	restore?: (copy: StaleAppCopy, stagedPath: string) => Promise<void>;
	discoveryDependencies?: Partial<DiscoveryDependencies>;
	stagingDependencies?: Partial<StagingDependencies>;
}

function plistString(contents: string, key: string): string | null {
	const escapedKey = key.replace(/[.*+?^\${}()|[\]\\]/g, "\\$&");
	const match = new RegExp(`<key>${escapedKey}</key>\\s*<string>([^<]*)</string>`).exec(contents);
	return match?.[1]?.trim() || null;
}

export async function readBundleMetadata(bundlePath: string): Promise<BundleMetadata> {
	const contents = await readFile(path.join(bundlePath, "Contents", "Info.plist"), "utf8");
	return {
		bundleId: plistString(contents, "CFBundleIdentifier"),
		version: plistString(contents, "CFBundleShortVersionString"),
	};
}

const defaultDiscoveryDependencies: DiscoveryDependencies = {
	fileIdentity: async (candidate) => {
		const stats = await lstat(candidate);
		if (stats.isSymbolicLink() || !stats.isDirectory()) return null;
		return { device: stats.dev, inode: stats.ino };
	},
	readMetadata: readBundleMetadata,
};

export async function findStaleAppCopies(
	input: { runningVersion: string; homeDir: string },
	dependencyOverrides: Partial<DiscoveryDependencies> = {},
): Promise<StaleAppCopy[]> {
	const runningVersion = semver.valid(input.runningVersion);
	if (runningVersion === null) return [];

	const dependencies = { ...defaultDiscoveryDependencies, ...dependencyOverrides };
	const candidates = [
		path.join(input.homeDir, "Downloads", "Agent Orchestrator.app"),
		path.join(input.homeDir, "Desktop", "Agent Orchestrator.app"),
	];
	const copies: StaleAppCopy[] = [];
	for (const candidate of candidates) {
		try {
			const identity = await dependencies.fileIdentity(candidate);
			if (!identity) continue;
			const metadata = await dependencies.readMetadata(candidate);
			if (metadata.bundleId !== AO_BUNDLE_ID) continue;
			const candidateVersion = semver.valid(metadata.version ?? "");
			if (candidateVersion && semver.lt(candidateVersion, runningVersion)) {
				copies.push({ path: candidate, version: candidateVersion, ...identity });
			}
		} catch {
			// Missing or unreadable candidates are left untouched.
		}
	}
	return copies;
}

const defaultStagingDependencies: StagingDependencies = {
	rename: (from, to) => rename(from, to),
};

// Move the candidate to a sibling path whose name only this process knows, so
// no other writer can swap the bundle between validation and trashing. Returns
// the staging path, or null when the candidate could not be moved aside.
export async function stageStaleAppCopy(
	copy: StaleAppCopy,
	dependencyOverrides: Partial<StagingDependencies> = {},
): Promise<string | null> {
	const dependencies = { ...defaultStagingDependencies, ...dependencyOverrides };
	const stagedPath = `${copy.path}.ao-retiring-${randomUUID()}`;
	try {
		await dependencies.rename(copy.path, stagedPath);
		return stagedPath;
	} catch {
		return null;
	}
}

// Restore a staged bundle to its original path, e.g. when validation fails or
// the move to Trash is rejected. Best effort: if a replacement already occupies
// the original path the rename fails and the staged bundle is left in place.
export async function restoreStagedAppCopy(
	copy: StaleAppCopy,
	stagedPath: string,
	dependencyOverrides: Partial<StagingDependencies> = {},
): Promise<void> {
	const dependencies = { ...defaultStagingDependencies, ...dependencyOverrides };
	await dependencies.rename(stagedPath, copy.path);
}

export async function isUnchangedStaleAppCopy(
	copy: StaleAppCopy,
	inspectPath: string,
	runningVersion: string,
	dependencyOverrides: Partial<DiscoveryDependencies> = {},
): Promise<boolean> {
	const currentVersion = semver.valid(runningVersion);
	if (!currentVersion) return false;
	const dependencies = { ...defaultDiscoveryDependencies, ...dependencyOverrides };
	try {
		const identity = await dependencies.fileIdentity(inspectPath);
		if (!identity || identity.device !== copy.device || identity.inode !== copy.inode) return false;
		const metadata = await dependencies.readMetadata(inspectPath);
		const bundleVersion = semver.valid(metadata.version ?? "");
		return metadata.bundleId === AO_BUNDLE_ID
			&& bundleVersion !== null
			&& bundleVersion === copy.version
			&& semver.lt(copy.version, currentVersion);
	} catch {
		return false;
	}
}

export function formatStaleAppCopies(copies: StaleAppCopy[]): string {
	return copies.map((copy) => `v${copy.version} - ${copy.path}`).join("\n");
}

export async function retireStaleAppCopies(dependencies: RetirementDependencies): Promise<void> {
	const copies = await dependencies.findCopies();
	if (copies.length === 0 || !(await dependencies.confirm(copies))) return;

	const failures: string[] = [];
	for (const copy of copies) {
		let stagedPath: string | null;
		try {
			// Bind the object to the operation before validating it: once it is
			// moved to a private path, a replacement renamed into the original
			// path can no longer be the thing we trash.
			stagedPath = await dependencies.stage(copy);
		} catch {
			failures.push(copy.path);
			continue;
		}
		if (stagedPath === null) continue;

		if (!(await dependencies.revalidate(copy, stagedPath).catch(() => false))) {
			// The moved object is not the stale copy we captured; put it back.
			await dependencies.restore(copy, stagedPath).catch(() => undefined);
			continue;
		}

		try {
			await dependencies.trashItem(stagedPath);
		} catch {
			await dependencies.restore(copy, stagedPath).catch(() => undefined);
			failures.push(copy.path);
		}
	}
	if (failures.length > 0) await dependencies.reportFailures(failures);
}

export async function retireStaleMacAppCopies(options: MacRetirementOptions): Promise<void> {
	if (options.platform !== "darwin" || !options.isPackaged) return;
	if (path.resolve(options.runningPath) !== MAINTAINED_MAC_APP_PATH) return;
	if (semver.valid(options.runningVersion) === null) return;

	await retireStaleAppCopies({
		findCopies: options.findCopies ?? (() => findStaleAppCopies({
			runningVersion: options.runningVersion,
			homeDir: options.homeDir ?? os.homedir(),
		}, options.discoveryDependencies)),
		confirm: options.confirm,
		stage: options.stage ?? ((copy) => stageStaleAppCopy(copy, options.stagingDependencies)),
		revalidate: options.revalidate ?? ((copy, stagedPath) => isUnchangedStaleAppCopy(
			copy,
			stagedPath,
			options.runningVersion,
			options.discoveryDependencies,
		)),
		restore: options.restore ?? ((copy, stagedPath) => restoreStagedAppCopy(
			copy,
			stagedPath,
			options.stagingDependencies,
		)),
		trashItem: options.trashItem,
		reportFailures: options.reportFailures,
	});
}
