package httpapi

import (
	"strings"
	"testing"
)

// The renderer derives a cloud session's display name from the task brief with a
// 100-character slice (frontend TaskComposer). validSessionInput must accept the
// full width of that slice, or cloud task creation 422s with "Session ... is
// invalid" for any brief longer than the cap. Regression guard for the 80 -> 100
// mismatch introduced by #5125.
func TestValidSessionInputDisplayNameLength(t *testing.T) {
	base := createSessionRequest{
		ProjectID:   "11111111-1111-1111-1111-111111111111",
		Kind:        "worker",
		Harness:     "claude-code",
		DisplayName: "ok",
		Prompt:      "do the work",
		Mode:        "trusted",
	}
	if !validSessionInput(base) {
		t.Fatal("baseline request should be valid")
	}

	cases := []struct {
		name        string
		displayName string
		want        bool
	}{
		{"empty rejected", "", false},
		{"single char ok", "a", true},
		{"80 chars ok", strings.Repeat("a", 80), true},
		{"100 chars ok (renderer slice max)", strings.Repeat("a", 100), true},
		{"101 chars rejected", strings.Repeat("a", 101), false},
		// Rune count, not byte length: 100 two-byte runes are 200 bytes but only
		// 100 characters, matching the renderer's char-based slice.
		{"100 multibyte runes ok", strings.Repeat("é", 100), true},
		{"101 multibyte runes rejected", strings.Repeat("é", 101), false},
	}
	for _, tc := range cases {
		req := base
		req.DisplayName = tc.displayName
		if got := validSessionInput(req); got != tc.want {
			t.Errorf("%s: validSessionInput(displayName runes=%d) = %v, want %v",
				tc.name, len([]rune(tc.displayName)), got, tc.want)
		}
	}
}
