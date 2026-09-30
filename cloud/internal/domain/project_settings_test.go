package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectSettingsLegacyAndPartialMerge(t *testing.T) {
	existing := json.RawMessage(`{"workerAgent":"codex","orchestratorAgent":"claude-code","worker":{"agentConfig":{"model":"worker-model","permissions":"auto"}},"orchestrator":{"agent":"cursor"},"coder":{"templateId":"keep"},"agentRules":"keep rules","autoReview":false}`)
	merged, err := MergeProjectSettingsConfig(existing, json.RawMessage(`{"worker":{"agentConfig":{"effort":"max"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := DecodeProjectSettings(merged)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Worker.Agent != "codex" || settings.Worker.AgentConfig.Model != "worker-model" || settings.Worker.AgentConfig.Permissions != "auto" || settings.Worker.AgentConfig.Effort != "max" || settings.Orchestrator.Agent != "cursor" || *settings.AutoReview {
		t.Fatalf("settings = %+v, worker = %+v", settings, settings.Worker)
	}
	if strings.Contains(string(merged), "workerAgent") || strings.Contains(string(merged), "orchestratorAgent") || !strings.Contains(string(merged), `"templateId":"keep"`) || !strings.Contains(string(merged), `"agentRules":"keep rules"`) {
		t.Fatalf("config lost hidden settings or retained legacy choices: %s", merged)
	}
}

func TestProjectSettingsValidation(t *testing.T) {
	for _, raw := range []string{
		`null`, `{"displayName":null}`, `{"displayName":" "}`, `{"defaultBranch":""}`,
		`{"workerAgent":"codex"}`, `{"config":null}`, `{"config":{"sessionPrefix":"unused"}}`,
		`{"config":{"autoInjectReview":false}}`, `{"config":{"autoReview":"false"}}`,
		`{"config":{"reviewers":null}}`, `{"config":{"autoReview":null}}`,
		`{"config":{"worker":{"agent":null}}}`, `{"config":{"worker":{"agentConfig":null}}}`,
		`{"config":{"worker":{"agentConfig":{"model":null}}}}`,
		`{"config":{"worker":{"agent":"unknown"}}}`,
		`{"config":{"worker":{"agent":"codex","agentConfig":{"permissions":"unknown"}}}}`,
		`{"config":{"reviewers":[{"harness":"cursor","agentConfig":{"effort":"high"}}]}}`,
		`{"config":{"reviewers":[{"harness":"claude-code","agentConfig":{"effort":"xhigh"}}]}}`,
		`{"config":{"reviewers":[{"harness":"opencode","agentConfig":{"permissions":"accept-edits"}}]}}`,
		`{"config":{"reviewers":[{"harness":"codex"},{"harness":"claude-code"}]}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			patch, err := ParseProjectSettingsPatch(json.RawMessage(raw))
			if err == nil {
				_, err = MergeProjectSettingsConfig(json.RawMessage(`{}`), patch.Config)
			}
			if err == nil {
				t.Fatal("invalid settings were accepted")
			}
		})
	}
}

func TestProjectSettingsClearsRoleDefaults(t *testing.T) {
	for _, role := range []string{"worker", "orchestrator"} {
		t.Run(role, func(t *testing.T) {
			existing := json.RawMessage(`{"workerAgent":"codex","orchestratorAgent":"claude-code","worker":{"agentConfig":{"model":"worker-model","effort":"max","permissions":"auto"}},"orchestrator":{"agentConfig":{"model":"orchestrator-model","effort":"high"}},"reviewers":[{"harness":"cursor"}],"autoReview":false,"coder":{"templateId":"keep"}}`)
			patch, err := ParseProjectSettingsPatch(json.RawMessage(`{"config":{"` + role + `":null}}`))
			if err != nil {
				t.Fatal(err)
			}
			merged, err := MergeProjectSettingsConfig(existing, patch.Config)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(merged, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields[role]; exists || strings.Contains(string(merged), role+"Agent") {
				t.Fatalf("role override remains: %s", merged)
			}
			harness, config, err := SessionAgentConfig(merged, role, "opencode", "")
			if err != nil || harness != "opencode" || config != (ProjectAgentConfig{}) {
				t.Fatalf("session selection = %s %+v %v", harness, config, err)
			}
			other := "worker"
			if role == other {
				other = "orchestrator"
			}
			normalized, err := NormalizeProjectConfig(existing)
			if err != nil {
				t.Fatal(err)
			}
			var before map[string]json.RawMessage
			_ = json.Unmarshal(normalized, &before)
			for _, key := range []string{other, "reviewers", "autoReview", "coder"} {
				if string(fields[key]) != string(before[key]) {
					t.Fatalf("omitted %s changed: %s", key, merged)
				}
			}
		})
	}
}

func TestEffectiveReviewerAndSessionDefaults(t *testing.T) {
	settings, err := DecodeProjectSettings(json.RawMessage(`{"worker":{"agent":"codex","agentConfig":{"model":"worker","effort":"max","permissions":"accept-edits"}},"reviewers":[{"harness":"claude-code","agentConfig":{"model":"reviewer","effort":"high","permissions":"auto"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	harness, config, err := SessionAgentConfig(json.RawMessage(`{"workerAgent":"codex","worker":{"agentConfig":{"model":"default","effort":"max"}}}`), "worker", "codex", "explicit")
	if err != nil || harness != "codex" || config.Model != "explicit" || config.Effort != "max" {
		t.Fatalf("session defaults = %s %+v %v", harness, config, err)
	}
	reviewer := EffectiveReviewer(settings, "codex", "worker", "trusted", config)
	if reviewer.Harness != "claude-code" || reviewer.AgentConfig.Model != "reviewer" || reviewer.AgentConfig.Permissions != "auto" {
		t.Fatalf("reviewer = %+v", reviewer)
	}
	fallback := EffectiveReviewer(ProjectSettingsConfig{}, "codex", "worker", "trusted", config)
	if fallback.Harness != "codex" || fallback.AgentConfig.Model != "worker" || fallback.AgentConfig.Effort != "max" || fallback.AgentConfig.Permissions != "bypass-permissions" {
		t.Fatalf("fallback = %+v", fallback)
	}
}
