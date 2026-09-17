import { formatTimeCompact as formatPortableTimeCompact } from "@aoagents/product-ui";
import { appI18n, type MessageKey } from "../i18n";

export function formatTimeCompact(isoDate: string | null | undefined): string {
	return formatPortableTimeCompact(isoDate, {
		translate: (key, values) => appI18n.t(key as MessageKey, values),
	});
}

/** Extra-terse relative time for space-constrained navigation rows. */
export function formatTimeTerse(
	isoDate: string | null | undefined,
	now: number | Date = Date.now(),
): string {
	if (!isoDate) return "now";
	const timestamp = new Date(isoDate).getTime();
	if (!Number.isFinite(timestamp)) return "now";
	const nowMs = now instanceof Date ? now.getTime() : now;
	const diffMinutes = Math.floor((nowMs - timestamp) / 60_000);
	if (diffMinutes < 1) return "now";
	if (diffMinutes < 60) return `${diffMinutes}m`;
	const diffHours = Math.floor(diffMinutes / 60);
	if (diffHours < 24) return `${diffHours}h`;
	return `${Math.floor(diffHours / 24)}d`;
}
