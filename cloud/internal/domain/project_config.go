package domain

import (
	"encoding/json"
	"strings"
)

// ProjectSettings is the renderer-managed part of a cloud project's config.
// It deliberately mirrors the local project's role defaults while leaving
// unrelated config keys intact when the control plane saves the JSON object.
type ProjectSettings struct {
	SessionPrefix string          `json:"sessionPrefix,omitempty"`
	Worker        RoleSettings    `json:"worker,omitempty"`
	Orchestrator  RoleSettings    `json:"orchestrator,omitempty"`
	Reviewers     []ReviewerSetup `json:"reviewers,omitempty"`
	AutoReview    bool            `json:"autoReview,omitempty"`
}

type RoleSettings struct {
	Agent       string      `json:"agent,omitempty"`
	AgentConfig AgentConfig `json:"agentConfig,omitempty"`
}

type ReviewerSetup struct {
	Harness     string      `json:"harness"`
	AgentConfig AgentConfig `json:"agentConfig,omitempty"`
}

// AgentConfig is passed to the worker with the chosen project role. Model and
// permission policy have direct cloud-worker consumers; effort and mode are
// retained so project settings round-trip without losing a user's selection.
type AgentConfig struct {
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Permissions string `json:"permissions,omitempty"`
}

func ParseProjectSettings(raw json.RawMessage) ProjectSettings {
	var settings ProjectSettings
	_ = json.Unmarshal(raw, &settings)
	settings.SessionPrefix = strings.TrimSpace(settings.SessionPrefix)
	settings.Worker.Agent = strings.TrimSpace(settings.Worker.Agent)
	settings.Orchestrator.Agent = strings.TrimSpace(settings.Orchestrator.Agent)
	return settings
}

// AgentConfigForSession resolves the project's role-specific launch defaults.
// Review settings are intentionally not selected here: a review run creates a
// dedicated reviewer session through the review subsystem.
func (c ProjectSettings) AgentConfigForSession(kind string) AgentConfig {
	if kind == "orchestrator" {
		return c.Orchestrator.AgentConfig
	}
	return c.Worker.AgentConfig
}
