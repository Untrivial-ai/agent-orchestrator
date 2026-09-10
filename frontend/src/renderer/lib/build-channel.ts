export function isNightlyBuild(version?: string): boolean {
	return version?.includes("-nightly.") ?? false;
}

export function isCommandPaletteEnabled(version?: string, isDev: boolean = import.meta.env.DEV): boolean {
	return isDev || isNightlyBuild(version);
}

/**
 * Splits a nightly version into the parts worth putting in front of a user.
 *
 * A raw nightly string ("0.12.11-nightly.202609021713") truncates to noise in a
 * sidebar row, and two consecutive nightlies differ only in the trailing digits.
 * The base version plus the build's own date reads at a glance and tells the
 * user how fresh the build actually is. Returns null for anything that is not a
 * nightly stamp, so callers fall back to the plain version.
 */
export function parseNightlyVersion(version?: string): { base: string; builtAt: Date } | null {
	const match = /^(\d+\.\d+\.\d+)-nightly\.(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(?:[.+]|$)/.exec(version ?? "");
	if (!match) return null;
	const [, base, year, month, day, hour, minute] = match;
	// The nightly stamp encodes the build time in UTC, so it must be parsed as
	// UTC components. The local Date constructor would read the stamp as local
	// wall time and shift the instant by the machine's UTC offset, which then
	// renders a wrong time (or the wrong calendar day) in the sidebar, the
	// Settings Updates section, and the restart dialog.
	const builtAt = new Date(
		Date.UTC(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute)),
	);
	// A stamp like 202699999999 parses into a nonsense date; treat it as unusable
	// rather than rendering "Invalid Date" into the sidebar. Validate with the
	// UTC getters so the check does not depend on the machine timezone either.
	if (Number.isNaN(builtAt.getTime()) || builtAt.getUTCMonth() !== Number(month) - 1) return null;
	return { base, builtAt };
}
