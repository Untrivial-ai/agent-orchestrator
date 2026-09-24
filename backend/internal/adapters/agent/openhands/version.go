package openhands

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minVersion is the first OpenHands CLI release that loads .openhands/hooks.json
// (OpenHands-CLI#428, shipped in 1.12.0), which AO needs for native session
// identity and activity.
var minVersion = [3]int{1, 12, 0}

// versionProbeTimeout covers a cold Python start; a warm `openhands --version`
// already takes several seconds.
const versionProbeTimeout = 30 * time.Second

// versionPattern anchors on the CLI's own banner. The SDK prints a separate
// "OpenHands SDK vX.Y.Z" banner on stderr, which must not be mistaken for the
// CLI version; matching the product name also confirms binary identity.
var versionPattern = regexp.MustCompile(`OpenHands CLI (\d+)\.(\d+)\.(\d+)`)

// versionCommand is injectable so tests never launch a real CLI.
var versionCommand = func(ctx context.Context, binary string) ([]byte, error) {
	return aoprocess.CommandContext(ctx, binary, "--version").Output()
}

func supportedVersion(output string) bool {
	match := versionPattern.FindStringSubmatch(output)
	if match == nil {
		return false
	}
	for i, want := range minVersion {
		got, err := strconv.Atoi(match[i+1])
		if err != nil {
			return false
		}
		if got != want {
			return got > want
		}
	}
	return true
}

func checkVersion(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	output, err := versionCommand(ctx, binary)
	if err != nil {
		return fmt.Errorf("openhands: check CLI version: %w", err)
	}
	if !supportedVersion(string(output)) {
		return fmt.Errorf("openhands: OpenHands CLI %d.%d.%d or newer is required", minVersion[0], minVersion[1], minVersion[2])
	}
	return nil
}
