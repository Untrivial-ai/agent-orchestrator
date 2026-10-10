package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestHooksPreserveUserConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".zcode", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	input := `{"model":{"main":"user-model"},"hooks":{"timeoutMs":45000,"events":{"Stop":[{"matcher":"user","hooks":[{"type":"command","command":"user-hook","custom":true}]}]}}}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New()
	cfg := ports.WorkspaceHookConfig{WorkspacePath: dir}
	for range 2 {
		if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config["model"]), "user-model") {
		t.Fatal("user model changed")
	}
	if strings.Count(string(data), "user-hook") != 1 || strings.Count(string(data), "ao hooks zcode stop") != 1 {
		t.Fatalf("hooks duplicated or lost: %s", data)
	}
	if !strings.Contains(string(data), `"custom": true`) {
		t.Fatal("user hook fields lost")
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".zcode", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), "config.json") {
		t.Fatal("hook file is not ignored")
	}
	if err := p.UninstallHooks(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), hookCommandPrefix) || !strings.Contains(string(data), "user-hook") {
		t.Fatalf("uninstall changed user hook: %s", data)
	}
}

func TestHooksRefuseDisabledOrMalformedConfiguration(t *testing.T) {
	for _, input := range []string{`{"hooks":{"enabled":false}}`, `{"hooks":{"enabled":null}}`, `{"hooks":{"enabled":"true"}}`, `{"hooks":{"events":{"Stop":"invalid"}}}`, `{"hooks":null}`, `null`} {
		t.Run(input, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".zcode", "config.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: dir}); err == nil {
				t.Fatal("invalid hooks accepted")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != input {
				t.Fatal("failed installation changed configuration")
			}
		})
	}
}

func TestHooksEnableNativeDefaultWithoutTrustingUserCommands(t *testing.T) {
	dir := t.TempDir()
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: dir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".zcode", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Hooks struct {
			Enabled bool                       `json:"enabled"`
			Events  map[string]json.RawMessage `json:"events"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if !config.Hooks.Enabled || len(config.Hooks.Events) != len(managedHooks) {
		t.Fatalf("AO hooks not enabled under native disabled default: %s", data)
	}
}

func TestTrustSelectsOnlyAODeclarations(t *testing.T) {
	status := trustStatus{WorkspacePath: "/workspace"}
	for i, spec := range managedHooks {
		status.Items = append(status.Items, trustItem{Event: spec.event, DisplayCommand: hookCommandPrefix + spec.eventArg, SourcePath: ".zcode/config.json", ConfiguredEnabled: true, HookDeclarationDigest: fmt.Sprintf("%064x", i+1), TrustState: "pending_trust"})
	}
	status.Items = append(status.Items, trustItem{Event: "Stop", DisplayCommand: "user-hook", SourcePath: "zcode.json", ConfiguredEnabled: true, HookDeclarationDigest: strings.Repeat("f", 64), TrustState: "pending_trust"})
	digests, err := aoHookDigests(status, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != len(managedHooks) {
		t.Fatalf("got %d grants", len(digests))
	}
	status.Items[0].SourcePath = "zcode.json"
	if _, err := aoHookDigests(status, "/workspace"); err == nil {
		t.Fatal("foreign source accepted as AO hook")
	}
	status.Items[0].SourcePath = ".zcode/config.json"
	if _, err := aoHookDigests(status, "/different"); err == nil {
		t.Fatal("different workspace accepted")
	}
	status.Items[0].HookDeclarationDigest = "invalid"
	if _, err := aoHookDigests(status, "/workspace"); err == nil {
		t.Fatal("invalid digest accepted")
	}
}
