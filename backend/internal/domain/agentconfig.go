package domain

import (
	"fmt"
	"reflect"
	"strings"
)

// PermissionMode controls how much review an agent requires before acting. It
// lives in domain (not ports) so the typed AgentConfig can carry it; ports
// re-exports it as a type alias so agent adapters keep referring to
// ports.PermissionMode unchanged.
type PermissionMode string

// The permission modes adapters map onto their agent's native approval flags.
const (
	// PermissionModeDefault is special: adapters choose their own baseline
	// behavior for it. Most defer to the agent's own config; some managed
	// adapters may map it to a safer non-interactive default.
	PermissionModeDefault           PermissionMode = "default"
	PermissionModeAcceptEdits       PermissionMode = "accept-edits"
	PermissionModeAuto              PermissionMode = "auto"
	PermissionModeBypassPermissions PermissionMode = "bypass-permissions"
)

// AgentConfig is the typed per-project agent configuration. It replaces the
// former free-form map so the fields are validated and the API/UI render a
// real form rather than arbitrary JSON. An empty value (IsZero) means unset.
type AgentConfig struct {
	// Model overrides the agent's default model (e.g. claude-opus-4-5).
	Model string `json:"model,omitempty"`
	// Mode selects an agent-owned operating mode when the adapter exposes modes
	// instead of raw model ids (currently Amp: low|medium|high|ultra).
	Mode string `json:"mode,omitempty"`
	// Permissions sets the agent's starting permission mode. Empty inherits the
	// project/role preference; new sessions fall back to Auto when none is saved.
	// Other adapter callers retain their existing baseline for an empty value.
	Permissions PermissionMode `json:"permissions,omitempty"`

	// SystemPrompt is a per-role base prompt appended to AO's role-derived
	// system prompt at spawn/restore. It lets a project layer standing
	// instructions onto a worker (or orchestrator) beyond the built-in role.
	// A role SystemPrompt replaces (not concatenates) a base AgentConfig
	// SystemPrompt; today only the role level is configurable, so this matters
	// only if the base level is ever exposed.
	SystemPrompt string `json:"systemPrompt,omitempty"`
	// Env are extra environment variables forwarded into the session runtime,
	// merged on top of the project's Env so a per-role value wins on key
	// collision. AO-internal vars (AO_SESSION_ID, …) always win.
	Env map[string]string `json:"env,omitempty"`
	// MCP scopes the MCP servers a session loads. When set, adapters that
	// support per-session MCP (claude-code) pass it as --mcp-config; Strict
	// additionally passes --strict-mcp-config so the session ignores every
	// other MCP source (isolation). nil means inherit the global config.
	MCP *MCPConfig `json:"mcp,omitempty"`
	// PluginDirs are plugin paths or URLs loaded for this session only
	// (claude-code: a local path maps to --plugin-dir, an http(s):// URL to
	// --plugin-url). nil/empty means inherit the global plugin set.
	PluginDirs []string `json:"pluginDirs,omitempty"`
}

// MCPConfig narrows the MCP servers a session sees. It is a pointer on
// AgentConfig so an empty (default) config stays a zero value for storage and
// resolution, while a present-but-empty MCPConfig is meaningful: Strict on its
// own isolates a worker from every MCP source.
type MCPConfig struct {
	// Configs are the values passed to claude-code's repeatable --mcp-config
	// flag. Each is either a JSON string defining servers inline or a path to
	// a JSON file, exactly as claude-code's CLI accepts.
	Configs []string `json:"configs,omitempty"`
	// Strict, when true, adds --strict-mcp-config so claude-code uses only the
	// servers in Configs and ignores all other MCP configurations. Strict with
	// empty Configs is valid and means "this session gets no MCP servers".
	Strict bool `json:"strict,omitempty"`
}

// IsZero reports whether the config carries no settings, so storage can persist
// SQL NULL and resolution can skip an empty config. Map, slice, and pointer
// fields are compared by value via reflect (a bare == would not compile).
func (c AgentConfig) IsZero() bool {
	return reflect.DeepEqual(c, AgentConfig{})
}

// Valid reports whether the mode is one AO knows. Empty counts as valid: it means
// "the adapter's own baseline", which is a legitimate choice rather than a missing
// one.
func (m PermissionMode) Valid() bool {
	switch m {
	case "", PermissionModeDefault, PermissionModeAcceptEdits,
		PermissionModeAuto, PermissionModeBypassPermissions:
		return true
	default:
		return false
	}
}

// Validate rejects values outside the typed vocabulary so a bad config is
// refused when it is set (CLI/API) rather than silently dropped at spawn.
func (c AgentConfig) Validate() error {
	switch c.Mode {
	case "", "low", "medium", "high", "ultra":
	default:
		return fmt.Errorf("invalid mode %q: want one of low, medium, high, ultra", c.Mode)
	}
	if !c.Permissions.Valid() {
		return fmt.Errorf("invalid permissions %q: want one of default, accept-edits, auto, bypass-permissions", c.Permissions)
	}
	if c.MCP != nil {
		for i, cfg := range c.MCP.Configs {
			if strings.TrimSpace(cfg) == "" {
				return fmt.Errorf("mcp.configs[%d]: empty entry", i)
			}
		}
		// Strict with empty Configs is intentional isolation, not an error.
	}
	for i, dir := range c.PluginDirs {
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("pluginDirs[%d]: empty entry", i)
		}
	}
	return nil
}
