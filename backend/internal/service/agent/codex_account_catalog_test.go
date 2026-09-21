package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const testAccountID = "72d4db6e-da2c-414c-a6a9-fdbd09a006b6"

// snapshots is test-only inspection. Production callers use the catalog's
// scoped read methods instead of materializing every account snapshot.
func (c *codexAccountCatalog) snapshots() []domain.CodexAccountSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	records := c.sortedRecordsLocked()
	out := make([]domain.CodexAccountSnapshot, 0, len(records))
	for _, record := range records {
		out = append(out, record.Snapshot)
	}
	return out
}

func commitTestAccount(t *testing.T, catalog *codexAccountCatalog, pendingRoot, operationID string, observed ports.CodexAccountObservation) codexAccountRecord {
	t.Helper()
	return commitTestAccountWithCredential(t, catalog, pendingRoot, operationID, []byte("opaque-codex-credential\x00\xff"), observed)
}

func commitTestAccountWithCredential(t *testing.T, catalog *codexAccountCatalog, pendingRoot, operationID string, credential []byte, observed ports.CodexAccountObservation) codexAccountRecord {
	t.Helper()
	pendingDir, home, err := createPendingCredentialHome(pendingRoot, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(home, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	record, err := catalog.commitPending(pendingDir, observed)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func testOAuthCredential(accountID, accessToken string) []byte {
	data, err := json.Marshal(map[string]any{
		"tokens": map[string]string{
			"account_id":    accountID,
			"access_token":  accessToken,
			"refresh_token": "refresh-" + accessToken,
		},
	})
	if err != nil {
		panic(err)
	}
	return data
}

func testAPIKeyCredential(key string) []byte {
	data, err := json.Marshal(map[string]string{"OPENAI_API_KEY": key})
	if err != nil {
		panic(err)
	}
	return data
}

func TestParseCodexCredentialIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		credential []byte
		method     domain.CodexAuthMethod
		accountID  string
		wantError  bool
	}{
		"oauth account id": {credential: testOAuthCredential("account-123", "access"), method: domain.CodexAuthMethodChatGPT, accountID: "account-123"},
		"legacy oauth":     {credential: []byte(`{"tokens":{"access_token":"access"}}`), method: domain.CodexAuthMethodChatGPT},
		"api key":          {credential: testAPIKeyCredential("api-key"), method: domain.CodexAuthMethodAPIKey},
		"malformed":        {credential: []byte(`{"tokens":`), wantError: true},
		"unsupported":      {credential: []byte(`{}`), wantError: true},
	} {
		t.Run(name, func(t *testing.T) {
			identity, err := parseCodexCredentialIdentity(tc.credential)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			if identity.Method != tc.method || identity.ProviderAccountID != tc.accountID {
				t.Fatalf("safe identity fields = (%q, %q)", identity.Method, identity.ProviderAccountID)
			}
			if name == "api key" && identity.APIKey != "api-key" {
				t.Fatal("API key was not retained for in-memory matching")
			}
		})
	}
}

func TestCodexAccountCatalogPersistsSafeProviderAccountID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "access"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationUnknown,
		Method:         domain.CodexAuthMethodChatGPT,
	})
	if record.ProviderAccountID != "provider-account" {
		t.Fatalf("provider account id = %q", record.ProviderAccountID)
	}
	descriptor, err := readCodexAccountDescriptor(filepath.Join(root, testAccountID, codexAccountDescriptorFilename))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Version != codexAccountVersion || descriptor.ProviderAccountID != "provider-account" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
}

func TestCodexAccountCatalogLazilyUpgradesLegacyDescriptorIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	catalog.newID = func() string { return testAccountID }
	credential := testOAuthCredential("provider-account", "access")
	record := commitTestAccountWithCredential(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.CodexAccountObservation{Method: domain.CodexAuthMethodChatGPT})
	descriptorPath := filepath.Join(root, testAccountID, codexAccountDescriptorFilename)
	descriptor, err := readCodexAccountDescriptor(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Version = 1
	descriptor.ProviderAccountID = ""
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(descriptorPath, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.updateCredentialIdentity(context.Background(), record.Snapshot.ID, credential); err != nil {
		t.Fatal(err)
	}
	upgraded, err := readCodexAccountDescriptor(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Version != codexAccountVersion || upgraded.ProviderAccountID != "provider-account" {
		t.Fatalf("upgraded descriptor = %#v", upgraded)
	}
}

func TestCodexAccountCatalogCommitsStrictPrivateOpaqueSlot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	catalog.newID = func() string { return testAccountID }
	catalog.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	email := "person@example.com"
	record := commitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	if record.Snapshot.ID != testAccountID || record.Snapshot.Label != email || record.Snapshot.Status != domain.CodexAccountStatusValid {
		t.Fatalf("account = %#v", record.Snapshot)
	}
	accountDir := filepath.Join(root, testAccountID)
	for path, want := range map[string]os.FileMode{
		accountDir: 0o700,
		filepath.Join(accountDir, codexCredentialHomeDirectory):                          0o700,
		filepath.Join(accountDir, codexAccountDescriptorFilename):                        0o600,
		filepath.Join(accountDir, codexCredentialHomeDirectory, codexCredentialFilename): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(accountDir, codexAccountDescriptorFilename))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	if len(descriptor) != 7 || descriptor["id"] != testAccountID || descriptor["accountEmail"] != email {
		t.Fatalf("descriptor = %s", data)
	}
	for _, forbidden := range []string{"credential", "token", "plan", "capacity", "usage", "authUrl"} {
		if _, exists := descriptor[forbidden]; exists {
			t.Errorf("descriptor contains forbidden field %q", forbidden)
		}
	}
}

func TestCodexAccountCatalogAllowsDuplicateEmailAndOrdersByCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	ids := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	times := []time.Time{
		time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
	index := 0
	catalog.newID = func() string { return ids[index] }
	catalog.now = func() time.Time { return times[index] }
	email := "same@example.com"
	first := commitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email})
	index++
	second := commitTestAccount(t, catalog, pending, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email})
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	snapshots := catalog.snapshots()
	if len(snapshots) != 2 || snapshots[0].ID != second.Snapshot.ID || snapshots[1].ID != first.Snapshot.ID {
		t.Fatalf("ordered accounts = %#v", snapshots)
	}
	if err := os.RemoveAll(filepath.Join(root, second.Snapshot.ID)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := catalog.snapshots(); len(got) != 1 || got[0].ID != first.Snapshot.ID {
		t.Fatalf("accounts after rediscovery = %#v", got)
	}
}

