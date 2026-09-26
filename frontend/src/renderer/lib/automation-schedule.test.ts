import { describe, expect, it } from "vitest";
import { WEEKDAY_CODES, buildRRuleFromSchedule, scheduleFieldsFromRRule } from "./automation-schedule";

describe("automation-schedule", () => {
	it("roundtrips every weekday the form can choose", () => {
		for (const weekday of WEEKDAY_CODES) {
			const rrule = `FREQ=WEEKLY;BYDAY=${weekday};BYHOUR=9;BYMINUTE=15;BYSECOND=0`;
			const fields = scheduleFieldsFromRRule(rrule);
			expect(fields.preset).toBe("weekly");
			expect(fields.weekday).toBe(weekday);
			expect(buildRRuleFromSchedule(fields)).toBe(rrule);
			expect(
				buildRRuleFromSchedule({
					...fields,
					preset: "custom",
					customFrequency: "weekly",
					hour: "9",
					minute: "15",
				}),
			).toBe(rrule);
		}
	});

	it("maps weekly RRULE to preset weekly with weekday and time", () => {
		const rrule = "FREQ=WEEKLY;BYDAY=FR;BYHOUR=14;BYMINUTE=30;BYSECOND=0";
		const fields = scheduleFieldsFromRRule(rrule);
		expect(fields.preset).toBe("weekly");
		expect(fields.weekday).toBe("FR");
		expect(fields.time).toBe("14:30");
		expect(fields.legacyRaw).toBeNull();
		expect(buildRRuleFromSchedule(fields)).toBe(rrule);
	});

	it("builds weekly RRULE from custom weekly fields", () => {
		expect(
			buildRRuleFromSchedule({
				preset: "custom",
				time: "09:00",
				customFrequency: "weekly",
				monthDay: "1",
				weekday: "WE",
				hour: "16",
				minute: "45",
				legacyRaw: null,
			}),
		).toBe("FREQ=WEEKLY;BYDAY=WE;BYHOUR=16;BYMINUTE=45;BYSECOND=0");
	});

	it("roundtrips daily and monthly simple rules", () => {
		const daily = "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0";
		expect(buildRRuleFromSchedule(scheduleFieldsFromRRule(daily))).toBe(daily);

		const monthly = "FREQ=MONTHLY;BYMONTHDAY=15;BYHOUR=8;BYMINUTE=15;BYSECOND=0";
		const monthlyFields = scheduleFieldsFromRRule(monthly);
		expect(monthlyFields.preset).toBe("custom");
		expect(monthlyFields.customFrequency).toBe("monthly");
		expect(monthlyFields.monthDay).toBe("15");
		expect(buildRRuleFromSchedule(monthlyFields)).toBe(monthly);
	});
});
