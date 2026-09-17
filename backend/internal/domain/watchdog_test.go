package domain

import (
	"testing"
	"time"
)

func TestWatchdogConfigResolve(t *testing.T) {
	tests := []struct {
		name string
		cfg  WatchdogConfig
		want ResolvedWatchdogConfig
	}{
		{"zero uses global defaults", WatchdogConfig{}, ResolvedWatchdogConfig{
			StalledAfter:           DefaultStalledAfterMinutes * time.Minute,
			QuestionPendingAfter:   DefaultQuestionPendingAfterMinutes * time.Minute,
			ConsecutiveInfraErrors: DefaultConsecutiveInfraErrors,
		}},
		{"partial override keeps defaults elsewhere", WatchdogConfig{StalledAfterMinutes: 30}, ResolvedWatchdogConfig{
			StalledAfter:           30 * time.Minute,
			QuestionPendingAfter:   DefaultQuestionPendingAfterMinutes * time.Minute,
			ConsecutiveInfraErrors: DefaultConsecutiveInfraErrors,
		}},
		{"full override", WatchdogConfig{StalledAfterMinutes: 1, QuestionPendingAfterMinutes: 2, ConsecutiveInfraErrors: 5}, ResolvedWatchdogConfig{
			StalledAfter:           time.Minute,
			QuestionPendingAfter:   2 * time.Minute,
			ConsecutiveInfraErrors: 5,
		}},
		{"negative treated as unset", WatchdogConfig{StalledAfterMinutes: -1}, ResolvedWatchdogConfig{
			StalledAfter:           DefaultStalledAfterMinutes * time.Minute,
			QuestionPendingAfter:   DefaultQuestionPendingAfterMinutes * time.Minute,
			ConsecutiveInfraErrors: DefaultConsecutiveInfraErrors,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.Resolve()
			if got.StalledAfter != tc.want.StalledAfter ||
				got.QuestionPendingAfter != tc.want.QuestionPendingAfter ||
				got.ConsecutiveInfraErrors != tc.want.ConsecutiveInfraErrors {
				t.Fatalf("Resolve() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDefaultWatchdogMatchesZeroConfig(t *testing.T) {
	got := DefaultWatchdog()
	want := WatchdogConfig{}.Resolve()
	if got.StalledAfter != want.StalledAfter ||
		got.QuestionPendingAfter != want.QuestionPendingAfter ||
		got.ConsecutiveInfraErrors != want.ConsecutiveInfraErrors {
		t.Fatalf("DefaultWatchdog() = %+v, want %+v", got, want)
	}
}

func TestWatchdogConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     WatchdogConfig
		wantErr bool
	}{
		{"zero is valid (all unset)", WatchdogConfig{}, false},
		{"positive thresholds valid", WatchdogConfig{StalledAfterMinutes: 10, QuestionPendingAfterMinutes: 5, ConsecutiveInfraErrors: 3}, false},
		{"negative stalled", WatchdogConfig{StalledAfterMinutes: -1}, true},
		{"negative question", WatchdogConfig{QuestionPendingAfterMinutes: -1}, true},
		{"negative infra count", WatchdogConfig{ConsecutiveInfraErrors: -1}, true},
		{"absurd stalled", WatchdogConfig{StalledAfterMinutes: maxWatchdogMinutes + 1}, true},
		{"absurd question", WatchdogConfig{QuestionPendingAfterMinutes: maxWatchdogMinutes + 1}, true},
		{"absurd infra count", WatchdogConfig{ConsecutiveInfraErrors: maxConsecutiveInfraErrors + 1}, true},
		{"one-week stalled accepted", WatchdogConfig{StalledAfterMinutes: maxWatchdogMinutes}, false},
		{"max infra count accepted", WatchdogConfig{ConsecutiveInfraErrors: maxConsecutiveInfraErrors}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestProjectConfigValidateIncludesWatchdog(t *testing.T) {
	// A bad watchdog block must be refused when the project config is set,
	// not discovered at derivation time.
	cfg := ProjectConfig{Watchdog: WatchdogConfig{StalledAfterMinutes: -1}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a negative watchdog threshold")
	}
}
