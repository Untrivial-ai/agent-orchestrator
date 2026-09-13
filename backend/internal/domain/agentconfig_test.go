package domain

import "testing"

func TestAgentConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AgentConfig
		wantErr bool
	}{
		{"empty ok", AgentConfig{}, false},
		{"good model", AgentConfig{Model: "claude-opus-4-5"}, false},
		{"good permission", AgentConfig{Permissions: PermissionModeAuto}, false},
		{"bad permission", AgentConfig{Permissions: "yolo"}, true},

		{"mcp nil ok", AgentConfig{}, false},
		{"mcp configs ok", AgentConfig{MCP: &MCPConfig{Configs: []string{"{\"a\":1}", "/path/to/mcp.json"}}}, false},
		{"mcp empty config entry", AgentConfig{MCP: &MCPConfig{Configs: []string{"  "}}}, true},
		{"mcp strict alone ok (isolation)", AgentConfig{MCP: &MCPConfig{Strict: true}}, false},
		{"mcp strict with configs ok", AgentConfig{MCP: &MCPConfig{Configs: []string{"{\"a\":1}"}, Strict: true}}, false},

		{"plugin dir path ok", AgentConfig{PluginDirs: []string{"/abs/path", "rel/path"}}, false},
		{"plugin url ok", AgentConfig{PluginDirs: []string{"https://example.com/p.zip"}}, false},
		{"plugin empty entry", AgentConfig{PluginDirs: []string{"ok", "  "}}, true},

		{"env map ok", AgentConfig{Env: map[string]string{"FOO": "bar"}}, false},
		{"system prompt ok", AgentConfig{SystemPrompt: "extra standing instructions"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestAgentConfigIsZero(t *testing.T) {
	if !(AgentConfig{}).IsZero() {
		t.Fatalf("empty AgentConfig should be zero")
	}
	// Each new field, when set, makes the config non-zero.
	setters := map[string]func() AgentConfig{
		"model":        func() AgentConfig { return AgentConfig{Model: "m"} },
		"permissions":  func() AgentConfig { return AgentConfig{Permissions: PermissionModeAuto} },
		"systemPrompt": func() AgentConfig { return AgentConfig{SystemPrompt: "x"} },
		"env":          func() AgentConfig { return AgentConfig{Env: map[string]string{"K": "v"}} },
		"mcp":          func() AgentConfig { return AgentConfig{MCP: &MCPConfig{Strict: true}} },
		"pluginDirs":   func() AgentConfig { return AgentConfig{PluginDirs: []string{"/p"}} },
	}
	for name, mk := range setters {
		t.Run(name, func(t *testing.T) {
			if mk().IsZero() {
				t.Fatalf("AgentConfig with %s set should not be zero", name)
			}
		})
	}
}
