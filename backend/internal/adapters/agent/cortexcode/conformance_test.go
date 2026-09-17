package cortexcode

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const pinnedCortexCodeRelease = "1.1.87+175514.04f5c9114e11"

func TestPinnedHelpFixtureFailsRequiredContract(t *testing.T) {
	help, err := os.ReadFile("testdata/help.txt")
	if err != nil {
		t.Fatal(err)
	}

	err = validateCLIContract("Cortex Code v0.26.0916", string(help))
	if err == nil {
		t.Fatal("validateCLIContract unexpectedly admitted a release without permission mediation and append-only system instructions")
	}
	for _, missing := range []string{"--permission-prompt-tool", "append-only system instructions"} {
		if !strings.Contains(err.Error(), missing) {
			t.Fatalf("error %q does not identify missing %q", err, missing)
		}
	}
}

func TestContractRejectsUnknownVersion(t *testing.T) {
	help := requiredContractHelp("--append-system-prompt")
	if err := validateCLIContract("Cortex Code development", help); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("validateCLIContract error = %v, want unsupported version", err)
	}
}

func TestContractRejectsReplaceOnlySystemPrompt(t *testing.T) {
	help := requiredContractHelp("--system-prompt")
	err := validateCLIContract("Cortex Code v0.26.0916", help)
	if err == nil || !strings.Contains(err.Error(), "append-only system instructions") {
		t.Fatalf("validateCLIContract error = %v, want append-only system instruction failure", err)
	}
}

func TestInstalledCortexContract(t *testing.T) {
	if os.Getenv("AO_CORTEX_CODE_E2E") != "1" {
		t.Skip("set AO_CORTEX_CODE_E2E=1 to validate an installed Cortex Code release")
	}
	binary, err := exec.LookPath("cortex")
	if err != nil {
		t.Fatal("AO_CORTEX_CODE_E2E=1 but cortex is not on PATH")
	}
	version := runCortex(t, binary, "--version")
	help := runCortex(t, binary, "--help")
	if err := validateCLIContract(version, help); err != nil {
		t.Fatalf("installed Cortex Code does not satisfy AO's admission contract: %v", err)
	}
}

func requiredContractHelp(systemFlag string) string {
	return strings.Join([]string{
		"--input-format stream-json",
		"--output-format stream-json",
		"--permission-prompt-tool stdio",
		"--resume",
		"--model",
		systemFlag,
	}, "\n")
}

func runCortex(t *testing.T, binary string, args ...string) string {
	t.Helper()
	output, err := exec.Command(binary, args...).CombinedOutput() //nolint:gosec // opt-in test executes the explicitly resolved official CLI
	if err != nil {
		t.Fatalf("cortex %s failed: %v", strings.Join(args, " "), err)
	}
	return string(output)
}
