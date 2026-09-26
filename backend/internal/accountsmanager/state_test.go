package accountsmanager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRouteStatePersistsPinsWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	state, err := newRouteState(path)
	if err != nil {
		t.Fatalf("newRouteState: %v", err)
	}
	if err := state.setAccountForSession("session-1", "proxy-account-1"); err != nil {
		t.Fatalf("setAccountForSession: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	if string(raw) == "" || string(raw) == "{}" {
		t.Fatalf("persisted state is empty: %s", raw)
	}
	if string(raw) != `{
  "sessions": {
    "session-1": "proxy-account-1"
  }
}` {
		t.Fatalf("unexpected persisted state: %s", raw)
	}
	reloaded, err := newRouteState(path)
	if err != nil {
		t.Fatalf("reload route state: %v", err)
	}
	if got, ok := reloaded.accountForSession("session-1"); !ok || got != "proxy-account-1" {
		t.Fatalf("reloaded pin = (%q, %t), want proxy-account-1", got, ok)
	}
}

func TestRouteStateProtectsExistingFileOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	state, err := newRouteState(path)
	if err != nil {
		t.Fatalf("newRouteState: %v", err)
	}
	if err := state.setAccountForSession("session-1", "proxy-account-1"); err != nil {
		t.Fatalf("setAccountForSession: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("relax route state permissions: %v", err)
	}
	if _, err := newRouteState(path); err != nil {
		t.Fatalf("reload route state: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat route state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("route state permissions = %o, want 600", got)
	}
}

func TestRouteStateGlobalSwitchPreservesExistingSessionsAndUpdatesFutureDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	state, err := newRouteState(path)
	if err != nil {
		t.Fatalf("newRouteState: %v", err)
	}
	for _, sessionID := range []string{"session-1", "session-2"} {
		if err := state.setAccountForSession(sessionID, "proxy-account-1"); err != nil {
			t.Fatalf("setAccountForSession(%s): %v", sessionID, err)
		}
	}
	if err := state.setAccountForAllSessions("proxy-account-2"); err != nil {
		t.Fatalf("setAccountForAllSessions: %v", err)
	}
	for _, sessionID := range []string{"session-1", "session-2"} {
		if got, ok := state.accountForSession(sessionID); !ok || got != "proxy-account-1" {
			t.Fatalf("session %s pin = (%q, %t), want proxy-account-1", sessionID, got, ok)
		}
	}
	if got, ok := state.activeAccountID(); !ok || got != "proxy-account-2" {
		t.Fatalf("active account = (%q, %t), want proxy-account-2", got, ok)
	}

	reloaded, err := newRouteState(path)
	if err != nil {
		t.Fatalf("reload route state: %v", err)
	}
	if got, ok := reloaded.activeAccountID(); !ok || got != "proxy-account-2" {
		t.Fatalf("reloaded active account = (%q, %t), want proxy-account-2", got, ok)
	}
}

func TestRouteStateDoesNotMutateWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatalf("write persistence blocker: %v", err)
	}
	state := &routeState{path: filepath.Join(parent, "routes.json"), sessions: make(map[string]string)}
	if err := state.setAccountForSession("session-1", "proxy-account-1"); err == nil {
		t.Fatal("setAccountForSession unexpectedly succeeded")
	}
	if _, ok := state.accountForSession("session-1"); ok {
		t.Fatal("failed persistence mutated in-memory route state")
	}
}
