export type RelativeTimeMessageKey =
	| "time.justNow"
	| "time.minutesAgo"
	| "time.hoursAgo"
	| "time.daysAgo";

export type RelativeTimeTranslator = (
	key: RelativeTimeMessageKey,
	values?: Readonly<{ n: number }>,
) => string;

const defaultRelativeTimeTranslator: RelativeTimeTranslator = (key, values) => {
	const count = values?.n ?? 0;
	switch (key) {
		case "time.justNow":
			return "just now";
		case "time.minutesAgo":
			return `${count}m ago`;
		case "time.hoursAgo":
			return `${count}h ago`;
		case "time.daysAgo":
			return `${count}d ago`;
	}
};

export type FormatTimeCompactOptions = {
	now?: number | Date;
	translate?: RelativeTimeTranslator;
};

export function formatTimeCompact(
	isoDate: string | null | undefined,
	options: FormatTimeCompactOptions = {},
): string {
	const translate = options.translate ?? defaultRelativeTimeTranslator;
	if (!isoDate) {
		return translate("time.justNow");
	}
	const timestamp = new Date(isoDate).getTime();
	if (!Number.isFinite(timestamp)) {
		return translate("time.justNow");
	}
	const now = options.now instanceof Date ? options.now.getTime() : (options.now ?? Date.now());
	const diffMs = now - timestamp;
	if (diffMs <= 0) {
		return translate("time.justNow");
	}
	const diffMinutes = Math.floor(diffMs / 60_000);
	const diffHours = Math.floor(diffMinutes / 60);
	if (diffMinutes < 1) {
		return translate("time.justNow");
	}
	if (diffMinutes < 60) {
		return translate("time.minutesAgo", { n: diffMinutes });
	}
	if (diffHours < 24) {
		return translate("time.hoursAgo", { n: diffHours });
	}
	return translate("time.daysAgo", { n: Math.floor(diffHours / 24) });
}

export function formatTokenCount(totalTokens: number, locale = "en-US"): string {
	const tokens = Math.max(0, Math.trunc(totalTokens));
	if (tokens < 1_000) {
		return `${tokens.toLocaleString(locale)} tok`;
	}
	if (tokens < 1_000_000) {
		return `${formatUnit(tokens / 1_000)}K tok`;
	}
	if (tokens < 1_000_000_000) {
		return `${formatUnit(tokens / 1_000_000)}M tok`;
	}
	return `${formatUnit(tokens / 1_000_000_000)}B tok`;
}

function formatUnit(value: number): string {
	return value.toFixed(1).replace(/\.0$/, "");
}
