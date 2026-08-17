package legacyimport

import (
	"strings"
	"testing"
	"time"

	yaml "gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// nonNilNode returns a non-nil *yaml.Node for struct fields that are captured
// as raw nodes (tracker, scm, etc.), used to trigger the "dropped" note path.
func nonNilNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: "x"}
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func TestMapPermission(t *testing.T) {
	cases := []struct {
		in    string
		want  domain.PermissionMode
		ok    bool
		lossy bool
	}{
		{"", "", false, false},
		{"permissionless", domain.PermissionModeBypassPermissions, true, false},
		{"skip", domain.PermissionModeBypassPermissions, true, false},
		{"auto-edit", domain.PermissionModeAcceptEdits, true, false},
		{"default", domain.PermissionModeDefault, true, false},
		{"suggest", domain.PermissionModeDefault, true, true},
		{"weird", domain.PermissionModeDefault, true, true},
	}
	for _, c := range cases {
		mode, ok, lossy := mapPermission(c.in)
		if mode != c.want || ok != c.ok || lossy != c.lossy {
			t.Fatalf("mapPermission(%q) = (%q,%v,%v), want (%q,%v,%v)", c.in, mode, ok, lossy, c.want, c.ok, c.lossy)
		}
	}
}

func TestMapHarness(t *testing.T) {
	if h, ok := mapHarness("claude-code"); !ok || h != domain.HarnessClaudeCode {
		t.Fatalf("claude-code = (%q,%v)", h, ok)
	}
	if _, ok := mapHarness("nope"); ok {
		t.Fatal("unknown harness must map to ok=false")
	}
	if _, ok := mapHarness(""); ok {
		t.Fatal("empty harness must map to ok=false")
	}
}

func TestBuildProjectConfig_RemapAndPreserveMain(t *testing.T) {
	var notes []string
	pc := legacyProjectConfig{
		DefaultBranch: "main",
		SessionPrefix: "px",
		Env:           map[string]string{"K": "V"},
		AgentConfig:   &legacyAgentConfig{Model: "m", Permissions: "suggest"},
		Worker:        &legacyRole{Agent: "codex"},
		Orchestrator:  &legacyRole{Agent: "bogus"}, // no rewrite harness → dropped note
		Tracker:       nonNilNode(),
	}
	cfg := buildProjectConfig(pc, &notes)
	if cfg.DefaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want explicit main preserved", cfg.DefaultBranch)
	}
	if cfg.SessionPrefix != "px" || cfg.Env["K"] != "V" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.AgentConfig.Permissions != domain.PermissionModeDefault {
		t.Fatalf("permissions = %q, want default (lossy from suggest)", cfg.AgentConfig.Permissions)
	}
	if cfg.Worker.Harness != domain.HarnessCodex {
		t.Fatalf("worker harness = %q, want codex", cfg.Worker.Harness)
	}
	if cfg.Orchestrator.Harness != "" {
		t.Fatalf("orchestrator harness = %q, want dropped (unknown)", cfg.Orchestrator.Harness)
	}
	if len(notes) == 0 {
		t.Fatal("expected lossy/dropped notes")
	}
}

func TestBuildProjectConfig_NonMainBranchKept(t *testing.T) {
	var notes []string
	cfg := buildProjectConfig(legacyProjectConfig{DefaultBranch: "develop"}, &notes)
	if cfg.DefaultBranch != "develop" {
		t.Fatalf("defaultBranch = %q, want develop", cfg.DefaultBranch)
	}
}

func TestBuildProjectConfig_CarriesPromptRules(t *testing.T) {
	var notes []string
	cfg := buildProjectConfig(legacyProjectConfig{
		AgentRules:       stringNode("Run focused tests."),
		AgentRulesFile:   stringNode(" docs/rules.md "),
		OrchestratorRule: stringNode("Coordinate through workers."),
	}, &notes)
	if cfg.AgentRules != "Run focused tests." {
		t.Fatalf("agentRules = %q", cfg.AgentRules)
	}
	if cfg.AgentRulesFile != "docs/rules.md" {
		t.Fatalf("agentRulesFile = %q", cfg.AgentRulesFile)
	}
	if cfg.OrchestratorRules != "Coordinate through workers." {
		t.Fatalf("orchestratorRules = %q", cfg.OrchestratorRules)
	}
	for _, note := range notes {
		if strings.Contains(note, "rules") {
			t.Fatalf("supported prompt rules should not be reported as dropped: %v", notes)
		}
	}
}

func TestBuildProjectRecord_DisplayNameAndRegisteredAt(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	prefs := preferences{Projects: map[string]struct {
		DisplayName string `json:"displayName"`
	}{"proj": {DisplayName: "Pretty"}}}
	reg := registeredManifest{Projects: []struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		AddedAt string `json:"addedAt"`
	}{{ID: "proj", AddedAt: "2026-05-05T00:00:00Z"}}}

	deps := projectRowDeps{
		repoOriginURL: func(string) string { return "git@github.com:o/r.git" },
		now:           now,
	}
	rec, _ := buildProjectRecord("proj", legacyProjectConfig{Path: "/repo", Name: "ignored"}, prefs, reg, deps)
	if rec.DisplayName != "Pretty" {
		t.Fatalf("displayName = %q, want Pretty (preferences win)", rec.DisplayName)
	}
	if rec.RepoOriginURL != "git@github.com:o/r.git" {
		t.Fatalf("origin = %q", rec.RepoOriginURL)
	}
	if rec.RegisteredAt.Format(time.RFC3339) != "2026-05-05T00:00:00Z" {
		t.Fatalf("registeredAt = %s, want addedAt", rec.RegisteredAt)
	}
	if rec.Kind != domain.ProjectKindSingleRepo {
		t.Fatalf("kind = %s", rec.Kind)
	}
}

func TestBuildProjectRecord_DisplayNameFallbacks(t *testing.T) {
	now := time.Now().UTC()
	deps := projectRowDeps{now: now}
	// No preferences, config name == id → empty display name (rewrite falls back to id).
	rec, _ := buildProjectRecord("proj", legacyProjectConfig{Path: "/r", Name: "proj"}, preferences{}, registeredManifest{}, deps)
	if rec.DisplayName != "" {
		t.Fatalf("displayName = %q, want empty", rec.DisplayName)
	}
	if !rec.RegisteredAt.Equal(now) {
		t.Fatalf("registeredAt = %s, want now fallback", rec.RegisteredAt)
	}
}
