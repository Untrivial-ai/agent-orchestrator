package accountsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceRoutesMirroredNativeCodexAccount(t *testing.T) {
	dataDir := t.TempDir()
	nativeRoot := filepath.Join(dataDir, "native")
	nativeID := "native-account-1"
	nativeDir := filepath.Join(nativeRoot, nativeID, "credential-home")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatalf("create native account: %v", err)
	}
	credential := map[string]any{
		"tokens": map[string]any{
			"account_id":    "account-provider-id",
			"access_token":  "test-token",
			"refresh_token": "refresh-token",
		},
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, "auth.json"), raw, 0o600); err != nil {
		t.Fatalf("write native credential: %v", err)
	}

	service, err := New(Options{DataDir: dataDir, NativeAccountRoot: nativeRoot})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = service.Close(context.Background()) }()

	route, err := service.RouteForSession(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("RouteForSession: %v", err)
	}
	if route.BaseURL == "" || route.Token == "" || route.TokenEnv == "" {
		t.Fatalf("incomplete route: %+v", route)
	}
	if got := service.externalAccountID("ao-native-" + nativeID + ".json"); got != nativeID {
		t.Fatalf("external account id = %q, want %q", got, nativeID)
	}
	if got, ok := service.routes.accountForSession("session-1"); !ok || got != "ao-native-"+nativeID+".json" {
		t.Fatalf("persisted proxy pin = (%q, %t)", got, ok)
	}
	mirrored, err := os.ReadFile(filepath.Join(service.authDir, "ao-native-"+nativeID+".json"))
	if err != nil {
		t.Fatalf("read mirrored credential: %v", err)
	}
	var proxyCredential map[string]string
	if err := json.Unmarshal(mirrored, &proxyCredential); err != nil {
		t.Fatalf("decode mirrored credential: %v", err)
	}
	if proxyCredential["type"] != "codex" || proxyCredential["access_token"] != "test-token" || proxyCredential["account_id"] != "account-provider-id" {
		t.Fatalf("mirrored native credential was not converted for CLIProxyAPI: %#v", proxyCredential)
	}

	selected, err := service.SwitchSessionAccount(context.Background(), "session-1", nativeID)
	if err != nil {
		t.Fatalf("SwitchSessionAccount: %v", err)
	}
	if selected != nativeID {
		t.Fatalf("selected account = %q, want %q", selected, nativeID)
	}
}

func TestServiceRemovesMirrorsWhenNativeAccountRootDisappears(t *testing.T) {
	dataDir := t.TempDir()
	nativeRoot := filepath.Join(dataDir, "native")
	nativeID := "native-account-1"
	nativeDir := filepath.Join(nativeRoot, nativeID, "credential-home")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatalf("create native account: %v", err)
	}
	credential := []byte(`{"type":"codex","access_token":"test-token"}`)
	if err := os.WriteFile(filepath.Join(nativeDir, "auth.json"), credential, 0o600); err != nil {
		t.Fatalf("write native credential: %v", err)
	}

	service, err := New(Options{DataDir: dataDir, NativeAccountRoot: nativeRoot})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = service.Close(context.Background()) }()
	if _, err := service.RouteForSession(context.Background(), "session-1"); err != nil {
		t.Fatalf("RouteForSession: %v", err)
	}
	mirror := filepath.Join(service.authDir, "ao-native-"+nativeID+".json")
	if _, err := os.Stat(mirror); err != nil {
		t.Fatalf("stat mirrored credential: %v", err)
	}
	if err := os.RemoveAll(nativeRoot); err != nil {
		t.Fatalf("remove native account root: %v", err)
	}
	if err := service.refreshAccounts(context.Background()); err != nil {
		t.Fatalf("refreshAccounts after root removal: %v", err)
	}
	if _, err := os.Stat(mirror); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mirrored credential stat error = %v, want not exists", err)
	}
	service.nativeMu.Lock()
	defer service.nativeMu.Unlock()
	if len(service.nativeRefs) != 0 {
		t.Fatalf("native refs after root removal = %#v, want empty", service.nativeRefs)
	}
}

func TestServiceDoesNotOverwriteRefreshedMirrorWhenNativeCredentialIsUnchanged(t *testing.T) {
	dataDir := t.TempDir()
	nativeRoot := filepath.Join(dataDir, "native")
	nativeID := "native-account-1"
	nativeDir := filepath.Join(nativeRoot, nativeID, "credential-home")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatalf("create native account: %v", err)
	}
	source := []byte(`{"type":"codex","access_token":"source-token"}`)
	if err := os.WriteFile(filepath.Join(nativeDir, "auth.json"), source, 0o600); err != nil {
		t.Fatalf("write native credential: %v", err)
	}
	service, err := New(Options{DataDir: dataDir, NativeAccountRoot: nativeRoot})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = service.Close(context.Background()) }()

	mirror := filepath.Join(service.authDir, "ao-native-"+nativeID+".json")
	refreshed := []byte(`{"type":"codex","access_token":"refreshed-token"}`)
	if err := os.WriteFile(mirror, refreshed, 0o600); err != nil {
		t.Fatalf("write refreshed mirror: %v", err)
	}
	if err := service.syncNativeAccounts(); err != nil {
		t.Fatalf("syncNativeAccounts: %v", err)
	}
	got, err := os.ReadFile(mirror)
	if err != nil {
		t.Fatalf("read mirrored credential: %v", err)
	}
	if string(got) != string(refreshed) {
		t.Fatalf("mirror = %s, want refreshed credential %s", got, refreshed)
	}
}
