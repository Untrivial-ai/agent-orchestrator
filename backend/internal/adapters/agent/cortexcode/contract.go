// Package cortexcode contains the admission gate for Snowflake Cortex Code.
//
// Cortex Code is intentionally not registered as an AO agent until an official
// release satisfies every required launch and lifecycle capability. Keeping
// this check executable prevents a partial integration from silently claiming
// worker, orchestrator, restore, or Chat support.
package cortexcode

import (
	"fmt"
	"strings"
)

const minimumCortexCodeVersion = "Cortex Code v0.26.0916"

type cliContract struct {
	requiredFlags               []string
	appendSystemInstructionFlag string
}

// cortexCLIContract records only flags verified in the pinned official
// release. The empty append-system field is deliberate: stable artifact
// 1.1.87+175514.04f5c9114e11 has no such option and therefore cannot be
// admitted. Do not fill this field from SDK behavior or an inferred flag.
var cortexCLIContract = cliContract{
	requiredFlags: []string{
		"--input-format",
		"--output-format",
		"--permission-prompt-tool",
		"--resume",
		"--model",
	},
}

func validateCLIContract(version, help string) error {
	var failures []string
	if strings.TrimSpace(version) != minimumCortexCodeVersion {
		failures = append(failures, fmt.Sprintf("unsupported version %q (pinned %q)", strings.TrimSpace(version), minimumCortexCodeVersion))
	}
	for _, flag := range cortexCLIContract.requiredFlags {
		if !strings.Contains(help, flag) {
			failures = append(failures, "missing "+flag)
		}
	}
	appendFlag := strings.TrimSpace(cortexCLIContract.appendSystemInstructionFlag)
	if appendFlag == "" || !strings.Contains(help, appendFlag) {
		failures = append(failures, "missing verified append-only system instructions")
	}
	if len(failures) != 0 {
		return fmt.Errorf("cortex code contract rejected: %s", strings.Join(failures, "; "))
	}
	return nil
}
