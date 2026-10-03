package opencode

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var versionPattern = regexp.MustCompile(`^(?:opencode\s+)?v?([0-9]+)\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?$`)

const versionProbeTimeout = 10 * time.Second

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

// ResolveBinaryForMajor returns the opencode executable whose major version
// matches. OpenCode 1 and 2 share an executable name and can be installed side
// by side, so every candidate is probed in resolution order. Results are never
// cached across attempts. When none match, the first candidate's error is
// returned so a mismatch still reports what was found.
func ResolveBinaryForMajor(ctx context.Context, major int) (string, error) {
	candidates, err := BinaryCandidates(ctx)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("opencode: %w", ports.ErrAgentBinaryNotFound)
	}
	var firstErr error
	for _, candidate := range candidates {
		binary, err := probeBinaryMajor(ctx, candidate, major)
		if err == nil {
			return binary, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", firstErr
}

func probeBinaryMajor(ctx context.Context, binary string, major int) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	binary, err := filepath.Abs(binary)
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
