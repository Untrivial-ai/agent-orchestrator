export type SchedulePreset = "daily" | "weekly" | "custom";
export type CustomFrequency = "daily" | "weekly" | "monthly";
export type WeekdayCode = "MO" | "TU" | "WE" | "TH" | "FR" | "SA" | "SU";

export const WEEKDAY_CODES: WeekdayCode[] = ["MO", "TU", "WE", "TH", "FR", "SA", "SU"];

export type ScheduleFields = {
	preset: SchedulePreset;
	time: string;
	customFrequency: CustomFrequency;
	monthDay: string;
	weekday: WeekdayCode;
	hour: string;
	minute: string;
	/** Unmapped RRULE text preserved until the user edits the custom builder. */
	legacyRaw: string | null;
};

export function normalizeWeekday(value: string | undefined): WeekdayCode {
	const upper = value?.trim().toUpperCase();
	if (upper && WEEKDAY_CODES.includes(upper as WeekdayCode)) return upper as WeekdayCode;
	return "MO";
}

export function nowLocalHHMM(): string {
	const now = new Date();
	return `${String(now.getHours()).padStart(2, "0")}:${String(now.getMinutes()).padStart(2, "0")}`;
}

function parseRRuleParts(rruleText: string): Record<string, string> {
	const trimmed = rruleText.trim();
	const lines = trimmed.split("\n").map((line) => line.trim()).filter(Boolean);
	const rawRule =
		lines.length === 1 && lines[0].startsWith("RRULE:")
			? lines[0].slice("RRULE:".length)
			: lines.find((line) => line.startsWith("RRULE:"))?.slice("RRULE:".length) ?? trimmed;
	return Object.fromEntries(
		rawRule.split(";").map((part) => {
			const [key, ...value] = part.split("=");
			return [key, value.join("=")];
		}),
	);
}

function isSimpleDaily(parts: Record<string, string>): boolean {
	const keys = Object.keys(parts).sort().join(",");
	return (
		parts.FREQ === "DAILY" &&
		parts.BYHOUR !== undefined &&
		parts.BYMINUTE !== undefined &&
		parts.BYSECOND === "0" &&
		keys === "BYHOUR,BYMINUTE,BYSECOND,FREQ"
	);
}

function isSimpleWeekly(parts: Record<string, string>): boolean {
	const keys = Object.keys(parts).sort().join(",");
	const weekday = parts.BYDAY?.split(",")[0];
	return (
		parts.FREQ === "WEEKLY" &&
		weekday !== undefined &&
		!parts.BYDAY?.includes(",") &&
		WEEKDAY_CODES.includes(weekday as WeekdayCode) &&
		parts.BYHOUR !== undefined &&
		parts.BYMINUTE !== undefined &&
		parts.BYSECOND === "0" &&
		keys === "BYDAY,BYHOUR,BYMINUTE,BYSECOND,FREQ"
	);
}

function isSimpleMonthly(parts: Record<string, string>): boolean {
	const keys = Object.keys(parts).sort().join(",");
	return (
		parts.FREQ === "MONTHLY" &&
		parts.BYMONTHDAY !== undefined &&
		parts.BYHOUR !== undefined &&
		parts.BYMINUTE !== undefined &&
		parts.BYSECOND === "0" &&
		keys === "BYHOUR,BYMINUTE,BYMONTHDAY,BYSECOND,FREQ"
	);
}