func TestCodexAccountCatalogSurfacesUnsafeAndMalformedSlotsWithoutMetadataLeak(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	if err := ensurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	id := testAccountID
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := `{"version":1,"id":"` + id + `","source":"managed","authMethod":"chatgpt","accountEmail":"secret@example.com","createdAt":"2026-08-31T12:00:00Z","verifiedAt":"2026-08-31T12:00:00Z","token":"secret"}`
	if err := os.WriteFile(filepath.Join(dir, codexAccountDescriptorFilename), []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := newCodexAccountCatalog(root, nil)
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	got := catalog.snapshots()
	if len(got) != 1 || got[0].Status != domain.CodexAccountStatusBroken || got[0].ReasonCode != domain.CodexAccountReasonDescriptorInvalid {
		t.Fatalf("broken account = %#v", got)
	}
	if got[0].Label == "secret@example.com" || got[0].AccountEmail != nil {
		t.Fatalf("malformed descriptor metadata leaked: %#v", got[0])
	}
}

func TestCodexAccountCatalogRejectsSymlinkedCredentialHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey})
	home := filepath.Join(root, record.Snapshot.ID, codexCredentialHomeDirectory)
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), home); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	broken, _ := catalog.record(record.Snapshot.ID)
	if broken.Snapshot.Status != domain.CodexAccountStatusBroken || broken.Snapshot.ReasonCode != domain.CodexAccountReasonUnsafePath {
		t.Fatalf("symlinked account = %#v", broken.Snapshot)
	}
}

func TestCodexAccountCatalogRetainsSignedOutSlotAndReplacesItsCredential(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	catalog.newID = func() string { return testAccountID }
	email := "person@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	record := commitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)

	signedOut, err := catalog.markSignedOut(record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signedOut.Snapshot.Status != domain.CodexAccountStatusSignedOut || signedOut.Snapshot.Label != email || signedOut.Snapshot.Authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("signed-out account = %#v", signedOut.Snapshot)
	}
	if _, err := os.Stat(filepath.Join(record.Home, codexCredentialFilename)); !os.IsNotExist(err) {
		t.Fatalf("signed-out credential still exists: %v", err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	rediscovered, _ := catalog.record(record.Snapshot.ID)
	if rediscovered.Snapshot.Status != domain.CodexAccountStatusSignedOut {
		t.Fatalf("rediscovered account = %#v", rediscovered.Snapshot)
	}

	reauthenticated, err := catalog.replaceCredential(context.Background(), record.Snapshot.ID, []byte("replacement-opaque-credential"), observation)
	if err != nil {
		t.Fatal(err)
	}
	if reauthenticated.Snapshot.ID != record.Snapshot.ID || reauthenticated.Snapshot.Status != domain.CodexAccountStatusValid || reauthenticated.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("reauthenticated account = %#v", reauthenticated.Snapshot)
	}
	if snapshots := catalog.snapshots(); len(snapshots) != 1 {
		t.Fatalf("reauthentication created a duplicate account: %#v", snapshots)
	}
}

func TestCodexAccountCatalogDeletesSignedOutSlot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newCodexAccountCatalog(root, nil)
	catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	})
	if _, err := catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	providerCacheDir := filepath.Join(record.Home, "skills", ".system")
	if err := os.MkdirAll(providerCacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.Home, "models_cache.json"), []byte("provider cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed := make(chan []string, 1)
	catalog.setOnRemoved(func(ids []string) { removed <- ids })
	if err := catalog.deleteSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}

	if _, ok := catalog.record(record.Snapshot.ID); ok {
		t.Fatal("deleted account remains in the catalog")
	}
	if _, err := os.Stat(filepath.Join(root, record.Snapshot.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted account directory still exists: %v", err)
	}
	select {
	case ids := <-removed:
		if len(ids) != 1 || ids[0] != record.Snapshot.ID {
			t.Fatalf("removed account IDs = %#v", ids)
		}
	default:
		t.Fatal("account removal callback was not delivered")
	}
}
