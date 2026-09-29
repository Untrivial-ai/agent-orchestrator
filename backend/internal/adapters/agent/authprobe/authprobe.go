package authprobe

import (
	"context"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// CmdRunner runs the command and returns the combined stdout/stderr.
// It is exposed as a package variable to allow mocking in tests.
var CmdRunner = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
	return aoprocess.CommandContext(ctx, name, arg...).CombinedOutput()
}

// CLIStatus runs bounded local CLI probes and classifies their output.
// Callers must pass adapter-specific commands; catalog refresh should not run
// a generic sequence of auth-like commands against every installed binary.
func CLIStatus(ctx context.Context, binary string, commands [][]string) (ports.AgentAuthStatus, error) {
	return CLIStatusWithTimeout(ctx, binary, commands, 3*time.Second)
}

// Classifier reads one probe's raw output. The bool reports whether it
// recognized the shape at all; false defers to the shared prose classifier.
type Classifier func(out []byte) (ports.AgentAuthStatus, bool)

// CLIStatusWithClassifier is CLIStatus for a CLI whose status output the shared
// prose classifier cannot read — structured output, or prose wrapped around it.
// The adapter's classifier is consulted first and the shared one remains the
// fallback, so an unrecognized shape still gets the ordinary treatment.
func CLIStatusWithClassifier(ctx context.Context, binary string, commands [][]string, classify Classifier) (ports.AgentAuthStatus, error) {
	return cliStatus(ctx, binary, commands, 3*time.Second, classify)
}

// CLIStatusWithTimeout is CLIStatus with an adapter-specific per-command
// timeout for CLIs whose native status command has documented startup work.
func CLIStatusWithTimeout(ctx context.Context, binary string, commands [][]string, timeout time.Duration) (ports.AgentAuthStatus, error) {
	return cliStatus(ctx, binary, commands, timeout, nil)
}

func cliStatus(ctx context.Context, binary string, commands [][]string, timeout time.Duration, classify Classifier) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	if len(commands) == 0 {
		return ports.AgentAuthStatusUnknown, nil
	}
	for _, args := range commands {
		status, err := commandStatus(ctx, binary, args, timeout, classify)
		if err != nil {
			return ports.AgentAuthStatusUnknown, err
		}
		if status != ports.AgentAuthStatusUnknown {
			return status, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

func commandStatus(ctx context.Context, binary string, args []string, timeout time.Duration, classify Classifier) (ports.AgentAuthStatus, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := CmdRunner(probeCtx, binary, args...)
	if probeCtx.Err() != nil {
		if probeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	if classify != nil {
		if status, ok := classify(out); ok {
			return status, nil
		}
	}
	status := StatusFromText(string(out))
	if status != ports.AgentAuthStatusUnknown {
		return status, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

// StatusFromText classifies common CLI auth/status output.
func StatusFromText(out string) ports.AgentAuthStatus {
	text := strings.ToLower(out)
	compactText := compact(text)
	if hasAny(text,
		"not logged in",
		"not currently logged in",
		"logged out",
		"not authenticated",
		"unauthenticated",
		"authentication required",
		"not authorized",
		"unauthorized",
		"login required",
		"no credentials",
		"0 credentials",
		"no api key",
		"no token",
		`"loggedin": false`,
		`"loggedin":false`,
	) || hasAny(compactText,
		// A null account is a CLI saying "nobody is signed in" in JSON rather
		// than prose. Kiro's `whoami --format json` answers exactly this when
		// signed out, and matched none of the phrases above.
		`"account":null`,
		`'account':null`,
		"account:null",
		`"authenticated":false`,
		`'authenticated':false`,
		"authenticated:false",
		"authenticated=false",
		`"authorized":false`,
		`'authorized':false`,
		"authorized:false",
		"authorized=false",
		`"logged_in":false`,
		`'logged_in':false`,
		"logged_in:false",
		"logged_in=false",
		`"loggedin":false`,
		`'loggedin':false`,
		"loggedin:false",
		"loggedin=false",
	) {
		return ports.AgentAuthStatusUnauthorized
	}
	if hasAny(text,
		"logged in",
		"authenticated",
		"authorized",
		"token valid",
		"api key found",
		"credentials found",
		`"loggedin": true`,
		`"loggedin":true`,
	) {
		return ports.AgentAuthStatusAuthorized
	}
	return ports.AgentAuthStatusUnknown
}

func compact(text string) string {
	return strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(text)
}

func hasAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