export function scheduleFieldsFromRRule(rruleText: string): ScheduleFields {
	const trimmed = rruleText.trim();
	const defaultTime = nowLocalHHMM();
	const [defaultHour, defaultMinute] = defaultTime.split(":");
	const emptyCustom = (): ScheduleFields => ({
		preset: "custom",
		time: defaultTime,
		customFrequency: "monthly",
		monthDay: "1",
		weekday: "MO",
		hour: defaultHour,
		minute: defaultMinute,
		legacyRaw: trimmed || null,
	});

	if (!trimmed) {
		return {
			preset: "daily",
			time: defaultTime,
			customFrequency: "daily",
			monthDay: "1",
			weekday: "MO",
			hour: defaultHour,
			minute: defaultMinute,
			legacyRaw: null,
		};
	}

	const lines = trimmed.split("\n").map((line) => line.trim()).filter(Boolean);
	if (lines.length > 1 || trimmed.includes("INTERVAL=")) {
		return emptyCustom();
	}

	const parts = parseRRuleParts(trimmed);
	const hour = parts.BYHOUR ?? defaultHour;
	const minute = parts.BYMINUTE ?? defaultMinute;
	const time = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`;

	if (isSimpleDaily(parts)) {
		return {
			preset: "daily",
			time,
			customFrequency: "daily",
			monthDay: "1",
			weekday: "MO",
			hour,
			minute,
			legacyRaw: null,
		};
	}
	if (isSimpleWeekly(parts)) {
		return {
			preset: "weekly",
			time,
			customFrequency: "weekly",
			monthDay: "1",
			weekday: normalizeWeekday(parts.BYDAY),
			hour,
			minute,
			legacyRaw: null,
		};
	}
	if (isSimpleMonthly(parts)) {
		return {
			preset: "custom",
			time,
			customFrequency: "monthly",
			monthDay: parts.BYMONTHDAY,
			weekday: "MO",
			hour,
			minute,
			legacyRaw: null,
		};
	}

	return emptyCustom();
}

export function buildRRuleFromSchedule(fields: ScheduleFields): string {
	if (fields.legacyRaw) return fields.legacyRaw;
	if (fields.preset === "daily" || fields.preset === "weekly") {
		const parsed = parseTimeValue(fields.time);
		const hour = parsed?.hour ?? 0;
		const minute = parsed?.minute ?? 0;
		if (fields.preset === "daily") {
			return `FREQ=DAILY;BYHOUR=${hour};BYMINUTE=${minute};BYSECOND=0`;
		}
		return `FREQ=WEEKLY;BYDAY=${fields.weekday};BYHOUR=${hour};BYMINUTE=${minute};BYSECOND=0`;
	}
	const parsed = parseHourMinute(fields.hour, fields.minute);
	const hour = parsed?.hour ?? 0;
	const minute = parsed?.minute ?? 0;
	const monthDay = Number(fields.monthDay);
	if (fields.customFrequency === "daily") {
		return `FREQ=DAILY;BYHOUR=${hour};BYMINUTE=${minute};BYSECOND=0`;
	}
	if (fields.customFrequency === "weekly") {
		return `FREQ=WEEKLY;BYDAY=${fields.weekday};BYHOUR=${hour};BYMINUTE=${minute};BYSECOND=0`;
	}
	return `FREQ=MONTHLY;BYMONTHDAY=${monthDay};BYHOUR=${hour};BYMINUTE=${minute};BYSECOND=0`;
}

export function schedulesEqual(a: ScheduleFields, b: ScheduleFields): boolean {
	return (
		a.preset === b.preset &&
		a.time === b.time &&
		a.customFrequency === b.customFrequency &&
		a.monthDay === b.monthDay &&
		a.weekday === b.weekday &&
		a.hour === b.hour &&
		a.minute === b.minute &&
		a.legacyRaw === b.legacyRaw
	);
}

/** 24-hour clock values as HH:mm. Empty means unset. */
export function parseTimeValue(value: string): { hour: number; minute: number } | null {
	const match = value.trim().match(/^(\d{2}):(\d{2})$/);
	if (!match) return null;
	const hour = Number(match[1]);
	const minute = Number(match[2]);
	if (hour > 23 || minute > 59) return null;
	return { hour, minute };
}

export function clampHourDraft(raw: string): string {
	const digits = raw.replace(/\D/g, "").slice(0, 2);
	if (digits.length === 0) return "";
	if (digits.length === 1) return digits;
	const hour = Number(digits);
	if (hour > 23) return "23";
	return digits;
}

export function clampMinuteDraft(raw: string): string {
	const digits = raw.replace(/\D/g, "").slice(0, 2);
	if (digits.length === 0) return "";
	if (digits.length === 1) return digits;
	const minute = Number(digits);
	if (minute > 59) return "59";
	return digits;
}

export function clampMonthDayDraft(raw: string): string {
	const digits = raw.replace(/\D/g, "").slice(0, 2);
	if (digits.length === 0) return "";
	const day = Number(digits);
	if (day < 1) return "1";
	if (day > 31) return "31";
	return String(day);
}

export function parseHourMinute(hour: string, minute: string): { hour: number; minute: number } | null {
	if (!/^\d{1,2}$/.test(hour.trim()) || !/^\d{1,2}$/.test(minute.trim())) return null;
	const h = Number(hour);
	const m = Number(minute);
	if (h > 23 || m > 59) return null;
	return { hour: h, minute: m };
}

/** Digits-only draft → HH:mm, rejecting any digit that would make an invalid clock. */
export function formatTimeDraft(raw: string): string {
	const out: string[] = [];
	for (const ch of raw.replace(/\D/g, "")) {
		if (out.length >= 4) break;
		const digit = Number(ch);
		const pos = out.length;
		if (pos === 0) {
			if (digit > 2) {
				out.push("0", ch);
			} else {
				out.push(ch);
			}
			continue;
		}
		if (pos === 1) {
			if (Number(out[0]) === 2 && digit > 3) continue;
			out.push(ch);
			continue;
		}
		if (pos === 2) {
			if (digit > 5) continue;
			out.push(ch);
			continue;
		}
		out.push(ch);
	}
	const digits = out.slice(0, 4).join("");
	if (digits.length <= 2) return digits;
	return `${digits.slice(0, 2)}:${digits.slice(2)}`;
}
