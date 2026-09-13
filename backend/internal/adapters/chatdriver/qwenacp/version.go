package qwenacp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minimumQwenVersion is the oldest Qwen Code build known to expose native ACP
// as `qwen --acp`. Older builds either lacked ACP or only had the deprecated
// `--experimental-acp` alias.
const minimumQwenVersion = "0.15.0"

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func versionProbe(ctx context.Context, bin string) error {
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read Qwen Code version: %w", err)
	}
	return validateVersionOutput(string(output))
}

func validateVersionOutput(output string) error {
	installed, ok := parseVersion(output)
	if !ok {
		return fmt.Errorf("unrecognized Qwen Code version %q (AO requires %s or newer)",
			strings.TrimSpace(output), minimumQwenVersion)
	}
	minimum, _ := parseVersion(minimumQwenVersion)
	if installed.less(minimum) {
		return fmt.Errorf("installed Qwen Code %s is older than AO's tested minimum %s",
			installed, minimumQwenVersion)
	}
	return nil
}

type version [3]int

func parseVersion(output string) (version, bool) {
	match := versionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return version{}, false
	}
	var parsed version
	for i := range parsed {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return version{}, false
		}
		parsed[i] = value
	}
	return parsed, true
}

func (v version) less(other version) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v version) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
