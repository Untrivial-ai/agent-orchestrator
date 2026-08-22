package daytona

import "testing"

func TestEnvironmentKeyPattern(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"HOME", "AO_SESSION_ID", "_PRIVATE", "value9"} {
		if !environmentKeyPattern.MatchString(key) {
			t.Fatalf("valid environment key %q was rejected", key)
		}
	}
	for _, key := range []string{"", "9VALUE", "BAD KEY", "BAD-NAME", "NAME=value", "$(touch /tmp/bad)"} {
		if environmentKeyPattern.MatchString(key) {
			t.Fatalf("invalid environment key %q was accepted", key)
		}
	}
}
