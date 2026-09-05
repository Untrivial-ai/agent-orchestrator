package domain

import "testing"

// TestActivityStatePredicates pins the independent activity-state families.
func TestActivityStatePredicates(t *testing.T) {
	tests := []struct {
		state       ActivityState
		sticky      bool
		needsInput  bool
		recoverable bool
	}{
		{ActivityActive, false, false, true},
		{ActivityIdle, false, false, true},
		{ActivityWaitingInput, true, true, true},
		{ActivityBlocked, true, true, true},
		{ActivityExited, false, false, false},
		{"unknown", false, false, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsSticky(); got != tt.sticky {
				t.Errorf("IsSticky() = %v, want %v", got, tt.sticky)
			}
			if got := tt.state.NeedsInput(); got != tt.needsInput {
				t.Errorf("NeedsInput() = %v, want %v", got, tt.needsInput)
			}
			if got := tt.state.IsRecoverable(); got != tt.recoverable {
				t.Errorf("IsRecoverable() = %v, want %v", got, tt.recoverable)
			}
		})
	}
}
