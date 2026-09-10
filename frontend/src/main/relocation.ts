import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import semver from "semver";

/**
 * macOS relocation policy for a bundle running outside /Applications.
 *
 * `app.moveToApplicationsFolder()` is destructive by default: with no
 * conflictHandler it TRASHES whatever sits at /Applications and copies the
 * running bundle over it (Electron's electron_bundle_mover.mm). Calling it on
 * every launch meant that opening a stale copy (the original download still in
 * ~/Downloads, or one on a mounted dmg, neither of which Squirrel ever updates)
 * silently destroyed the updated install and pinned the user to the version
 * they first downloaded. See the "old copy overwrites the updated one" bug.
 *
 * So the decision is made here, up front, from versions rather than
 * unconditionally: relocate only when there is nothing to lose, hand off to the
 * install when it is at least as new, and otherwise do nothing at all.
 */

export type RelocationAction =
	/** Run where we are; touch nothing. */
	| "stay"
	/** Nothing newer to lose: move this bundle into /Applications. */
	| "relocate"
	/** An equal-or-newer install exists: launch it instead and quit. */
	| "handoff";

/** The system Applications folder moveToApplicationsFolder() copies into. */
const APPLICATIONS_DIR = "/Applications";

export interface RelocationInputs {
	/** app.isInApplicationsFolder(): true for /Applications AND ~/Applications. */
	inApplicationsFolder: boolean;
	/** A bundle already occupies /Applications/<our bundle name>. */
	installedPresent: boolean;
	/** That bundle's CFBundleShortVersionString, or null when unreadable. */
	installedVersion: string | null;
	/** app.getVersion(). */
	runningVersion: string;
}

/**
 * Pure relocation decision. The only branch that destroys anything is
 * "relocate", and it is reachable only when /Applications holds nothing or
 * holds a strictly older build. Every unknown resolves to "stay": an
 * unreadable version must never cost a user their install, nor hand their
 * session to a bundle we could not identify.
 */
export function decideRelocation(inputs: RelocationInputs): RelocationAction {
	if (inputs.inApplicationsFolder) return "stay";
	if (!inputs.installedPresent) return "relocate";

	const installed = semver.valid(inputs.installedVersion ?? "");
	const running = semver.valid(inputs.runningVersion);
	if (installed === null || running === null) return "stay";

	return semver.lt(installed, running) ? "relocate" : "handoff";
}

/** Path the running bundle would occupy under /Applications. */
export function installedBundlePath(runningBundlePath: string): string {
	return path.join(APPLICATIONS_DIR, path.basename(runningBundlePath));
}

/**
 * CFBundleShortVersionString out of a bundle's Info.plist, or null when the
 * bundle is absent or the key is unreadable. Regex on an XML plist on purpose:
 * no plist dependency, and a parse miss degrades to "unknown" rather than an
 * error, exactly like the release-yml readers in auto-updater.ts. Electron
 * Forge writes this plist as XML (verified on a packaged build).
 */
export function readBundleVersion(bundlePath: string): string | null {
	try {
		const plist = readFileSync(
			path.join(bundlePath, "Contents", "Info.plist"),
			"utf8",
		);
		const match =
			/<key>CFBundleShortVersionString<\/key>\s*<string>([^<]*)<\/string>/.exec(
				plist,
			);
		return match?.[1]?.trim() || null;
	} catch {
		return null;
	}
}

/**
 * Read the /Applications copy's state for decideRelocation. Sync on purpose:
 * this runs before the window opens and is two stats plus a small read.
 */
export function inspectInstalledBundle(runningBundlePath: string): {
	installedPresent: boolean;
	installedVersion: string | null;
} {
	const installed = installedBundlePath(runningBundlePath);
	if (!existsSync(installed))
		return { installedPresent: false, installedVersion: null };
	return {
		installedPresent: true,
		installedVersion: readBundleVersion(installed),
	};
}
