package kimchiacp

import (
	"testing"
)

func TestParseSemver(t *testing.T) {
	tests := map[string]semver{
		"kimchi 0.0.7":         {0, 0, 7},
		"kimchi version 1.2.3": {1, 2, 3},
		"kimchi 0.10.0":        {0, 10, 0},
		"  0.1.0-rc1 ":         {0, 1, 0},
	}
	for input, want := range tests {
		got, ok := parseSemver(input)
		if !ok {
			t.Errorf("parseSemver(%q): ok=false, want %v", input, want)
			continue
		}
		if got != want {
			t.Errorf("parseSemver(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestParseSemverFailsOnGarbage(t *testing.T) {
	bad := []string{"", "kimchi", "no version here", "0.0", "v1"}
	for _, input := range bad {
		if _, ok := parseSemver(input); ok {
			t.Errorf("parseSemver(%q): ok=true, want false", input)
		}
	}
}

func TestSemverLess(t *testing.T) {
	tests := []struct {
		name string
		v, w semver
		want bool
	}{
		{"0.0.6 < 0.0.7", semver{0, 0, 6}, semver{0, 0, 7}, true},
		{"0.0.7 < 0.0.7", semver{0, 0, 7}, semver{0, 0, 7}, false},
		{"0.1.0 < 0.0.7", semver{0, 1, 0}, semver{0, 0, 7}, false},
		{"0.0.7 < 0.1.0", semver{0, 0, 7}, semver{0, 1, 0}, true},
		{"1.0.0 < 0.99.99", semver{1, 0, 0}, semver{0, 99, 99}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.less(tt.w); got != tt.want {
				t.Errorf("less = %v, want %v", got, tt.want)
			}
		})
	}
}
