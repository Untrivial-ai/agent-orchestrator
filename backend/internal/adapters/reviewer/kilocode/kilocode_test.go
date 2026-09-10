package kilocode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type captureAgent struct {
	got  ports.LaunchConfig
	argv []string
	err  error
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.got = cfg
	return a.argv, a.err
}
func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *captureAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error { return nil }
func (a *captureAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandPreservesAgentAndAppliesReadOnlyPolicy(t *testing.T) {
	agent := &captureAgent{argv: []string{
		"env",
		`KILO_CONFIG_CONTENT={"agent":{"ao-review-w1":{"prompt":"review only"}}}`,
		"kilocode",
		"--agent", "ao-review-w1",
		"--prompt", "review it",
	}}
	r := &Reviewer{agent: agent}

	got, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		WorkspacePath:  "/ws/w1",
		Prompt:         "review it",
		SystemPrompt:   "review only",
		Config:         ports.AgentConfig{Model: "gpt-5"},
		TaskPromptRoot: filepath.Join("ao", "prompts", "reviewer"),
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	if agent.got.Permissions != ports.PermissionModeDefault {
		t.Fatalf("permissions = %q, want default before reviewer policy overlay", agent.got.Permissions)
	}
	if agent.got.Config.Model != "gpt-5" {
		t.Fatalf("launch config model = %q, want gpt-5", agent.got.Config.Model)
	}
	if agent.got.WorkspacePath != "/ws/w1" || agent.got.Prompt != "review it" || agent.got.SystemPrompt != "review only" {
		t.Fatalf("launch config = %+v", agent.got)
	}
	if got.Argv[0] != "env" || got.Argv[2] != "kilocode" {
		t.Fatalf("argv = %#v", got.Argv)
	}

	var config struct {
		Agent      map[string]any `json:"agent"`
		Permission struct {
			CatchAll          string            `json:"*"`
			Read              string            `json:"read"`
			Bash              map[string]string `json:"bash"`
			ExternalDirectory map[string]string `json:"external_directory"`
		} `json:"permission"`
	}
	raw := strings.TrimPrefix(got.Argv[1], configAssignmentPrefix)
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("reviewer config: %v", err)
	}
	if _, ok := config.Agent["ao-review-w1"]; !ok {
		t.Fatalf("generated reviewer agent was lost: %s", raw)
	}
	if config.Permission.CatchAll != "deny" || config.Permission.Read != "allow" {
		t.Fatalf("permission policy = %+v", config.Permission)
	}
	if config.Permission.Bash["*"] != "deny" ||
		config.Permission.Bash["gh api *"] != "allow" ||
		config.Permission.Bash["ao review submit *"] != "allow" ||
		config.Permission.Bash["printf *"] != "allow" {
		t.Fatalf("bash policy = %#v", config.Permission.Bash)
	}
	wantPromptPattern := filepath.ToSlash(filepath.Join("ao", "prompts", "reviewer", "**"))
	if !reflect.DeepEqual(config.Permission.ExternalDirectory, map[string]string{wantPromptPattern: "allow"}) {
		t.Fatalf("external directory = %#v", config.Permission.ExternalDirectory)
	}
}

