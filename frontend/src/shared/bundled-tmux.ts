import { createHash, randomUUID } from "node:crypto";
import { chmod, copyFile, mkdir, readFile, rename, rm, stat } from "node:fs/promises";
import path from "node:path";

function joinPath(...segments: string[]): string {
	return segments.map((segment) => segment.replace(/[/\\]+$/, "")).join("/");
}

interface BundledTmuxStagingDependencies {
	copyFile: (source: string, destination: string) => Promise<void>;
	rename: (source: string, destination: string) => Promise<void>;
}

const defaultStagingDependencies: BundledTmuxStagingDependencies = {
	copyFile: (source, destination) => copyFile(source, destination),
	rename: (source, destination) => rename(source, destination),
};

function isNotFound(error: unknown): boolean {
	return (error as NodeJS.ErrnoException).code === "ENOENT";
}

async function fileSha256(file: string): Promise<string> {
	return createHash("sha256").update(await readFile(file)).digest("hex");
}

async function filesMatch(source: string, destination: string): Promise<boolean> {
	const sourceStats = await stat(source);
	let destinationStats;
	try {
		destinationStats = await stat(destination);
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
		return sourceHash === destinationHash;
	} catch (error) {
		// Another app launch may have replaced or removed the staged file after
		// stat. Treat that like a mismatch and let the atomic staging path repair it.
		if (isNotFound(error)) return false;
		throw error;
	}
}

export async function stageBundledTmuxBinary(
	source: string,
	destination: string,
	dependencyOverrides: Partial<BundledTmuxStagingDependencies> = {},
): Promise<boolean> {
	if (await filesMatch(source, destination)) return false;

	const dependencies = { ...defaultStagingDependencies, ...dependencyOverrides };
	await mkdir(path.dirname(destination), { recursive: true, mode: 0o750 });
	const temporary = `${destination}.tmp-${process.pid}-${randomUUID()}`;
	try {
		await dependencies.copyFile(source, temporary);
		await chmod(temporary, 0o755);
		await dependencies.rename(temporary, destination);
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
