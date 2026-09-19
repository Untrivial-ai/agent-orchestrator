package conpty

import (
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestWindowsCommandLineUTF16LenEscapesQuotesAndBackslashes(t *testing.T) {
	args := []string{`C:\Program Files\AO\ao.exe`, "-i", `say "hello"`, `trailing slash \`}
	const escaped = `"C:\Program Files\AO\ao.exe" -i "say \"hello\"" "trailing slash \\"`

	if got := windowsCommandLine(args); got != escaped {
		t.Fatalf("windowsCommandLine() = %q, want %q", got, escaped)
	}
	want := len(escaped) + 1
	if got := windowsCommandLineUTF16Len(args); got != want {
		t.Fatalf("windowsCommandLineUTF16Len() = %d, want %d for %q", got, want, escaped)
	}

	if got, want := windowsCommandLineUTF16Len([]string{"qwen", "🙂"}), 8; got != want {
		t.Fatalf("windowsCommandLineUTF16Len() with surrogate pair = %d, want %d", got, want)
	}
}

func TestValidateWindowsCommandLineQuoteBoundary(t *testing.T) {
	atLimit := []string{"qwen", "-i", strings.Repeat(`"`, 16379)}
	if got := windowsCommandLineUTF16Len(atLimit); got != maxWindowsCommandLineUTF16 {
		t.Fatalf("at-limit command length = %d, want %d", got, maxWindowsCommandLineUTF16)
	}
	if err := validateWindowsCommandLine(atLimit); err != nil {
		t.Fatalf("validateWindowsCommandLine(at limit) error = %v", err)
	}

	overLimit := []string{"qwen", "-i", strings.Repeat(`"`, 16380)}
	err := validateWindowsCommandLine(overLimit)
	if !errors.Is(err, ports.ErrRuntimeCommandLineTooLong) {
		t.Fatalf("validateWindowsCommandLine(over limit) error = %v, want ErrRuntimeCommandLineTooLong", err)
	}
}
