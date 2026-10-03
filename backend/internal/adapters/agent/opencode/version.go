package opencode

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var versionPattern = regexp.MustCompile(`^(?:opencode\s+)?v?([0-9]+)\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?$`)

// IncompatibleVersionError reports that the selected OpenCode executable is
// installed, but its major version does not match the selected harness.
type IncompatibleVersionError struct {
	ExpectedMajor int
	FoundMajor    int
	FoundVersion  string
	Path          string
}

func (e *IncompatibleVersionError) Error() string {
	return fmt.Sprintf("opencode: selected harness requires OpenCode %d, but %q reports OpenCode %d (%s); select the matching harness or put OpenCode %d on PATH", e.ExpectedMajor, e.Path, e.FoundMajor, e.FoundVersion, e.ExpectedMajor)
}

// ResolveBinaryForMajor resolves and probes one executable. It deliberately
// does not search past an incompatible PATH selection or cache a result across
// attempts: both official majors use the same executable name.
func ResolveBinaryForMajor(ctx context.Context, major int) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	binary, err := ResolveOpenCodeBinary(probeCtx)
	if err != nil {
		return "", err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return "", err
	}
	cmd := aoprocess.CommandContext(probeCtx, binary, "--version")
	// A wrapper may leave descendants holding stdout open after cancellation.
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if probeCtx.Err() != nil {
		return "", fmt.Errorf("opencode: version probe for %q: %w", binary, probeCtx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("opencode: version probe for %q failed: %w", binary, err)
	}
	version := strings.TrimSpace(string(out))
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return "", fmt.Errorf("opencode: cannot determine version of %q", binary)
	}
	found, err := strconv.Atoi(match[1])
	if err != nil {
		return "", fmt.Errorf("opencode: cannot determine version of %q", binary)
	}
	if found != major {
		return "", &IncompatibleVersionError{
			ExpectedMajor: major,
			FoundMajor:    found,
			FoundVersion:  version,
			Path:          binary,
		}
	}
	return binary, nil
}
