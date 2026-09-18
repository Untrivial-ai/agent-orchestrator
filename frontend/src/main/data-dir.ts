import path from "node:path";

/** Resolve the AO data directory, honoring an explicit `AO_DATA_DIR` override. */
export function resolveDesktopDataDir(
	env: Record<string, string | undefined>,
	homeDir: string,
	launchWorkingDirectory: string,
	isPackaged: boolean,
): string {
	const configured = env.AO_DATA_DIR?.trim();
	if (configured) return path.resolve(launchWorkingDirectory, configured);
	return path.resolve(homeDir, ".ao", isPackaged ? "data" : path.join("dev", "data"));
}
