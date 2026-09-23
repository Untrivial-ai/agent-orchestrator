function joinPath(...segments: string[]): string {
	return segments.map((segment) => segment.replace(/[/\\]+$/, "")).join("/");
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
