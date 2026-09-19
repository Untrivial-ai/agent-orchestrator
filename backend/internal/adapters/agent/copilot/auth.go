package copilot

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus reports local configuration, without claiming provider validation.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks credentials for the effective Copilot invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return copilotAuthStatus(ctx, scope, authutil.Dependencies{})
}

var copilotTokenEnvVars = []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}

type copilotEnvRunner func(context.Context, string, []string, map[string]string) ([]byte, error)

func copilotLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := copilotAuthStatus(ctx, ports.AgentAuthCheck{}, authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

func copilotAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies, runners ...copilotEnvRunner) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseEnv := d.Getenv
	if baseEnv == nil {
		baseEnv = os.Getenv
	}
	env := func(key string) string {
		if value, ok := scope.Env[key]; ok {
			return value
		}
		return baseEnv(key)
	}
	// BYOK selects the model provider independently of GitHub credentials.
	if endpoint := strings.TrimSpace(env("COPILOT_PROVIDER_BASE_URL")); endpoint != "" {
		return copilotBYOKStatus(scope, endpoint, env), nil
	}
	for _, key := range copilotTokenEnvVars {
		if token := strings.TrimSpace(env(key)); token != "" {
			if copilotUsableToken(token) {
				return ports.AgentAuthStatusConfigured, nil
			}
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	if d.Run == nil || len(runners) > 0 {
		commandEnv := make(map[string]string)
		if d.Getenv == nil {
			for _, entry := range os.Environ() {
				key, value, _ := strings.Cut(entry, "=")
				commandEnv[key] = value
			}
		} else {
			for _, key := range []string{"HOME", "USERPROFILE", "PATH", "XDG_CONFIG_HOME", "GH_CONFIG_DIR", "GH_HOST", "GH_TOKEN", "GITHUB_TOKEN"} {
				commandEnv[key] = baseEnv(key)
			}
		}
		for key, value := range scope.Env {
			commandEnv[key] = value
		}
		run := copilotRunWithEnv
		if len(runners) > 0 {
			run = runners[0]
		}
		d.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return run(ctx, name, args, commandEnv)
		}
	}
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	dir := strings.TrimSpace(env("COPILOT_HOME"))
	if dir == "" {
		homeKey := "HOME"
		if d.GOOS == "windows" {
			homeKey = "USERPROFILE"
		}
		if home := strings.TrimSpace(env(homeKey)); home != "" {
			dir = filepath.Join(home, ".copilot")
		}
	}
	if dir != "" && !filepath.IsAbs(dir) && scope.WorkingDir != "" {
		dir = filepath.Join(scope.WorkingDir, dir)
	}
	var config copilotAuthConfig
	if d.GOOS == "darwin" && dir != "" {
		// Native Copilot stores host/login in last_logged_in_user and uses
		// exactly host:login as the account under the fixed copilot-cli service.
		// Metadata selects the item; it does not itself establish credentials.
		if authutil.ReadJSON(ctx, d, filepath.Join(dir, "config.json"), &config) == nil {
			if account := config.selectedAccount(); account != "" {
				out, err := authutil.GenericPassword(ctx, d, "copilot-cli", account)
				if ctx.Err() != nil {
					return ports.AgentAuthStatusUnknown, ctx.Err()
				}
				if err == nil && copilotUsableToken(string(out)) {
					return ports.AgentAuthStatusConfigured, nil
				}
			}
		}
	}
	out, err := authutil.RunCommand(ctx, d, "gh", "auth", "token")
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if err == nil && copilotUsableToken(string(out)) {
		return ports.AgentAuthStatusConfigured, nil
	}
	if dir == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	status, _, _ := copilotConfigAuthStatusWith(ctx, filepath.Join(dir, "config.json"), d)
	return status, ctx.Err()
}

