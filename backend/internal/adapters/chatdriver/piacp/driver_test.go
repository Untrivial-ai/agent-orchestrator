package piacp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakePiPlugin struct {
	status ports.AgentAuthStatus
	err    error
}

func (p fakePiPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return p.status, p.err
}

func TestLaunchUsesPreinstalledAdapterAndPreservesPiEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	cfg := buildConfig(
		fakePiPlugin{status: ports.AgentAuthStatusAuthorized},
		func(context.Context) (string, error) { return "/user/bin/pi-acp", nil },
		nil,
	)
	if err := cfg.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	launchCfg := acpdriver.LaunchConfig{
		SessionID:     "session-1",
		DataDir:       dataDir,
		WorkspacePath: filepath.Join(dataDir, "worktree"),
		Env: map[string]string{
			"PI_CODING_AGENT_DIR": "/user/pi-config",
			"ANTHROPIC_API_KEY":   "from-project",
			"PATH":                "/user/bin",
		},
		SystemPrompt: "AO standing instructions",
	}
	launch, err := cfg.Launch(context.Background(), launchCfg)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if launch.Command != "/user/bin/pi-acp" || len(launch.Args) != 0 {
		t.Fatalf("launch = %#v, want exact pre-installed pi-acp with no installer arguments", launch)
	}
	for key, want := range launchCfg.Env {
		if got := launch.Env[key]; got != want {
			t.Errorf("env[%q] = %q, want %q", key, got, want)
		}
	}
	wantSocketDir := filepath.Join(dataDir, "pi-acp", "session-1", "run")
	if got := launch.Env["PI_ACP_SOCKET_DIR"]; got != wantSocketDir {
		t.Fatalf("PI_ACP_SOCKET_DIR = %q, want %q", got, wantSocketDir)
	}
	content, err := os.ReadFile(filepath.Join(instructionDir(launchCfg), "AGENTS.md"))
	if err != nil {
		t.Fatalf("read generated Pi instructions: %v", err)
	}
	if string(content) != "AO standing instructions\n" {
		t.Fatalf("generated instructions = %q", content)
	}

	meta := cfg.SessionMeta(launchCfg)
	piMeta := meta["piAcp"].(map[string]any)
	manifest := piMeta["manifest"].(map[string]any)
	roots := manifest["roots"].([]any)
	projectPaths := roots[0].(map[string]any)["paths"].(map[string]any)
	if projectPaths["cwd"] != launchCfg.WorkspacePath {
		t.Fatalf("project root = %#v, want ACP cwd %q", projectPaths, launchCfg.WorkspacePath)
	}
	aoPaths := roots[1].(map[string]any)["paths"].(map[string]any)
	if aoPaths["agentDir"] != filepath.Join(instructionDir(launchCfg), "agent") {
		t.Fatalf("AO instruction root = %#v", aoPaths)
	}
}

func TestLaunchHonorsCanceledContextBeforeWritingStandingInstructions(t *testing.T) {
	dataDir := t.TempDir()
	cfg := buildConfig(
		fakePiPlugin{status: ports.AgentAuthStatusAuthorized},
		func(context.Context) (string, error) { return "/user/bin/pi-acp", nil },
		nil,
	)
	launchCfg := acpdriver.LaunchConfig{
		SessionID:    "session-1",
		DataDir:      dataDir,
		SystemPrompt: "AO standing instructions",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cfg.Launch(ctx, launchCfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Launch error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(instructionDir(launchCfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("instruction directory stat error = %v, want os.ErrNotExist", err)
	}
}

func TestMissingPiACPDoesNotDownload(t *testing.T) {
	want := errors.New("not found")
	cfg := buildConfig(fakePiPlugin{}, func(context.Context) (string, error) { return "", want }, nil)
	err := cfg.Probe(context.Background())
	if !errors.Is(err, ports.ErrChatDriverUnavailable) || !errors.Is(err, want) {
		t.Fatalf("Probe error = %v", err)
	}
}

func TestProbeReusesPiAuth(t *testing.T) {
	cfg := buildConfig(
		fakePiPlugin{status: ports.AgentAuthStatusUnauthorized},
		func(context.Context) (string, error) { return "/user/bin/pi-acp", nil },
		nil,
	)
	if err := cfg.Probe(context.Background()); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("Probe error = %v, want ErrChatAuthRequired", err)
	}
}

func TestPiACPDistributionAndVersionPolicy(t *testing.T) {
	info := func(name, version string) acpsdk.InitializeResponse {
		return acpsdk.InitializeResponse{AgentInfo: &acpsdk.Implementation{Name: name, Version: version}}
	}
	for _, tc := range []struct {
		name    string
		init    acpsdk.InitializeResponse
		wantErr bool
	}{
		{name: "tested minimum", init: info(distributionName, minimumVersion)},
		{name: "newer compatible", init: info(distributionName, "0.18.0")},
		{name: "old", init: info(distributionName, "0.17.0"), wantErr: true},
		{name: "wrong distribution", init: info("community/pi-acp", minimumVersion), wantErr: true},
		{name: "missing info", init: acpsdk.InitializeResponse{}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInitialize(tc.init)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateInitialize error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPiACPFeatureMapping(t *testing.T) {
	cfg := buildConfig(fakePiPlugin{}, func(context.Context) (string, error) { return "pi-acp", nil }, nil)
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming,
		ports.ChatCapabilityTools,
		ports.ChatCapabilityInterrupt,
		ports.ChatCapabilityResume,
		ports.ChatCapabilityUsage,
		ports.ChatCapabilityDiffs,
	} {
		if !cfg.Capabilities.Has(capability) {
			t.Errorf("capability %q is false", capability)
		}
	}
	if cfg.Capabilities.Has(ports.ChatCapabilityApprovals) {
		t.Fatal("Pi ACP advertises approvals even though it cannot enforce AO permission modes")
	}
	if missing := ports.MissingProductionCapabilities(cfg.Capabilities); !reflect.DeepEqual(missing, []ports.ChatCapability{ports.ChatCapabilityApprovals}) {
		t.Fatalf("missing production capabilities = %v, want approvals so mutating Chat admission is refused", missing)
	}

	got := cfg.SessionOptions(ports.ChatTurnSettings{Model: "anthropic/claude-sonnet-4", Effort: "high"})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: "anthropic/claude-sonnet-4"},
		{ID: "thought_level", Value: "high"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session options = %#v, want %#v", got, want)
	}
}
