package systeminstall

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func lookPathFound(names ...string) func(string) (string, error) {
	found := make(map[string]bool, len(names))
	for _, name := range names {
		found[name] = true
	}
	return func(name string) (string, error) {
		if found[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func newTestService(goos string, found ...string) *Service {
	return &Service{
		jobs: make(map[Target]*Job), lookPath: lookPathFound(found...),
		goos: goos, installTimeout: 2 * time.Second,
		commandFunc: func(ctx context.Context, argv []string) *exec.Cmd {
			return exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // deterministic test argv
		},
	}
}

func TestAgentPlansCoverEveryHarnessOnce(t *testing.T) {
	s := newTestService("darwin", "curl", "bash", "sh", "npm", "uv", "python3", "brew", "bun")
	plans, err := s.AgentPlans(context.Background())
	if err != nil {
		t.Fatalf("AgentPlans: %v", err)
	}
	if len(plans) != 27 {
		t.Fatalf("len(AgentPlans) = %d, want 27", len(plans))
	}
	seen := map[string]bool{}
	for _, plan := range plans {
		if seen[plan.AgentID] {
			t.Errorf("duplicate agent plan %q", plan.AgentID)
		}
		seen[plan.AgentID] = true
		if plan.DocumentationURL == "" {
			t.Errorf("agent %q has no documentation URL", plan.AgentID)
		}
		if !plan.Available || !plan.Automatic {
			t.Errorf("agent %q should resolve automatically: %#v", plan.AgentID, plan)
		}
	}
}

func TestAgentPlansWindowsMarksUnixOnlyHarnessesManual(t *testing.T) {
	s := newTestService("windows", "powershell.exe", "npm", "uv", "python", "winget", "bun")
	plans, err := s.AgentPlans(context.Background())
	if err != nil {
		t.Fatalf("AgentPlans: %v", err)
	}
	manual := map[string]bool{"goose": true, "devin": true, "muse": true, "prime-agent": true}
	for _, plan := range plans {
		if manual[plan.AgentID] {
			if plan.Available || plan.Automatic || plan.Method != "manual" {
				t.Errorf("%s = %#v, want manual-only", plan.AgentID, plan)
			}
		} else if !plan.Available {
			t.Errorf("%s unexpectedly unavailable: %#v", plan.AgentID, plan)
		}
	}
}

func TestAgentPlanSelectsAvailableFallback(t *testing.T) {
	tests := []struct {
		name, goos, method string
		target             Target
		found              []string
		command            string
	}{
		{"claude brew", "darwin", "homebrew", TargetClaudeCode, []string{"brew"}, "brew install --cask claude-code"},
		{"codex npm", "linux", "npm", TargetCodex, []string{"npm"}, "npm install -g @openai/codex"},
		{"copilot winget", "windows", "winget", TargetCopilot, []string{"winget", "npm"}, "winget install -e --id GitHub.Copilot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := newTestService(tt.goos, tt.found...).planAgent(tt.target)
			if plan.Unsupported || plan.Method != tt.method || strings.Join(plan.Command, " ") != tt.command {
				t.Fatalf("plan = %#v, want method %q command %q", plan, tt.method, tt.command)
			}
		})
	}
}

func TestMissingPrerequisitesNeverExecute(t *testing.T) {
	s := newTestService("darwin")
	s.commandFunc = func(context.Context, []string) *exec.Cmd {
		t.Fatal("unsupported plan must not execute")
		return nil
	}
	job, err := s.Start(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.Status != StatusUnsupported || job.FinishedAt == nil {
		t.Fatalf("job = %#v, want terminal unsupported", job)
	}
}

func TestStartAndStatusSucceeded(t *testing.T) {
	s := newTestService("linux", "npm")
	s.commandFunc = func(context.Context, []string) *exec.Cmd { return exec.Command("true") }
	if _, err := s.Start(context.Background(), TargetCodex); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := s.Status(TargetCodex)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if job.Status == StatusSucceeded {
			if job.FinishedAt == nil {
				t.Fatal("succeeded job has no FinishedAt")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("install did not finish")
}

func TestRejectsUnknownTarget(t *testing.T) {
	s := newTestService("linux", "npm")
	if _, err := s.Start(context.Background(), Target("../../bin/sh")); err == nil {
		t.Fatal("Start unknown target succeeded")
	}
	if _, err := s.Status(Target("TMUX")); err == nil {
		t.Fatal("Status unknown target succeeded")
	}
}
