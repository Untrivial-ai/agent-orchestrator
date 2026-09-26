package cli

import (
	"os"
	"strings"
)

// Tests inherit the environment of the process that launched them. When the
// suite runs from inside an AO-managed session, the daemon exports leak in
// (AO_*, CLAUDE_CODE_*, ANTHROPIC_*): hooks start routing to the live session,
// reviewer permission decisions see a review session id, and transports pick
// up a real upstream base URL, so dozens of hook tests fail. CI never sees
// this because its environment is clean. Scrub the inherited prefixes once
// before any test runs; tests that need these variables set them explicitly
// via t.Setenv.
func init() {
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "AO_") || strings.HasPrefix(key, "CLAUDE_CODE_") || strings.HasPrefix(key, "ANTHROPIC_") {
			_ = os.Unsetenv(key)
		}
	}
}