func copilotBYOKStatus(scope ports.AgentAuthCheck, endpoint string, env func(string) string) ports.AgentAuthStatus {
	model := strings.TrimSpace(env("COPILOT_MODEL"))
	if scope.Config.Model != "" {
		model = strings.TrimSpace(scope.Config.Model)
	}
	for i, arg := range scope.Args {
		if arg == "--model" && i+1 < len(scope.Args) {
			model = strings.TrimSpace(scope.Args[i+1])
		} else if strings.HasPrefix(arg, "--model=") {
			model = strings.TrimSpace(strings.TrimPrefix(arg, "--model="))
		}
	}
	address, err := url.Parse(endpoint)
	if err != nil || address.Hostname() == "" || address.User != nil || (address.Scheme != "http" && address.Scheme != "https") || model == "" {
		return ports.AgentAuthStatusUnknown
	}
	provider := strings.TrimSpace(env("COPILOT_PROVIDER_TYPE"))
	if provider == "" {
		provider = "openai"
	}
	if provider != "openai" && provider != "azure" && provider != "anthropic" {
		return ports.AgentAuthStatusUnknown
	}
	if strings.TrimSpace(env("COPILOT_PROVIDER_API_KEY")) != "" || strings.TrimSpace(env("COPILOT_PROVIDER_BEARER_TOKEN")) != "" {
		return ports.AgentAuthStatusConfigured
	}
	// The documented keyless example is local Ollama on its default port.
	host := address.Hostname()
	if provider == "openai" && address.Port() == "11434" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return ports.AgentAuthStatusNotApplicable
	}
	return ports.AgentAuthStatusUnknown
}

type copilotAuthUser struct {
	Host  string `json:"host"`
	Login string `json:"login"`
}

type copilotAuthConfig struct {
	LastUser *copilotAuthUser  `json:"last_logged_in_user"`
	Users    []copilotAuthUser `json:"logged_in_users"`
	Tokens   map[string]string `json:"copilot_tokens"`
}

func (c copilotAuthConfig) selectedAccount() string {
	if c.LastUser != nil {
		return copilotAccountKey(c.LastUser.Host, c.LastUser.Login)
	}
	if len(c.Users) > 0 {
		return copilotAccountKey(c.Users[0].Host, c.Users[0].Login)
	}
	return ""
}

func copilotAccountKey(host, login string) string {
	address, err := url.Parse(host)
	if err != nil || address.Scheme != "https" || address.Hostname() == "" || address.User != nil || address.Path != "" || address.RawQuery != "" || address.Fragment != "" || login == "" || len(login) > 100 {
		return ""
	}
	for _, char := range login {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
			return ""
		}
	}
	return host + ":" + login
}

func copilotConfigAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	return copilotConfigAuthStatusWith(context.Background(), path, authutil.Dependencies{})
}

func copilotConfigAuthStatusWith(ctx context.Context, path string, d authutil.Dependencies) (ports.AgentAuthStatus, bool, error) {
	var config copilotAuthConfig
	if err := authutil.ReadJSON(ctx, d, path, &config); err != nil {
		return ports.AgentAuthStatusUnknown, false, ctx.Err()
	}
	if config.LastUser != nil || len(config.Users) > 0 {
		account := config.selectedAccount()
		if account != "" && copilotUsableToken(config.Tokens[account]) {
			return ports.AgentAuthStatusConfigured, true, nil
		}
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for account, token := range config.Tokens {
		if split := strings.LastIndex(account, ":"); split > 0 && copilotAccountKey(account[:split], account[split+1:]) != "" && copilotUsableToken(token) {
			return ports.AgentAuthStatusConfigured, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func copilotUsableToken(value string) bool {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"gho_", "ghu_", "github_pat_"} {
		if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
			continue
		}
		for _, char := range strings.TrimPrefix(value, prefix) {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && (prefix != "github_pat_" || char != '_') {
				return false
			}
		}
		return true
	}
	return false
}

func copilotRunWithEnv(ctx context.Context, name string, args []string, env map[string]string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, name, args...)
	cmd.Env = make([]string, 0, len(env))
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output := &copilotCommandOutput{}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if output.exceeded {
		return nil, errors.New("credential command output exceeds limit")
	}
	return output.data, err
}

type copilotCommandOutput struct {
	data     []byte
	exceeded bool
}

func (b *copilotCommandOutput) Write(p []byte) (int, error) {
	remaining := authutil.MaxFileSize - len(b.data)
	if len(p) > remaining {
		b.exceeded = true
		b.data = append(b.data, p[:remaining]...)
	} else {
		b.data = append(b.data, p...)
	}
	return len(p), nil
}
