/**
 * Text helpers for the updater, owned by the main process.
 *
 * Kept separate from the updater itself so the parsing rules — which consume
 * remote release-feed content and free-text error messages — stay unit-testable
 * without constructing a full updater.
 */

/**
 * True when the error is a Chromium network-stack failure (`net::ERR_*`). A
 * wedged network stack makes every updater request fail this way until the app
 * restarts, so the updater keys user-facing restart guidance off this (#3526).
 * Anchored at the start: that is how Chromium surfaces these errors.
 */
export function isNetErrorMessage(message: string | undefined): boolean {
	return /^net::/i.test(message ?? "");
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
