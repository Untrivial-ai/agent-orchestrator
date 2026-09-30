import { createHash, randomUUID } from "node:crypto";
import type { Stats } from "node:fs";
import { chmod, copyFile, link, mkdir, open, readFile, rename, rm, stat } from "node:fs/promises";
import path from "node:path";

function joinPath(...segments: string[]): string {
	return segments.map((segment) => segment.replace(/[/\\]+$/, "")).join("/");
}

interface BundledTmuxStagingDependencies {
	copyFile: (source: string, destination: string) => Promise<void>;
	stat: (target: string) => Promise<Stats>;
	link: (existingPath: string, newPath: string) => Promise<void>;
	chmod: (target: string, mode: number) => Promise<void>;
	rename: (source: string, destination: string) => Promise<void>;
	platform: NodeJS.Platform;
}

const defaultStagingDependencies: BundledTmuxStagingDependencies = {
	copyFile: (source, destination) => copyFile(source, destination),
	stat: (target) => stat(target),
	link: (existingPath, newPath) => link(existingPath, newPath),
	chmod: (target, mode) => chmod(target, mode),
	rename: (source, destination) => rename(source, destination),
	platform: process.platform,
};

function isNotFound(error: unknown): boolean {
	return (error as NodeJS.ErrnoException).code === "ENOENT";
}

function isAlreadyExists(error: unknown): boolean {
	return (error as NodeJS.ErrnoException).code === "EEXIST";
}

async function fileSha256(file: string): Promise<string> {
	return createHash("sha256").update(await readFile(file)).digest("hex");
}

async function filesMatch(
	source: string,
	destination: string,
	dependencies: BundledTmuxStagingDependencies,
): Promise<boolean> {
	const sourceStats = await dependencies.stat(source);
	let destinationStats;
	try {
		destinationStats = await dependencies.stat(destination);
	} catch (error) {
		if (isNotFound(error)) return false;
		throw error;
	}
	if (sourceStats.size !== destinationStats.size) return false;
	try {
		const [sourceHash, destinationHash] = await Promise.all([
			fileSha256(source),
			fileSha256(destination),
		]);
		if (sourceHash !== destinationHash) return false;
		if (dependencies.platform !== "win32" && (destinationStats.mode & 0o111) === 0) {
			await dependencies.chmod(destination, 0o755);
		}
		return true;
	} catch (error) {
		// Another app launch may have replaced or removed the staged file after
		// stat. Treat that like a mismatch and let the atomic staging path repair it.
		if (isNotFound(error)) return false;
		throw error;
	}
}

async function syncPath(target: string): Promise<void> {
	const handle = await open(target, "r");
	try {
		await handle.sync();
	} finally {
		await handle.close();
	}
}

export function retainedBundledTmuxBinaryPath(stagedBinary: string): string {
	return `${stagedBinary}.retained`;
}

async function preserveOutgoingBinary(
	destination: string,
	dependencies: BundledTmuxStagingDependencies,
): Promise<void> {
	try {
		await dependencies.stat(destination);
	} catch (error) {
		if (isNotFound(error)) return;
		throw error;
	}

	const retained = retainedBundledTmuxBinaryPath(destination);
	try {
		await dependencies.stat(retained);
		return;
	} catch (error) {
		if (!isNotFound(error)) throw error;
	}

	// Publish only a complete, flushed copy. A hard link is an atomic
	// create-if-absent operation on the same filesystem, so concurrent launches
	// can never replace the older retained client that may still be the only one
	// able to talk to a surviving server.
	const temporary = `${retained}.tmp-${process.pid}-${randomUUID()}`;
	try {
		await dependencies.copyFile(destination, temporary);
		await chmod(temporary, 0o755);
		await syncPath(temporary);
		try {
			await dependencies.link(temporary, retained);
		} catch (error) {
			if (!isAlreadyExists(error)) throw error;
		}
		await syncPath(path.dirname(retained));
	} finally {
		await rm(temporary, { force: true });
	}
}

export async function stageBundledTmuxBinary(
	source: string,
	destination: string,
	dependencyOverrides: Partial<BundledTmuxStagingDependencies> = {},
): Promise<boolean> {
	const dependencies = { ...defaultStagingDependencies, ...dependencyOverrides };
	if (await filesMatch(source, destination, dependencies)) return false;

	await mkdir(path.dirname(destination), { recursive: true, mode: 0o750 });
	// Capture the client that may own an already-running server before replacing
	// its stable path. If capture fails, staging aborts while destination remains
	// untouched and usable.
	await preserveOutgoingBinary(destination, dependencies);
	const temporary = `${destination}.tmp-${process.pid}-${randomUUID()}`;
	try {
		await dependencies.copyFile(source, temporary);
		await chmod(temporary, 0o755);
		await syncPath(temporary);
		await dependencies.rename(temporary, destination);
		await syncPath(path.dirname(destination));
		return true;
	} finally {
		await rm(temporary, { force: true });
	}
}

// Packaged Unix builds always point the daemon at AO's own tmux. Returning a
// concrete path (rather than prepending PATH) makes a broken/missing bundle fail
// closed instead of silently falling back to an arbitrary machine installation.
export function bundledTmuxBinaryPath(
	isPackaged: boolean,
	resourcesPath: string,
	platform: NodeJS.Platform,
): string | null {
	if (!isPackaged || (platform !== "darwin" && platform !== "linux")) return null;
	return joinPath(resourcesPath, "tmux", "bin", "tmux");
}

// The daemon can intentionally outlive Electron. AppImage resources cannot:
// they live under a temporary FUSE mount that disappears when Electron exits.
// Stage each app/platform build under AO's durable home and keep versions
// separate so an update cannot replace a binary used by an older daemon.
export function stableBundledTmuxBinaryPath(
	isPackaged: boolean,
	aoDataDir: string,
	appVersion: string,
	platform: NodeJS.Platform,
	arch: string,
): string | null {
	if (!isPackaged || (platform !== "darwin" && platform !== "linux")) return null;
	const identity = `${appVersion}-${platform}-${arch}`.replace(/[^a-zA-Z0-9._-]/g, "_");
	return joinPath(aoDataDir, "runtime", "tmux", identity, "tmux");
}
