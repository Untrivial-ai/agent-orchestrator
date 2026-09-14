package qodercliacp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minimumQodercliVersion is the first release whose ACP surface reports the
// session mode ids AO maps onto (default/acceptEdits/auto/dontAsk/yolo). Older
// builds advertised a different set, so their approval semantics are not
// interchangeable with the ones this driver assumes.
const minimumQodercliVersion = "1.1.37"

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func versionProbe(ctx context.Context, bin string) error {
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read Qoder CLI version: %w", err)
	}
	return validateVersionOutput(string(output))
}

func validateVersionOutput(output string) error {
	installed, ok := parseVersion(output)
	if !ok {
		return fmt.Errorf("unrecognized Qoder CLI version %q (AO requires %s or newer)",
			strings.TrimSpace(output), minimumQodercliVersion)
	}
	minimum, _ := parseVersion(minimumQodercliVersion)
	if installed.less(minimum) {
		return fmt.Errorf("Qoder CLI %s is older than AO's tested minimum %s",
			installed, minimumQodercliVersion)
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
