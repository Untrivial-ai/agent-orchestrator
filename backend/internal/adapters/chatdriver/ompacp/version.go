package ompacp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minimumOMPVersion is the first tagged OMP release that contains the native
// `omp acp` command. Older builds exposed a different RPC mode whose session,
// approval, and replay semantics are not treated as interchangeable.
const minimumOMPVersion = "15.0.0"

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func versionProbe(ctx context.Context, bin string) error {
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read OMP version: %w", err)
	}
	return validateVersionOutput(string(output))
}

func validateVersionOutput(output string) error {
	installed, ok := parseVersion(output)
	if !ok {
		return fmt.Errorf("unrecognized OMP version %q (AO requires %s or newer)",
			strings.TrimSpace(output), minimumOMPVersion)
	}
	minimum, _ := parseVersion(minimumOMPVersion)
	if installed.less(minimum) {
		return fmt.Errorf("OMP %s is older than AO's tested minimum %s",
			installed, minimumOMPVersion)
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
