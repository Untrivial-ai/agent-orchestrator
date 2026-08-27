// Build identity for the About section. The formatting is split from the Expo
// lookup so it can be unit-tested without a native runtime — the same split as
// pushStatus.ts / push.ts.

/** Just the fields we read off expo-constants. */
export type BuildInfo = {
	version?: string | null;
	// iOS build number / Android versionCode, as a string.
	build?: string | null;
	// expo-updates identity. `embedded` is undefined where updates are off (dev).
	updateId?: string | null;
	channel?: string | null;
	runtimeVersion?: string | null;
	embedded?: boolean;
};

/**
 * "1.2.0 (42)", or "1.2.0" when the build number is missing or merely repeats
 * the version (Expo Go reports them identically, and "1.2.0 (1.2.0)" is noise).
 * Falls back to "unknown" rather than rendering an empty row.
 */
export function formatVersion(info: BuildInfo): string {
	const version = info.version?.trim();
	const build = info.build?.trim();
	if (!version) return build ? `build ${build}` : "unknown";
	if (!build || build === version) return version;
	return `${version} (${build})`;
}

/** "a1b2c3d4 on production", "embedded", or null where updates are off. */
export function formatUpdate(info: BuildInfo): string | null {
	if (info.embedded === undefined) return null;
	if (info.embedded || !info.updateId) return "embedded";
	const id = info.updateId.slice(0, 8);
	return info.channel ? `${id} on ${info.channel}` : id;
}

/** Version plus the short update id when an OTA bundle is running. */
export function formatVersionLine(info: BuildInfo): string {
	const update = info.embedded === false && info.updateId ? ` · ${info.updateId.slice(0, 8)}` : "";
	return formatVersion(info) + update;
}

/** A one-line device/build string for prefilling a bug report. */
export function bugReportBody(info: BuildInfo, platform: string, osVersion: string | number): string {
	const update = formatUpdate(info);
	const runtime = info.runtimeVersion ? ` (runtime ${info.runtimeVersion.slice(0, 8)})` : "";
	return [
		"",
		"",
		"---",
		`AO mobile: ${formatVersion(info)}`,
		...(update ? [`Update: ${update}${runtime}`] : []),
		`Platform: ${platform} ${osVersion}`,
	].join("\n");
}
