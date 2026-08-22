package inputdelay

import (
	"strings"
	"testing"
	"time"
)

func TestForMessage(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		configured time.Duration
		want       time.Duration
	}{
		{"empty nudge", "", 300 * time.Millisecond, 0},
		{"short line", "hello", 300 * time.Millisecond, 300 * time.Millisecond},
		{"multiline", "hello\nworld", 300 * time.Millisecond, time.Second},
		{"first chunk boundary", strings.Repeat("a", 512), 300 * time.Millisecond, 300 * time.Millisecond},
		{"second chunk", strings.Repeat("a", 513), 300 * time.Millisecond, 1150 * time.Millisecond},
		{"capped burst", strings.Repeat("a", 10_000), 300 * time.Millisecond, 2 * time.Second},
		{"configured longer delay", "hello\nworld", 3 * time.Second, 3 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ForMessage(tt.message, tt.configured); got != tt.want {
				t.Fatalf("ForMessage() = %s, want %s", got, tt.want)
			}
		})
	}
}
