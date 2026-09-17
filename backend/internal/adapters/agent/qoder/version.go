package qoder

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func ProbeMinimumVersion(ctx context.Context, bin string) error {
	out, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read Qoder version: %w", err)
	}
	return validateVersionOutput(string(out))
}

func validateVersionOutput(output string) error {
	installed, ok := parseVersion(output)
	if !ok {
		return fmt.Errorf("unrecognized Qoder version %q (AO requires %s or newer)", strings.TrimSpace(output), minimumQoderVersion)
	}
	minimum, _ := parseVersion(minimumQoderVersion)
	for i := range installed {
		if installed[i] != minimum[i] {
			if installed[i] < minimum[i] {
				return fmt.Errorf("Qoder %s is older than AO's tested minimum %s", strings.TrimSpace(output), minimumQoderVersion)
			}
			break
		}
	}
	return nil
}

func parseVersion(s string) ([3]int, bool) {
	match := versionPattern.FindStringSubmatch(s)
	if len(match) != 4 {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
