import { statSync } from "node:fs";
import path from "node:path";

// Windows/Linux: dropping a folder onto the app's taskbar icon or a desktop/
// Start-menu shortcut launches (or, if already running, re-signals) the app
// with the folder's path as a plain positional argv entry — unlike every
// other argument this app's own launchers pass (--installed-via=, the
// ao-app:// deep link, Electron/Chromium's own --flags). A real directory is
// a reliable signal: nothing else ever present in this app's argv resolves
// to one, so the first argv entry that does is the dropped folder.
//
// In an unpackaged dev run (`electron-forge start`), Electron's own bootstrap
// slot at argv[1] is ALSO a real directory (the app path it was told to load,
// e.g. "." resolved to the frontend build dir) — every cold start and
// second-instance relaunch would otherwise queue that as a dropped folder.
// `process.defaultApp` marks exactly that slot; see registerCloudProtocol in
// cloud-auth.ts for the same convention.
export function parseOpenFolderPathArg(argv: string[]): string | undefined {
	const bootstrapAppIndex = process.defaultApp ? 1 : -1;
	for (const [index, entry] of argv.entries()) {
		if (index === bootstrapAppIndex || entry.startsWith("-") || entry.includes("://")) continue;
		try {
			if (statSync(path.resolve(entry)).isDirectory()) return path.resolve(entry);
		} catch {
			// Not a real filesystem path — true of most argv entries (script paths,
			// the electron executable itself, flags already skipped above).
		}
	}
	return undefined;
}