func TestReviewCommandBuildsKiloCodeArgvWithHiddenPrompts(t *testing.T) {
	binDir := t.TempDir()
	binaryName := "kilocode"
	binaryBody := "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		binaryName = "kilocode.cmd"
		binaryBody = "@echo off\r\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, binaryName), []byte(binaryBody), 0o755); err != nil {
		t.Fatalf("write fake Kilo Code CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptRoot := t.TempDir()
	systemPath := filepath.Join(promptRoot, "system.md")
	if err := os.WriteFile(systemPath, []byte("review system prompt\n"), 0o600); err != nil {
		t.Fatalf("write system prompt: %v", err)
	}

	spec, err := New().ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		WorkspacePath:    t.TempDir(),
		Prompt:           "Read the AO review task.",
		SystemPromptFile: systemPath,
		TaskPromptRoot:   promptRoot,
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	joinedArgv := strings.Join(spec.Argv, "\n")
	for _, want := range []string{
		"KILO_CONFIG_CONTENT=",
		"--agent\nao-review-w1",
		"--prompt\nRead the AO review task.",
	} {
		if !strings.Contains(joinedArgv, want) {
			t.Fatalf("argv missing %q: %#v", want, spec.Argv)
		}
	}
	if strings.Contains(joinedArgv, "review system prompt") {
		t.Fatalf("hidden system prompt leaked into visible argv: %#v", spec.Argv)
	}
}

func TestBashPolicyAllowsEveryParsedReportingPipelineStage(t *testing.T) {
	bash := reviewerBashPolicy(t)
	tests := []struct {
		name   string
		stages []string
	}{
		{
			name: "GitHub review",
			stages: []string{
				`printf '%s' '{ "event": "COMMENT", "body": "ready" }'`,
				`gh api --method POST repos/o/r/pulls/1/reviews --input - --jq '.id'`,
			},
		},
		{
			name: "AO bookkeeping",
			stages: []string{
				`printf '%s' '{ "reviews": [] }'`,
				`ao review submit --session sess-1 --reviews -`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, stage := range tt.stages {
				if !bashAllowsCommand(t, bash, stage) {
					t.Fatalf("parsed pipeline stage is denied: %q; policy = %#v", stage, bash)
				}
			}
		})
	}
	if bashAllowsCommand(t, bash, `rm -rf /`) {
		t.Fatalf("arbitrary command is allowed by policy: %#v", bash)
	}
}

func TestReviewCommandAddsConfigWhenWorkerHasNoInlineConfig(t *testing.T) {
	agent := &captureAgent{argv: []string{"kilocode"}}
	got, err := (&Reviewer{agent: agent}).ReviewCommand(context.Background(), ports.ReviewInvocation{})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if len(got.Argv) != 3 || got.Argv[0] != "env" || !strings.HasPrefix(got.Argv[1], configAssignmentPrefix) || got.Argv[2] != "kilocode" {
		t.Fatalf("argv = %#v", got.Argv)
	}
}

func reviewerBashPolicy(t *testing.T) map[string]string {
	t.Helper()
	argv, err := withReviewerConfig([]string{"kilocode"}, "", "")
	if err != nil {
		t.Fatalf("withReviewerConfig: %v", err)
	}
	var config struct {
		Permission struct {
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	raw := strings.TrimPrefix(argv[1], configAssignmentPrefix)
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("reviewer config: %v", err)
	}
	return config.Permission.Bash
}

func bashAllowsCommand(t *testing.T, policy map[string]string, command string) bool {
	t.Helper()
	for pattern, action := range policy {
		if action == "deny" {
			continue
		}
		parts := strings.Split(pattern, "*")
		for i, part := range parts {
			parts[i] = regexp.QuoteMeta(part)
		}
		matcher, err := regexp.Compile("^" + strings.Join(parts, ".*") + "$")
		if err != nil {
			t.Fatalf("compile pattern %q: %v", pattern, err)
		}
		if matcher.MatchString(command) {
			return true
		}
	}
	return false
}

func TestReviewCommandPropagatesUnavailableCLI(t *testing.T) {
	agent := &captureAgent{err: errors.New(`kilocode binary not found`)}
	_, err := (&Reviewer{agent: agent}).ReviewCommand(context.Background(), ports.ReviewInvocation{})
	if err == nil || !strings.Contains(err.Error(), "binary not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewCommandRejectsMalformedWorkerConfig(t *testing.T) {
	agent := &captureAgent{argv: []string{"env", "KILO_CONFIG_CONTENT={", "kilocode"}}
	_, err := (&Reviewer{agent: agent}).ReviewCommand(context.Background(), ports.ReviewInvocation{})
	if err == nil || !strings.Contains(err.Error(), "decode Kilo Code launch config") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewMessageReturnsTaskPrompt(t *testing.T) {
	got, err := (&Reviewer{}).ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "next review"})
	if err != nil {
		t.Fatalf("ReviewMessage: %v", err)
	}
	if got != "next review" {
		t.Fatalf("message = %q", got)
	}
}
