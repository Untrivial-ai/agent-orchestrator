package agentlaunch

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

var managedEnv = map[string]string{"AO_PROXY_ENDPOINT": "http://127.0.0.1:4567/", "AO_PROXY_TICKET": "private-ticket"}

func proxyConfig(t *testing.T, args []string) map[string]string {
	t.Helper()
	values := make(map[string]string)
	if len(args)%2 != 0 {
		t.Fatalf("odd config argument count=%v", args)
	}
	for i := 0; i < len(args); i += 2 {
		key, value, ok := strings.Cut(args[i+1], "=")
		if args[i] != "-c" || !ok {
			t.Fatalf("not a config override: %q %q", args[i], args[i+1])
		}
		values[key] = value
	}
	return values
}

func TestManagedCodexLaunchUsesTheAccountHelper(t *testing.T) {
	argv := []string{"codex", "app-server", "-c", `model_provider="openai"`}
	before := slices.Clone(argv)
	got := CodexProxyArgv(argv, managedEnv)
	if !reflect.DeepEqual(argv, before) || !reflect.DeepEqual(got[:len(argv)], argv) {
		t.Fatalf("launch command changed: %v", got)
	}
	want := map[string]string{
		"model_provider":                                  `"ao-managed"`,
		"model_providers.ao-managed.name":                 `"AO Account Manager"`,
		"model_providers.ao-managed.base_url":             `"http://127.0.0.1:4567/v1"`,
		"model_providers.ao-managed.env_key":              `"AO_PROXY_TICKET"`,
		"model_providers.ao-managed.wire_api":             `"responses"`,
		"model_providers.ao-managed.requires_openai_auth": "false",
		"model_providers.ao-managed.supports_websockets":  "false",
	}
	// The overrides come last, so they win over an earlier provider choice.
	if cfg := proxyConfig(t, got[len(argv):]); !reflect.DeepEqual(cfg, want) {
		t.Fatalf("managed provider=%v", cfg)
	}
	if strings.Contains(strings.Join(got, " "), "private-ticket") {
		t.Fatal("the ticket entered the process arguments")
	}
	if CodexProxyProviderFor(managedEnv) != "ao-managed" {
		t.Fatal("a managed launch names no provider")
	}
}

func TestUnmanagedCodexLaunchIsUnchanged(t *testing.T) {
	for _, argv := range [][]string{nil, {"codex"}, {"codex", "resume", "thread-123"}} {
		for _, env := range []map[string]string{nil, {"OPENAI_API_KEY": "ambient", "CODEX_HOME": "/native"}} {
			if got := CodexProxyArgv(argv, env); !reflect.DeepEqual(got, argv) || CodexProxyProviderFor(env) != "" {
				t.Fatalf("argv=%v became %v", argv, got)
			}
		}
	}
	// A launch with only half its routing never falls back to this computer's login.
	for _, env := range []map[string]string{{"AO_PROXY_TICKET": "private"}, {"AO_PROXY_ENDPOINT": "http://127.0.0.1:4567"}} {
		if cfg := proxyConfig(t, CodexProxyArgv([]string{"codex"}, env)[1:]); cfg["model_provider"] != `"ao-managed"` {
			t.Fatalf("half-routed launch=%v", cfg)
		}
	}
}

func TestManagedCodexFlagsGoBeforeThePrompt(t *testing.T) {
	argv := []string{"codex", "--model", "gpt-example", "--", "--this-is-my-prompt"}
	got := CodexProxyArgv(argv, managedEnv)
	separator := slices.Index(got, "--")
	if separator != len(got)-2 || got[len(got)-1] != "--this-is-my-prompt" || !reflect.DeepEqual(got[:3], argv[:3]) {
		t.Fatalf("prompt corrupted=%v", got)
	}
	if cfg := proxyConfig(t, got[3:separator]); cfg["model_provider"] != `"ao-managed"` {
		t.Fatalf("config became part of the prompt=%v", got)
	}
}
