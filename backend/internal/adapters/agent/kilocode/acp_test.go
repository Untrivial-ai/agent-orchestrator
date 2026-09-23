package kilocode

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPrepareACPConfigContentMergesExistingConfig(t *testing.T) {
	got, err := PrepareACPConfigContent(
		`{"theme":"dark","agent":{"existing":{"prompt":"keep"}}}`,
		"Follow AO rules.", "sess-1", ports.PermissionModeDefault)
	if err != nil {
		t.Fatalf("PrepareACPConfigContent: %v", err)
	}
	var config struct {
		Theme        string `json:"theme"`
		DefaultAgent string `json:"default_agent"`
		Agent        map[string]struct {
			Prompt string `json:"prompt"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(got), &config); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if config.Theme != "dark" {
		t.Fatalf("existing config was not preserved: %#v", config)
	}
	if config.Agent["existing"].Prompt != "keep" {
		t.Fatalf("existing agent was not preserved: %#v", config.Agent)
	}
	if config.Agent["ao-sess-1"].Prompt != "Follow AO rules." {
		t.Fatalf("AO agent prompt = %#v", config.Agent["ao-sess-1"])
	}
	if config.DefaultAgent != "ao-sess-1" {
		t.Fatalf("default_agent = %q", config.DefaultAgent)
	}
}

func TestPrepareACPConfigContentAddsPermissionMap(t *testing.T) {
	got, err := PrepareACPConfigContent("", "", "sess-1", ports.PermissionModeBypassPermissions)
	if err != nil {
		t.Fatalf("PrepareACPConfigContent: %v", err)
	}
	var config struct {
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal([]byte(got), &config); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if config.Permission["*"] != "allow" {
		t.Fatalf("permission = %#v", config.Permission)
	}
}

func TestPrepareACPConfigContentDefaultModeIsUnchanged(t *testing.T) {
	existing := `{"theme":"dark"}`
	got, err := PrepareACPConfigContent(existing, "  ", "sess-1", ports.PermissionModeDefault)
	if err != nil {
		t.Fatalf("PrepareACPConfigContent: %v", err)
	}
	if got != existing {
		t.Fatalf("got %q, want existing config unchanged", got)
	}
}

func TestPrepareACPConfigContentRejectsNonObjectAgent(t *testing.T) {
	if _, err := PrepareACPConfigContent(`{"agent":"nope"}`, "rules", "sess-1", ports.PermissionModeDefault); err == nil {
		t.Fatal("expected an error for a non-object agent config")
	}
}
