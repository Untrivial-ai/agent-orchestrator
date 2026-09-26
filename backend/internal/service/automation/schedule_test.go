package automation

import (
	"strings"
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

func TestCanonicalizeScheduleWeeklyUIFiresOnChosenWeekday(t *testing.T) {
	// Friday 6 March 2026, 08:00 UTC — before the 09:00 occurrence, so Friday
	// itself is still a valid next run.
	now := time.Date(2026, time.March, 6, 8, 0, 0, 0, time.UTC)
	want := map[string]time.Time{
		"MO": time.Date(2026, time.March, 9, 9, 0, 0, 0, time.UTC),
		"TU": time.Date(2026, time.March, 10, 9, 0, 0, 0, time.UTC),
		"WE": time.Date(2026, time.March, 11, 9, 0, 0, 0, time.UTC),
		"TH": time.Date(2026, time.March, 12, 9, 0, 0, 0, time.UTC),
		"FR": time.Date(2026, time.March, 6, 9, 0, 0, 0, time.UTC),
		"SA": time.Date(2026, time.March, 7, 9, 0, 0, 0, time.UTC),
		"SU": time.Date(2026, time.March, 8, 9, 0, 0, 0, time.UTC),
	}
	for day, nextWant := range want {
		rruleText := "FREQ=WEEKLY;BYDAY=" + day + ";BYHOUR=9;BYMINUTE=0;BYSECOND=0"
		schedule, err := CanonicalizeSchedule(ScheduleInput{RRule: rruleText, Timezone: "UTC"}, now)
		if err != nil {
			t.Fatalf("CanonicalizeSchedule(%s): %v", day, err)
		}
		if !strings.Contains(schedule.RRuleText, "BYDAY="+day) {
			t.Fatalf("stored rule %q dropped BYDAY=%s", schedule.RRuleText, day)
		}
		if !schedule.NextRunAt.Equal(nextWant) {
			t.Fatalf("%s next run = %s, want %s", day, schedule.NextRunAt, nextWant)
		}
		following, err := NextOccurrence(schedule.RRuleText, schedule.Timezone, schedule.NextRunAt)
		if err != nil {
			t.Fatalf("NextOccurrence(%s): %v", day, err)
		}
		if !following.Equal(nextWant.AddDate(0, 0, 7)) {
			t.Fatalf("%s following run = %s, want %s", day, following, nextWant.AddDate(0, 0, 7))
		}
	}
}

func TestCanonicalizeScheduleWeeklyKeepsWallClockAcrossDST(t *testing.T) {
	// Friday 6 March 2026 is still EST. The following Friday, 13 March, is EDT.
	now := time.Date(2026, time.March, 6, 12, 0, 0, 0, time.UTC)
	schedule, err := CanonicalizeSchedule(ScheduleInput{
		RRule: "FREQ=WEEKLY;BYDAY=FR;BYHOUR=9;BYMINUTE=0;BYSECOND=0", Timezone: "America/New_York",
	}, now)
	if err != nil {
		t.Fatalf("CanonicalizeSchedule: %v", err)
	}
	wantThisFriday := time.Date(2026, time.March, 6, 14, 0, 0, 0, time.UTC) // 09:00 EST
	if !schedule.NextRunAt.Equal(wantThisFriday) {
		t.Fatalf("next run = %s, want %s", schedule.NextRunAt, wantThisFriday)
	}
	wantNextFriday := time.Date(2026, time.March, 13, 13, 0, 0, 0, time.UTC) // 09:00 EDT
	next, err := NextOccurrence(schedule.RRuleText, schedule.Timezone, schedule.NextRunAt)
	if err != nil {
		t.Fatalf("NextOccurrence: %v", err)
	}
	if !next.Equal(wantNextFriday) {
		t.Fatalf("following run = %s, want %s", next, wantNextFriday)
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
