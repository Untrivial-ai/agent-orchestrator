package gemini

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func supportedVersion(output string) bool {
	match := versionPattern.FindStringSubmatch(output)
	if match == nil {
		return false
	}
	parts := [3]int{}
	for i := range parts {
		parts[i], _ = strconv.Atoi(match[i+1])
	}
	return parts[0] > 0 || parts[1] >= 60
}

func checkVersion(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := aoprocess.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return fmt.Errorf("gemini: check CLI version: %w", err)
	}
	if !supportedVersion(string(output)) {
		return fmt.Errorf("gemini: CLI 0.60.0 or newer is required")
	}
	return nil
}
