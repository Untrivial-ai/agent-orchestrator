/**
 * Update-outcome telemetry, owned by the main process.
 *
 * The app downloads updates automatically and installs them on quit, yet a
 * large share of installs sit on an old version for weeks. A stuck install
 * looks identical whether the check failed, the download failed, or the user
 * simply never quits, so these outcomes separate the cases.
 *
 * This lives in the main process rather than the renderer because the renderer
 * only ever sees *broadcast* statuses, and `auto-updater.ts` deliberately
 * suppresses the UI status for automatic checks: on failure it logs, restores
 * the previous status, and returns. Automatic checks run hourly and are how
 * updates normally happen, so a renderer-side observer would miss precisely the
 * silent-failure case this exists to diagnose. Reporting at the operation
 * boundary also makes `phase` and `to_version` authoritative, since only the
 * main process knows which operation was running and what it was fetching.
 *
 * Only enum-like fields cross the wire. The updater's raw message can carry
 * feed URLs and local staging paths, so it is bucketed into a category here and
 * never forwarded.
 */

export type UpdateFailureCategory =
	| "network"
	| "signature"
	| "permission"
	| "disk_space"
	| "not_found"
	| "not_supported"
	| "unknown";

/** Which stage of the update was running when the outcome occurred. */
export type UpdatePhase = "check" | "download";

/** Whether the update ran on the hourly timer or because the user asked. */
export type UpdateTrigger = "automatic" | "manual";

export type UpdateOutcome = {
	event: "ao.renderer.update_failed" | "ao.renderer.update_downloaded" | "ao.renderer.update_unsupported";
	phase: UpdatePhase;
	trigger: UpdateTrigger;
	error_category?: UpdateFailureCategory;
	to_version?: string;
	differential_eligible?: boolean;
	transfer_mode?: "differential" | "full";
	fallback?: boolean;
	transferred_bytes?: number;
	target_bytes?: number;
};

/**
 * True when the error is a Chromium network-stack failure (`net::ERR_*`). A
 * wedged network stack makes every updater request fail this way until the app
 * restarts, so the updater keys user-facing restart guidance off this (#3526).
 * Anchored at the start: that is how Chromium surfaces these errors.
 */
export function isNetErrorMessage(message: string | undefined): boolean {
	return /^net::/i.test(message ?? "");
}

/**
 * Buckets an updater error into a safe category.
 *
 * electron-updater surfaces failures as free-text, so this matches on
 * substrings. Anything unrecognized becomes "unknown" rather than leaking the
 * original text.
 */
export function updateFailureCategory(message: string | undefined): UpdateFailureCategory {
	const text = (message ?? "").toLowerCase();
	if (text === "") return "unknown";
	if (/enotfound|econnreset|econnrefused|etimedout|net::|socket|dns|network|502|503|504/.test(text)) {
		return "network";
	}
	if (/signature|code sign|notariz|checksum|sha512|integrity|not trusted/.test(text)) {
		return "signature";
	}
	if (/eacces|eperm|permission|denied|read-only|readonly/.test(text)) return "permission";
	if (/enospc|no space|disk full/.test(text)) return "disk_space";
	if (/404|not found/.test(text)) return "not_found";
	if (/unsupported|not supported|cannot update/.test(text)) return "not_supported";
	return "unknown";
}

/** Builds the failure outcome for an updater error. */
export function updateFailureOutcome(
	message: string | undefined,
	phase: UpdatePhase,
	trigger: UpdateTrigger,
	toVersion: string | undefined,
): UpdateOutcome {
	return {
		event: "ao.renderer.update_failed",
		phase,
		trigger,
		error_category: updateFailureCategory(message),
		...(toVersion ? { to_version: toVersion } : {}),
	};
}

/** Longest release-note text worth pushing to the renderer. */
export const RELEASE_NOTES_MAX_CHARS = 4000;

/**
 * Flattens electron-updater's release notes into plain text.
 *
 * The value comes from the release feed, so it is remote content: it arrives as
 * a string or a per-version array, and for the GitHub provider it is the
 * release body, which routinely contains HTML. Tags are stripped rather than
 * forwarded so no call site can be tempted to inject it as markup, and the
 * result is length-capped because a release body has no size limit.
 */
export function normalizeReleaseNotes(
	notes: string | Array<{ version?: string; note?: string | null }> | null | undefined,
): string | undefined {
	const raw = typeof notes === "string"
		? notes
		: Array.isArray(notes)
			? notes.map((entry) => entry?.note ?? "").join("\n\n")
			: "";
	const text = raw
		// Keep block boundaries as line breaks before tags are dropped, so
		// "<li>a</li><li>b</li>" does not collapse into "ab".
		.replace(/<\/(p|div|li|ul|ol|h[1-6]|tr)>/gi, "\n")
		.replace(/<br\s*\/?>/gi, "\n")
		.replace(/<[^>]*>/g, "")
		.replace(/&nbsp;/gi, " ")
		.replace(/&amp;/gi, "&")
		.replace(/&lt;/gi, "<")
		.replace(/&gt;/gi, ">")
		.replace(/&quot;/gi, '"')
		.replace(/&#39;/gi, "'")
		.replace(/[ \t]+/g, " ")
		.replace(/\n{3,}/g, "\n\n")
		.split("\n")
		.map((line) => line.trim())
		.join("\n")
		.trim();
	if (text === "") return undefined;
	return text.length > RELEASE_NOTES_MAX_CHARS
		? `${text.slice(0, RELEASE_NOTES_MAX_CHARS).trimEnd()}…`
		: text;
}
