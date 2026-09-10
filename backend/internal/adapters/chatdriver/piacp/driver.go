// Package piacp binds AO's Pi harness to a user-installed pi-acp adapter.
//
// AO does not package or download pi-acp. The adapter embeds Pi itself, while
// the existing Pi plugin remains the canonical auth probe. Both processes read
// the same PI_CODING_AGENT_DIR, provider credentials, and project resources.
package piacp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	distributionName = "@victor-software-house/pi-acp"
	minimumVersion   = "0.17.1"
)

type piPlugin interface {
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

type binaryResolver func(context.Context) (string, error)

// New constructs Pi's Chat-only driver. Pi intentionally does not implement
// AgentInterfaceHandoff: pi-acp's durable id is not yet proven identical to the
// terminal adapter's continuation id across both implementations.
func New(plugin piPlugin, log *slog.Logger) ports.ChatDriver {
	return acpdriver.New(buildConfig(plugin, ResolveBinary, log), log)
}

func buildConfig(plugin piPlugin, resolve binaryResolver, log *slog.Logger) acpdriver.Config {
	return acpdriver.Config{
		Harness: domain.HarnessPi,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityTools:     true,
			// pi-acp does not request ACP permissions and Pi executes tools without
			// permission popups. Keep approvals false so AO's production floor
			// refuses mutating Chat admission instead of presenting a permission
			// selector whose value the provider would silently ignore.
			ports.ChatCapabilityApprovals: false,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
			ports.ChatCapabilityUsage:     true,
			ports.ChatCapabilityDiffs:     true,
		},
		Probe: func(ctx context.Context) error {
			if _, err := resolve(ctx); err != nil {
				return fmt.Errorf("%w: pi-acp is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			status, err := plugin.AuthStatus(ctx)
			if err == nil && status == ports.AgentAuthStatusUnauthorized {
				return ports.ErrChatAuthRequired
			}
			if err != nil && log != nil {
				log.Debug("Pi auth probe inconclusive; continuing", "error", err)
			}
			return nil
		},
		Launch: func(ctx context.Context, cfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			binary, err := resolve(ctx)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: pi-acp is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			if !filepath.IsAbs(cfg.DataDir) {
				return acpdriver.Launch{}, fmt.Errorf("pi-acp requires an absolute AO data directory, got %q", cfg.DataDir)
			}
			env := cloneEnv(cfg.Env)
			// pi-acp 0.17 uses a long-running daemon. A session-private socket
			// prevents one project's first-start environment (notably
			// PI_CODING_AGENT_DIR and provider keys) leaking into another.
			env["PI_ACP_SOCKET_DIR"] = filepath.Join(cfg.DataDir, "pi-acp", string(cfg.SessionID), "run")
			if err := prepareStandingInstructions(ctx, cfg); err != nil {
				return acpdriver.Launch{}, err
			}
			return acpdriver.Launch{Command: binary, Env: env}, nil
		},
		ValidateInitialize: validateInitialize,
		SessionMeta:        sessionMeta,
		SessionOptions:     sessionOptions,
	}
}

func cloneEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

func instructionDir(cfg acpdriver.LaunchConfig) string {
	return filepath.Join(cfg.DataDir, "prompts", string(cfg.SessionID), "pi-acp")
}

func prepareStandingInstructions(ctx context.Context, cfg acpdriver.LaunchConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		return nil
	}
	dir := instructionDir(cfg)
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o700); err != nil {
		return fmt.Errorf("prepare Pi ACP instructions: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	content := strings.TrimRight(cfg.SystemPrompt, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o600); err != nil {
		return fmt.Errorf("write Pi ACP instructions: %w", err)
	}
	return nil
}

func sessionMeta(cfg acpdriver.LaunchConfig) map[string]any {
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		return nil
	}
	// An inline resource manifest composes the ordinary project/user Pi
	// resources with AO's standing instructions. The first root preserves the
	// exact project AGENTS.md, skills, prompts, extensions, and agent directory
	// Pi's TUI sees; the second adds only AO's generated AGENTS.md.
	manifest := map[string]any{
		"version":       1,
		"mode":          "local",
		"mergeStrategy": "append",
		"roots": []any{
			map[string]any{"id": "project", "kind": "local", "paths": map[string]any{"cwd": cfg.WorkspacePath}},
			map[string]any{"id": "ao", "kind": "local", "paths": map[string]any{
				"cwd": instructionDir(cfg), "agentDir": filepath.Join(instructionDir(cfg), "agent"),
			}},
		},
	}
	return map[string]any{"piAcp": map[string]any{"manifest": manifest}}
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	if settings.Effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "thought_level", Value: settings.Effort})
	}
	return options
}

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$`)

func validateInitialize(init acpsdk.InitializeResponse) error {
	if init.AgentInfo == nil || init.AgentInfo.Name != distributionName {
		got := "missing agentInfo"
		if init.AgentInfo != nil {
			got = init.AgentInfo.Name
		}
		return fmt.Errorf("unsupported Pi ACP distribution %q; install %s", got, distributionName)
	}
	installed, ok := parseVersion(init.AgentInfo.Version)
	minimum, _ := parseVersion(minimumVersion)
	if !ok {
		return fmt.Errorf("unrecognized %s version %q", distributionName, init.AgentInfo.Version)
	}
	if installed.less(minimum) {
		return fmt.Errorf("%s %s is older than AO's tested minimum %s", distributionName, init.AgentInfo.Version, minimumVersion)
	}
	return nil
}

type version [3]int

func parseVersion(value string) (version, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 4 {
		return version{}, false
	}
	var out version
	for i := range out {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return version{}, false
		}
		out[i] = n
	}
	return out, true
}

func (v version) less(other version) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

var piACPBinarySpec = binaryutil.BinarySpec{
	Label:         "pi-acp",
	Names:         []string{"pi-acp"},
	WinNames:      []string{"pi-acp.cmd", "pi-acp.exe", "pi-acp"},
	UnixPaths:     []string{"/usr/local/bin/pi-acp", "/opt/homebrew/bin/pi-acp"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("pi-acp", []string{".local", "bin", "pi-acp"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "pi-acp.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "pi-acp.exe"}},
	},
}

// ResolveBinary finds a pre-installed pi-acp. It never invokes npm, npx, Bun,
// or any network installer.
func ResolveBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, piACPBinarySpec)
}
