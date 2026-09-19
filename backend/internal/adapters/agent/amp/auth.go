package amp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
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

// AuthStatus checks the device-wide invocation using the scoped resolver.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor probes Amp with the effective environment and settings file.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return ampAuthStatus(ctx, binary, check, authutil.Dependencies{})
}

func ampAuthStatus(ctx context.Context, binary string, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	// A native rejection applies to the effective invocation, even if a local
	// key is present. Inconclusive probes may fall back to local configuration.
	if status, err := ampUsageAuthStatus(ctx, binary, check, d); err != nil || status != ports.AgentAuthStatusUnknown {
		return status, err
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	d.Getenv = func(key string) string {
		if value, exists := check.Env[key]; exists {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(getenv(key))
	}
	// Settings access tokens have this documented prefix. Browser session
	// tokens expire within an hour and cannot refresh from AMP_API_KEY.
	if ampAccessToken.MatchString(d.Getenv("AMP_API_KEY")) {
		return ports.AgentAuthStatusConfigured, nil
	}
	home := d.Getenv("HOME")
	if d.GOOS == "windows" || (d.GOOS == "" && runtime.GOOS == "windows") {
		home = d.Getenv("USERPROFILE")
	}
	if !filepath.IsAbs(home) {
		return ports.AgentAuthStatusUnknown, nil
	}
	server := "https://ampcode.com/"
	settingsPath := d.Getenv("AMP_SETTINGS_FILE")
	if value := ampSettingsFlag(check.Args); value != "" {
		settingsPath = value
	}
	paths := []string{filepath.Join(home, ".config", "amp", "settings.json"), filepath.Join(home, ".config", "amp", "settings.jsonc")}
	if settingsPath != "" {
		if strings.HasPrefix(settingsPath, "~/") {
			settingsPath = filepath.Join(home, settingsPath[2:])
		}
		if !filepath.IsAbs(settingsPath) {
			if !filepath.IsAbs(check.WorkingDir) {
				return ports.AgentAuthStatusUnknown, nil
			}
			settingsPath = filepath.Join(check.WorkingDir, settingsPath)
		}
		paths = []string{settingsPath}
	}
	for _, path := range paths {
		data, err := authutil.ReadFile(ctx, d, path)
		if err != nil {
			continue
		}
		var settings struct {
			URL string `json:"amp.url"`
		}
		if json.Unmarshal(ampSettingsJSON(data), &settings) != nil {
			continue
		}
		if settings.URL != "" {
			server = settings.URL
		}
		break
	}
	if override := d.Getenv("AMP_URL"); override != "" {
		server = override
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Amp canonicalizes its default server with a trailing slash. Custom
	// servers retain their exact configured URL in the credential-store key.
	if server == "https://ampcode.com" {
		server += "/"
	}
	var secrets map[string]json.RawMessage
	if authutil.ReadJSON(ctx, d, filepath.Join(home, ".local", "share", "amp", "secrets.json"), &secrets) == nil {
		var key string
		if json.Unmarshal(secrets["apiKey@"+server], &key) == nil && strings.TrimSpace(key) != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

var ampAccessToken = regexp.MustCompile(`^sgamp_[A-Za-z0-9_-]+$`)

func ampSettingsFlag(args []string) string {
	value := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if strings.HasPrefix(args[i], "--settings-file=") {
			value = strings.TrimPrefix(args[i], "--settings-file=")
		}
		if args[i] == "--settings-file" && i+1 < len(args) {
			i++
			value = args[i]
		}
	}
	return value
}

func ampUsageAuthStatus(ctx context.Context, binary string, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
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
			output := &ampProbeOutput{}
			cmd.Stdout, cmd.Stderr = output, output
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			if output.exceeded {
				return nil, errors.New("amp auth output exceeds limit")
			}
			return output.data, err
		}
	}
	args := []string{"usage", "--no-color"}
	if settings := ampSettingsFlag(check.Args); settings != "" {
		args = append(args, "--settings-file", settings)
	}
	out, err := run(probeCtx, binary, args...)
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if probeCtx.Err() != nil || len(out) > authutil.MaxFileSize {
		return ports.AgentAuthStatusUnknown, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(strings.TrimPrefix(line, "Error: "))
		if lower == "invalid api key" || lower == "not signed in" || strings.HasPrefix(lower, "not signed in. ") ||
			lower == "authentication required" || strings.HasPrefix(lower, "authentication required. ") {
			return ports.AgentAuthStatusUnauthorized, nil
		}
	}
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Signed in as ") && strings.TrimSpace(strings.TrimPrefix(line, "Signed in as ")) != "" {
				return ports.AgentAuthStatusAuthorized, nil
			}
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

type ampProbeOutput struct {
	data     []byte
	exceeded bool
}

func (b *ampProbeOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := authutil.MaxFileSize - len(b.data); n > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.exceeded = true
	} else {
		b.data = append(b.data, p...)
	}
	return n, nil
}

// Amp accepts JSONC settings. Comments and trailing commas are stripped only
// outside strings; encoding/json still validates the complete settings object.
func ampSettingsJSON(data []byte) []byte {
	out := append([]byte(nil), data...)
	quoted := false
	for i := 0; i < len(out); i++ {
		if quoted {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				quoted = false
			}
			continue
		}
		if out[i] == '"' {
			quoted = true
			continue
		}
		if out[i] != '/' || i+1 >= len(out) {
			continue
		}
		switch out[i+1] {
		case '/':
			for ; i < len(out) && out[i] != '\n' && out[i] != '\r'; i++ {
				out[i] = ' '
			}
		case '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			for ; i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/'); i++ {
				out[i] = ' '
			}
			if i+1 >= len(out) {
				return nil
			}
			out[i], out[i+1] = ' ', ' '
			i++
		}
	}
	quoted = false
	for i := 0; i < len(out); i++ {
		if quoted {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				quoted = false
			}
			continue
		}
		if out[i] == '"' {
			quoted = true
			continue
		}
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && strings.ContainsRune(" \n\r\t", rune(out[j])) {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				out[i] = ' '
			}
		}
	}
	return out
}
