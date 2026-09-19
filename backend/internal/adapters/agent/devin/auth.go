package devin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus checks device-wide defaults using the scoped resolver.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks credentials for the effective Devin invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return devinAuthStatus(ctx, binary, check, authutil.Dependencies{})
}

var devinAPIKey = regexp.MustCompile(`^cog_[A-Za-z0-9_-]+$`)

func devinAuthStatus(ctx context.Context, binary string, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	status, err := devinNativeStatus(ctx, binary, check, d)
	if err != nil || status != ports.AgentAuthStatusUnknown {
		return status, err
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	d.Getenv = func(key string) string {
		if value, ok := check.Env[key]; ok {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(getenv(key))
	}
	// WINDSURF_API_KEY is an ACP input, not a normal CLI-login input. A
	// positional prompt or model named "acp" does not select that subcommand.
	args := check.Args
	if len(args) > 0 && (args[0] == binary || filepath.Base(args[0]) == "devin" || filepath.Base(args[0]) == "devin.exe") {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "acp" && d.Getenv("WINDSURF_API_KEY") != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	// DEVIN_API_KEY is documented for API/handoff. It is only local evidence,
	// never a reason to skip or override the native login status.
	if devinAPIKey.MatchString(d.Getenv("DEVIN_API_KEY")) {
		return ports.AgentAuthStatusConfigured, nil
	}
	root := d.Getenv("XDG_DATA_HOME")
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		root = d.Getenv("APPDATA")
		if !filepath.IsAbs(root) {
			home := d.Getenv("USERPROFILE")
			if !filepath.IsAbs(home) {
				return ports.AgentAuthStatusUnknown, nil
			}
			root = filepath.Join(home, "AppData", "Roaming")
		}
	} else if !filepath.IsAbs(root) {
		home := d.Getenv("HOME")
		if !filepath.IsAbs(home) {
			return ports.AgentAuthStatusUnknown, nil
		}
		root = filepath.Join(home, ".local", "share")
	}
	// windsurf_api_key is the concrete credentials.toml field present in the
	// repository fixture introduced by da82acf2c. URL/host fields are not secrets;
	// unobserved credential fields must not be guessed.
	var credentials struct {
		WindsurfAPIKey string `toml:"windsurf_api_key"`
	}
	if authutil.ReadTOML(ctx, d, filepath.Join(root, "devin", "credentials.toml"), &credentials) == nil && strings.TrimSpace(credentials.WindsurfAPIKey) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func devinNativeStatus(ctx context.Context, binary string, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := d.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, name, args...)
			cmd.Dir = check.WorkingDir
			cmd.Env = os.Environ()
			for key, value := range check.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output := &devinProbeOutput{}
			cmd.Stdout, cmd.Stderr = output, output
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			if output.exceeded {
				return nil, errors.New("devin status output exceeds limit")
			}
			return output.data, err
		}
	}
	out, err := run(probeCtx, binary, "auth", "status")
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if probeCtx.Err() != nil || len(out) > authutil.MaxFileSize {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Match the native command's complete response, never incidental phrases.
	switch strings.TrimSpace(string(out)) {
	case "You are not logged in.":
		return ports.AgentAuthStatusUnauthorized, nil
	case "Logged in (via Devin).":
		if err == nil {
			return ports.AgentAuthStatusAuthorized, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

type devinProbeOutput struct {
	data     []byte
	exceeded bool
}

func (b *devinProbeOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := authutil.MaxFileSize - len(b.data); n > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.exceeded = true
	} else {
		b.data = append(b.data, p...)
	}
	return n, nil
}
