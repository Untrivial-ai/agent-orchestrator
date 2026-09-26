package automation

import (
	"testing"
	"time"
)

// Removing the exactly-one schedule-input guard must make this test fail; an
// ambiguous request cannot have a deterministic canonical schedule.
func TestCanonicalizeScheduleRejectsAmbiguousInputs(t *testing.T) {
	now := time.Date(2026, time.March, 6, 15, 0, 0, 0, time.UTC)
	for _, input := range []ScheduleInput{
		{Timezone: "UTC"},
		{RRule: "FREQ=DAILY", Cron: "0 9 * * *", Timezone: "UTC"},
	} {
		if _, err := CanonicalizeSchedule(input, now); err == nil {
			t.Fatalf("CanonicalizeSchedule(%+v) succeeded, want ambiguity error", input)
		}
	}
}

// Accepting timezone abbreviations or second-level recurrence must make this
// test fail; both violate the durable scheduling contract.
func TestCanonicalizeScheduleRejectsUnsafeTimezoneAndFrequency(t *testing.T) {
	now := time.Date(2026, time.March, 6, 15, 0, 0, 0, time.UTC)
	for _, input := range []ScheduleInput{
		{RRule: "FREQ=DAILY", Timezone: "IST"},
		{RRule: "FREQ=SECONDLY", Timezone: "UTC"},
		{RRule: "FREQ=MINUTELY;BYSECOND=0,30", Timezone: "UTC"},
		{RRule: "DTSTART:20260923T090000Z\nRRULE:FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0,30", Timezone: "UTC"},
		{RRule: "DTSTART:20260101T090000Z\nRRULE:FREQ=YEARLY;BYMONTH=1;BYDAY=MO,TU,WE,TH,FR,SA,SU;BYSECOND=0,30;BYSETPOS=1,3,5,7,9,11,13,15,17,19,21,23,25,27,29,31,33,34", Timezone: "UTC"},
		{RRule: "DTSTART;TZID=America/New_York:20260306T090000\nRRULE:FREQ=DAILY", Timezone: "Europe/London"},
		{RRule: "FREQ=DAILY;COUNT=3", Timezone: "UTC"},
		{RRule: "FREQ=DAILY;UNTIL=20260310T090000Z", Timezone: "UTC"},
	} {
		if _, err := CanonicalizeSchedule(input, now); err == nil {
			t.Fatalf("CanonicalizeSchedule(%+v) succeeded, want validation error", input)
		}
	}
}

func TestCanonicalizeScheduleAllowsLowFrequencySecondOffsets(t *testing.T) {
	now := time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)
	for _, input := range []ScheduleInput{
		{RRule: "DTSTART:20260101T090030Z\nRRULE:FREQ=DAILY", Timezone: "UTC"},
		{RRule: "FREQ=MINUTELY;BYSECOND=30", Timezone: "UTC"},
	} {
		if _, err := CanonicalizeSchedule(input, now); err != nil {
			t.Fatalf("CanonicalizeSchedule(%+v): %v", input, err)
		}
	}
}

// Breaking five-field weekday cron conversion or timezone-local recurrence
// must make this test fail. The expected UTC instant is hand-derived from New
// York switching to daylight time on 8 March 2026.
func TestCanonicalizeScheduleConvertsWeekdayCronAcrossDST(t *testing.T) {
	now := time.Date(2026, time.March, 6, 15, 0, 0, 0, time.UTC) // Friday 10:00 EST.
	schedule, err := CanonicalizeSchedule(ScheduleInput{
		Cron: "0 9 * * 1-5", Timezone: "America/New_York",
	}, now)
	if err != nil {
		t.Fatalf("CanonicalizeSchedule: %v", err)
	}
	wantMonday := time.Date(2026, time.March, 9, 13, 0, 0, 0, time.UTC)
	if !schedule.NextRunAt.Equal(wantMonday) {
		t.Fatalf("next run = %s, want %s", schedule.NextRunAt, wantMonday)
	}
	wantTuesday := time.Date(2026, time.March, 10, 13, 0, 0, 0, time.UTC)
	next, err := NextOccurrence(schedule.RRuleText, schedule.Timezone, wantMonday)
	if err != nil {
		t.Fatalf("NextOccurrence: %v", err)
	}
	if !next.Equal(wantTuesday) {
		t.Fatalf("following run = %s, want %s", next, wantTuesday)
	}
}

// Recomputing a rule from the current UTC offset instead of its IANA timezone
// must make this test fail at the spring-forward boundary.
func TestCanonicalizeScheduleKeepsDailyWallClockAcrossDST(t *testing.T) {
	now := time.Date(2026, time.March, 7, 15, 0, 0, 0, time.UTC) // Saturday 10:00 EST.
	schedule, err := CanonicalizeSchedule(ScheduleInput{
		Cron: "0 9 * * *", Timezone: "America/New_York",
	}, now)
	if err != nil {
		t.Fatalf("CanonicalizeSchedule: %v", err)
	}
	want := time.Date(2026, time.March, 8, 13, 0, 0, 0, time.UTC) // Sunday 09:00 EDT.
	if !schedule.NextRunAt.Equal(want) {
		t.Fatalf("next run = %s, want %s", schedule.NextRunAt, want)
	}
}
