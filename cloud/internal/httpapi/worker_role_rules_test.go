package httpapi

import (
	"encoding/json"
	"testing"
)

func TestProjectRoleRulesIgnoresLegacyNonStringValues(t *testing.T) {
	agentRules, orchestratorRules, err := projectRoleRules(json.RawMessage(`{
		"agentRules": ["legacy"],
		"orchestratorRules": "Delegate focused work.",
		"unrelated": {"enabled": true}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if agentRules != "" {
		t.Fatalf("agentRules = %q, want empty for a legacy non-string value", agentRules)
	}
	if orchestratorRules != "Delegate focused work." {
		t.Fatalf("orchestratorRules = %q", orchestratorRules)
	}
}

func TestProjectRoleRulesRejectsMalformedConfig(t *testing.T) {
	if _, _, err := projectRoleRules(json.RawMessage(`{"agentRules":`)); err == nil {
		t.Fatal("expected malformed project config to fail")
	}
}
