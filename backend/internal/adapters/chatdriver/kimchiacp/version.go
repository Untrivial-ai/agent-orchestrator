package kimchiacp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minimumKimchiVersion is the oldest Kimchi build known to support ACP mode.
// Kimchi 0.0.7 added the --mode acp flag and the minimal ACP server.
const minimumKimchiVersion = "0.0.7"

var semverPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// versionProbe checks that the resolved Kimchi binary is at least
// minimumKimchiVersion. It runs `kimchi --version`, parses the semver
// triplet, and returns an error if the binary is too old or the version
// output is unparseable.
func versionProbe(ctx context.Context, bin string) error {
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read Kimchi version: %w", err)
	}
	installed, ok := parseSemver(string(output))
	if !ok {
		return fmt.Errorf("unrecognized Kimchi version %q (AO requires %s or newer)",
			strings.TrimSpace(string(output)), minimumKimchiVersion)
	}
	minimum, _ := parseSemver(minimumKimchiVersion)
	if installed.less(minimum) {
		return fmt.Errorf("kimchi %s is older than AO's tested minimum %s",
			installed, minimumKimchiVersion)
	}
	return nil
}

type semver [3]int

func parseSemver(output string) (semver, bool) {
	match := semverPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return semver{}, false
	}
	var version semver
	for i := range version {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return semver{}, false
		}
		version[i] = value
	}
	return version, true
}

func (v semver) less(other semver) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v semver) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
