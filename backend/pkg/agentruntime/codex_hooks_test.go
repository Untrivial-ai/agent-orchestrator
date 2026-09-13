package agentruntime

import (
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCodexHookIdentity(t *testing.T) {
	for _, tc := range []struct{ command, hash string }{
		{"/usr/local/bin/ao hooks codex session-start", "sha256:a1defe9972029ce9fb4932ae43f91870e4b3e1e4a1e4734d6b459f862fb68238"},
		{`& "C:\AO tools\ao.exe" hooks codex session-start`, "sha256:4c64eb84d916b58e34f7d4cc7bd75ea9f1d3ad4a390e00d1be08211aceb1b8ec"},
		{"ao <>&\u2028\u2029 \\u2028", "sha256:8d64eb3c186d6e3fec650abe50cd3bf9226805e54cbcb34a9a5fb6a65249e866"},
	} {
		key, hash, err := codexSessionHookIdentity("SessionStart", tc.command, 5)
		if err != nil || hash != tc.hash {
			t.Fatalf("hash(%q)=%q, err=%v; want %q", tc.command, hash, err, tc.hash)
		}
		source := "/<session-flags>/config.toml"
		if runtime.GOOS == "windows" {
			source = `C:\<session-flags>\config.toml`
		}
		if key != source+":session_start:0:0" {
			t.Fatalf("unexpected approval source %q", key)
		}
		for _, changed := range []CodexHook{{"Stop", tc.command, 5}, {"SessionStart", tc.command + " changed", 5}, {"SessionStart", tc.command, 6}} {
			_, changedHash, err := codexSessionHookIdentity(changed.Event, changed.Command, changed.Timeout)
			if err != nil || changedHash == hash {
				t.Fatalf("changed hook retained approval: %+v, err=%v", changed, err)
			}
		}
	}
}

func TestCodexSessionHooksApproveOnlyExactAOEntries(t *testing.T) {
	args, err := CodexSessionHooks([]CodexHook{{"SessionStart", "ao start", 5}, {"Stop", "ao stop", 5}})
	if err != nil {
		t.Fatal(err)
	}
	var states map[string]map[string]string
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			t.Fatalf("unexpected flag %q", args[i])
		}
		var config struct {
			Hooks struct {
				State map[string]map[string]string `toml:"state"`
			} `toml:"hooks"`
		}
		if err := toml.Unmarshal([]byte(args[i+1]), &config); err != nil {
			t.Fatal(err)
		}
		if config.Hooks.State != nil {
			if states != nil {
				t.Fatal("multiple state overrides would overwrite earlier AO approvals")
			}
			states = config.Hooks.State
		}
	}
	if len(states) != 2 {
		t.Fatalf("approval count = %d, want 2", len(states))
	}
	for key, fields := range states {
		if !strings.Contains(key, "<session-flags>") || len(fields) != 1 || !strings.HasPrefix(fields["trusted_hash"], "sha256:") {
			t.Fatalf("approval changes another source or preference: %q=%v", key, fields)
		}
	}
}

func TestCodexSessionHooksRejectUnknownOrDuplicateEvents(t *testing.T) {
	for _, hooks := range [][]CodexHook{
		{{"FutureEvent", "ao start", 5}},
		{{"SessionStart", "ao start", 5}, {"SessionStart", "another command", 5}},
	} {
		if _, err := CodexSessionHooks(hooks); err == nil {
			t.Fatalf("unverified event identity accepted: %+v", hooks)
		}
	}
}
