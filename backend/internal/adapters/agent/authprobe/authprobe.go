package authprobe

import (
	"context"
	"os"
	"runtime"
	"sort"
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

// ScopedCmdRunner runs a native auth command in the effective invocation
// scope. The function type keeps adapter probes injectable without mutable
// package globals.
type ScopedCmdRunner func(context.Context, ports.AgentAuthCheck, string, ...string) ([]byte, error)

// RunScopedCommand executes an argument vector directly, without a shell, in
// the supplied workspace and with launch overrides applied to the inherited
// environment. Callers own the command timeout and output parser.
func RunScopedCommand(ctx context.Context, check ports.AgentAuthCheck, name string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, name, args...)
	if check.WorkingDir != "" {
		cmd.Dir = check.WorkingDir
	}
	cmd.Env = scopedEnvironment(os.Environ(), check.Env, runtime.GOOS == "windows")
	return cmd.CombinedOutput()
}

func scopedEnvironment(inherited []string, overrides map[string]string, caseInsensitive bool) []string {
	merged := make(map[string]string, len(inherited)+len(overrides))
	for _, entry := range inherited {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if caseInsensitive {
			key = strings.ToUpper(key)
		}
		merged[key] = entry
	}
	for key, value := range overrides {
		lookup := key
		if caseInsensitive {
			lookup = strings.ToUpper(lookup)
		}
		merged[lookup] = key + "=" + value
	}
	out := make([]string, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry)
	}
	sort.Strings(out)
	return out
}

// CLIStatus runs bounded local CLI probes and recognizes explicit negative output.
// Positive authorization requires an adapter-specific parser.
// Callers must pass adapter-specific commands; catalog refresh should not run
// a generic sequence of auth-like commands against every installed binary.
func CLIStatus(ctx context.Context, binary string, commands [][]string) (ports.AgentAuthStatus, error) {
	return CLIStatusWithTimeout(ctx, binary, commands, 3*time.Second)
}

// CLIStatusWithTimeout is CLIStatus with an adapter-specific per-command
// timeout for CLIs whose native status command has documented startup work.
func CLIStatusWithTimeout(ctx context.Context, binary string, commands [][]string, timeout time.Duration) (ports.AgentAuthStatus, error) {
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
		status, err := commandStatus(ctx, binary, args, timeout)
		if err != nil {
			return ports.AgentAuthStatusUnknown, err
		}
		if status != ports.AgentAuthStatusUnknown {
			return status, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

func commandStatus(ctx context.Context, binary string, args []string, timeout time.Duration) (ports.AgentAuthStatus, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := CmdRunner(probeCtx, binary, args...)
	if probeCtx.Err() != nil {
		if probeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
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

// StatusFromText recognizes explicit negative CLI auth/status output.
// Generic positive phrases and fields are not proof of validated authorization.
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
