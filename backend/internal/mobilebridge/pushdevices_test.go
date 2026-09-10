package mobilebridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPushDevicesPath(t *testing.T) {
	got := PushDevicesPath("/data")
	want := filepath.Join("/data", "mobile", "push-devices.json")
	if got != want {
		t.Fatalf("path = %q want %q", got, want)
	}
}

func TestValidPushToken(t *testing.T) {
	valid := []string{
		"ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]",
		"ExpoPushToken[abc-123_DEF]",
	}
	for _, tok := range valid {
		if !ValidPushToken(tok) {
			t.Errorf("ValidPushToken(%q) = false, want true", tok)
		}
	}
	invalid := []string{
		"",
		"garbage",
		"ExponentPushToken[]",
		"fcm:some-raw-token",
		"ExponentPushToken[abc",
	}
	for _, tok := range invalid {
		if ValidPushToken(tok) {
			t.Errorf("ValidPushToken(%q) = true, want false", tok)
		}
	}
}

func TestLoadRegistryMissingIsEmpty(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("fresh registry = %+v, want empty", got)
	}
}

func TestUpsertListDeleteRoundTrip(t *testing.T) {
	path := PushDevicesPath(t.TempDir())
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	a := PushDevice{InstallID: "inst-a", Token: "ExpoPushToken[a]", Platform: "android", DeviceName: "Pixel", CreatedAt: now, LastSeenAt: now}
	b := PushDevice{InstallID: "inst-b", Token: "ExpoPushToken[b]", Platform: "ios", DeviceName: "iPhone", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(a); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := reg.Upsert(b); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if got := reg.List(); len(got) != 2 {
		t.Fatalf("list len = %d, want 2", len(got))
	}

	// Reload from disk: the two devices must survive a restart.
	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.List(); len(got) != 2 {
		t.Fatalf("reloaded list len = %d, want 2", len(got))
	}

	if err := reg.Delete("inst-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got := reg.List()
	if len(got) != 1 || got[0].Token != "ExpoPushToken[b]" {
		t.Fatalf("after delete = %+v, want only b", got)
	}
}

func TestUpsertPreservesCreatedAt(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	first := PushDevice{InstallID: "inst-a", Token: "ExpoPushToken[a]", Platform: "android", CreatedAt: created, LastSeenAt: created}
	if err := reg.Upsert(first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	// Re-register the same token (e.g. foreground refresh) with a fresh CreatedAt;
	// the store must keep the ORIGINAL CreatedAt and only advance LastSeenAt.
	again := PushDevice{InstallID: "inst-a", Token: "ExpoPushToken[a]", Platform: "android", DeviceName: "renamed", CreatedAt: later, LastSeenAt: later}
	if err := reg.Upsert(again); err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("list len = %d, want 1 (idempotent upsert)", len(got))
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want preserved %v", got[0].CreatedAt, created)
	}
	if !got[0].LastSeenAt.Equal(later) {
		t.Fatalf("LastSeenAt = %v, want advanced %v", got[0].LastSeenAt, later)
	}
	if got[0].DeviceName != "renamed" {
		t.Fatalf("DeviceName = %q, want updated to renamed", got[0].DeviceName)
	}
}

func TestUpsertRejectsInvalidToken(t *testing.T) {
	reg, _ := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err := reg.Upsert(PushDevice{Token: "garbage"}); err == nil {
		t.Fatal("expected error upserting invalid token")
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("invalid upsert leaked a row: %+v", got)
	}
}

func TestRegistryFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows does not honor Unix file-permission bits; os.Chmod only toggles
		// the read-only flag, so Stat reports 0666. The 0600 intent is a no-op there.
		t.Skip("file mode bits are not meaningful on Windows")
	}
	path := PushDevicesPath(t.TempDir())
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "ExpoPushToken[a]", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v want 0600", info.Mode().Perm())
	}
}

func TestDeleteMissingTokenIsNoop(t *testing.T) {
	reg, _ := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err := reg.Delete("missing-install-id"); err != nil {
		t.Fatalf("delete missing install id should be a no-op, got %v", err)
	}
}

func TestUpsertKeysByInstallIDAndPreservesMute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := reg.Upsert(PushDevice{
		InstallID: "inst-1", Token: "ExponentPushToken[a]", DeviceName: "Phone",
		CreatedAt: created, LastSeenAt: created,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.SetMuted("inst-1", true); err != nil {
		t.Fatalf("mute: %v", err)
	}

	// The phone reinstalls: same install ID, brand new push token.
	later := created.Add(48 * time.Hour)
	if err := reg.Upsert(PushDevice{
		InstallID: "inst-1", Token: "ExponentPushToken[b]", DeviceName: "Phone",
		CreatedAt: later, LastSeenAt: later,
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	devices := reg.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1 (reinstall must not duplicate)", len(devices))
	}
	got := devices[0]
	if got.Token != "ExponentPushToken[b]" {
		t.Fatalf("token = %q, want the new one", got.Token)
	}
	if !got.Muted {
		t.Fatalf("mute did not survive re-registration")
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want original %v", got.CreatedAt, created)
	}
}

func TestUpsertAdoptsLegacyRowByToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	legacy := `{"devices":[{"token":"ExponentPushToken[a]","platform":"ios","deviceName":"iPhone","createdAt":"2026-01-01T00:00:00Z","lastSeenAt":"2026-01-02T00:00:00Z"}]}`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	migrated := reg.List()
	if len(migrated) != 1 || migrated[0].InstallID == "" {
		t.Fatalf("migration did not synthesize an install ID: %+v", migrated)
	}
	if err := reg.SetMuted(migrated[0].InstallID, true); err != nil {
		t.Fatalf("mute: %v", err)
	}

	// That same phone now registers with its real install ID and same token.
	if err := reg.Upsert(PushDevice{
		InstallID: "real-inst", Token: "ExponentPushToken[a]", DeviceName: "iPhone",
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	devices := reg.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1 (adoption must not duplicate)", len(devices))
	}
	if devices[0].InstallID != "real-inst" {
		t.Fatalf("InstallID = %q, want the adopted real one", devices[0].InstallID)
	}
	if !devices[0].Muted {
		t.Fatalf("adoption lost the mute flag")
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !devices[0].CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %v, want preserved %v", devices[0].CreatedAt, want)
	}
}

func TestUnregisterTokenAndDeleteByInstallID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	_ = reg.Upsert(PushDevice{InstallID: "i1", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now})
	_ = reg.Upsert(PushDevice{InstallID: "i2", Token: "ExponentPushToken[b]", CreatedAt: now, LastSeenAt: now})

	// The two removal doors differ on purpose: unregistering a token detaches the
	// registration but leaves the paired phone listed, while the desktop's Delete
	// removes the phone outright.
	if err := reg.UnregisterToken("ExponentPushToken[a]"); err != nil {
		t.Fatalf("unregister token: %v", err)
	}
	if err := reg.Delete("i2"); err != nil {
		t.Fatalf("delete by install id: %v", err)
	}

	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %+v, want only i1 surviving (tokenless)", got)
	}
	if got[0].InstallID != "i1" || got[0].Token != "" {
		t.Fatalf("device = %+v, want i1 with its token cleared", got[0])
	}
	if err := reg.Delete("missing"); err != nil {
		t.Fatalf("deleting unknown install id must be a no-op, got %v", err)
	}
}

func TestRemovedDeviceReturnsMuted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-1", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.Delete("inst-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if !got[0].Muted {
		t.Fatalf("device returned after removal is not muted: %+v", got[0])
	}
}

func TestRemovedDeviceMatchedByTokenReturnsMuted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-1", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.Delete("inst-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// A reinstall looks like the same push token under a brand new install ID.
	reinstalled := PushDevice{InstallID: "inst-2", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(reinstalled); err != nil {
		t.Fatalf("upsert reinstalled: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if !got[0].Muted {
		t.Fatalf("reinstalled device matched by token is not muted: %+v", got[0])
	}
}

func TestTombstoneConsumedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-1", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.Delete("inst-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := reg.List(); len(got) != 1 || !got[0].Muted {
		t.Fatalf("expected device muted after removal, got %+v", got)
	}
	if err := reg.SetMuted("inst-1", false); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	// A further registration must NOT be re-muted: the tombstone was already
	// consumed by the previous match. A surviving tombstone would silently
	// override the user's deliberate unmute.
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if got[0].Muted {
		t.Fatalf("tombstone re-muted a device the user had unmuted: %+v", got[0])
	}
}

func TestUnregisterTokenWritesNoTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-1", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.UnregisterToken("ExponentPushToken[a]"); err != nil {
		t.Fatalf("delete by token: %v", err)
	}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if got[0].Muted {
		t.Fatalf("UnregisterToken must not write a tombstone, but device came back muted: %+v", got[0])
	}
}

func TestTombstonesPersistAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-1", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.Delete("inst-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := reloaded.Upsert(dev); err != nil {
		t.Fatalf("re-upsert after reload: %v", err)
	}
	got := reloaded.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if !got[0].Muted {
		t.Fatalf("tombstone did not survive reload: %+v", got[0])
	}
}

// TestTokenlessDeviceSurvivesReload pins the fix for a daemon-restart bug: a
// paired phone with no push token (permission not granted, or a build that
// can't mint one) must survive a LoadRegistry reload with its identity
// intact, not be silently dropped because it has no token.
func TestTokenlessDeviceSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dev := PushDevice{InstallID: "inst-a", Token: "", Platform: "ios", DeviceName: "iPhone", CreatedAt: created, LastSeenAt: created}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1 (tokenless row must survive reload)", len(got))
	}
	if got[0].InstallID != "inst-a" {
		t.Fatalf("InstallID = %q, want inst-a preserved", got[0].InstallID)
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want preserved %v (not reset on reload)", got[0].CreatedAt, created)
	}
}

// TestTokenlessMutedDeviceSurvivesReloadStillMuted covers the Task 10
// interaction: a removed-then-reannounced tokenless device comes back muted,
// and that mute must survive a daemon restart — otherwise the muted row is
// dropped on reload, a later tokenless announce returns unmuted, and a
// removed phone starts receiving pushes again once it later gets a token.
func TestTokenlessMutedDeviceSurvivesReloadStillMuted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-a", Token: "", Platform: "ios", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.Delete("inst-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Phone re-announces (still tokenless): the tombstone mutes and consumes it.
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := reg.List(); len(got) != 1 || !got[0].Muted {
		t.Fatalf("expected muted tokenless device before reload, got %+v", got)
	}

	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if !got[0].Muted {
		t.Fatalf("Muted did not survive reload for a tokenless device: %+v", got[0])
	}
}

// TestRowWithNeitherTokenNorInstallIDIsDroppedOnLoad pins the deliberate
// exception: a row with no token AND no install ID carries no identity to
// preserve or synthesize, so it must be dropped rather than kept (which would
// leave a permanently-unaddressable row) or re-synthesized every reload
// (which would multiply it). Two reload cycles must not accumulate rows.
func TestRowWithNeitherTokenNorInstallIDIsDroppedOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	raw := `{"devices":[{"platform":"ios","deviceName":"ghost","createdAt":"2026-01-01T00:00:00Z","lastSeenAt":"2026-01-02T00:00:00Z"}]}`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("devices = %+v, want empty (identity-less row must be dropped)", got)
	}

	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.List(); len(got) != 0 {
		t.Fatalf("devices = %+v, want still empty after a second reload cycle (must not accumulate)", got)
	}
}

func TestUpsertAcceptsDeviceWithoutToken(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	dev := PushDevice{InstallID: "inst-a", Token: "", Platform: "ios", DeviceName: "iPhone", CreatedAt: now, LastSeenAt: now}
	if err := reg.Upsert(dev); err != nil {
		t.Fatalf("upsert without token: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("list len = %d, want 1", len(got))
	}
	if got[0].InstallID != "inst-a" || got[0].Token != "" {
		t.Fatalf("stored device = %+v, want tokenless inst-a", got[0])
	}
}

func TestUpsertStillRejectsMalformedToken(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "garbage"}); err == nil {
		t.Fatal("expected error upserting malformed token")
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("malformed upsert leaked a row: %+v", got)
	}
}

func TestTokenAttachesToExistingIdentityRow(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	// The phone announces its identity with no token (permission not yet granted).
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "", Platform: "ios", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatalf("announce: %v", err)
	}
	later := now.Add(time.Minute)
	// Permission is granted; the same install ID registers WITH a token.
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "ExponentPushToken[a]", Platform: "ios", CreatedAt: later, LastSeenAt: later}); err != nil {
		t.Fatalf("register with token: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1 (token must attach to the same row)", len(got))
	}
	if got[0].InstallID != "inst-a" || got[0].Token != "ExponentPushToken[a]" {
		t.Fatalf("device = %+v, want inst-a with the new token", got[0])
	}
}

func TestAnnounceDoesNotBlankExistingToken(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "ExponentPushToken[a]", Platform: "ios", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatalf("register with token: %v", err)
	}
	later := now.Add(time.Minute)
	// A later announce (e.g. app foregrounded) carries no token at all.
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "", Platform: "ios", CreatedAt: later, LastSeenAt: later}); err != nil {
		t.Fatalf("announce: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1", len(got))
	}
	if got[0].Token != "ExponentPushToken[a]" {
		t.Fatalf("token = %q, want the original token to survive a tokenless announce", got[0].Token)
	}
}

func TestTokenlessAnnounceDoesNotAdoptByToken(t *testing.T) {
	reg, err := LoadRegistry(PushDevicesPath(t.TempDir()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	if err := reg.Upsert(PushDevice{InstallID: "inst-a", Token: "", Platform: "ios", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatalf("announce a: %v", err)
	}
	if err := reg.Upsert(PushDevice{InstallID: "inst-b", Token: "", Platform: "android", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatalf("announce b: %v", err)
	}
	got := reg.List()
	if len(got) != 2 {
		t.Fatalf("devices = %d, want 2 (two distinct tokenless phones must not merge)", len(got))
	}
}

func TestTombstonesAreCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	const total = 60
	for i := 0; i < total; i++ {
		installID := fmt.Sprintf("inst-%03d", i)
		token := fmt.Sprintf("ExponentPushToken[%03d]", i)
		if err := reg.Upsert(PushDevice{InstallID: installID, Token: token, CreatedAt: now, LastSeenAt: now}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
		if err := reg.Delete(installID); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var file pushDevicesFile
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(file.Removed) != maxTombstones {
		t.Fatalf("tombstones = %d, want %d", len(file.Removed), maxTombstones)
	}
	// The oldest (inst-000..inst-009) must have been dropped; the newest
	// (inst-050..inst-059) must remain.
	seen := map[string]bool{}
	for _, r := range file.Removed {
		seen[r.InstallID] = true
	}
	for i := 0; i < total-maxTombstones; i++ {
		installID := fmt.Sprintf("inst-%03d", i)
		if seen[installID] {
			t.Fatalf("oldest tombstone %q should have been dropped", installID)
		}
	}
	for i := total - maxTombstones; i < total; i++ {
		installID := fmt.Sprintf("inst-%03d", i)
		if !seen[installID] {
			t.Fatalf("newest tombstone %q should have been kept", installID)
		}
	}
}

// A phone upgrading to a build that sends an install id reaches the daemon under
// two identities: the legacy row that owns its push token, and its real install
// id. PushManager announces (tokenless) and registers (with token) from separate
// effects, so the announce can land first and create the real-id row before the
// token ever arrives. Both identities must end up as ONE row: two rows sharing a
// token would double every notification — the exact defect install ids exist to
// prevent.
func TestLegacyRowMergesWhenAnnounceLandsBeforeRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	token := "ExponentPushToken[abc]"
	legacyCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)

	if err := reg.Upsert(PushDevice{
		InstallID: "legacy-old", Token: token, DeviceName: "Phone", Platform: "android",
		CreatedAt: legacyCreated, LastSeenAt: legacyCreated,
	}); err != nil {
		t.Fatalf("legacy upsert: %v", err)
	}
	if err := reg.Upsert(PushDevice{
		InstallID: "real-id", DeviceName: "Phone", Platform: "android",
		CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if err := reg.Upsert(PushDevice{
		InstallID: "real-id", Token: token, DeviceName: "Phone", Platform: "android",
		CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	devices := reg.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1 (the legacy row must be merged, not left behind): %+v", len(devices), devices)
	}
	got := devices[0]
	if got.InstallID != "real-id" {
		t.Fatalf("InstallID = %q, want the real install id to win", got.InstallID)
	}
	if got.Token != token {
		t.Fatalf("Token = %q, want %q", got.Token, token)
	}
	if !got.CreatedAt.Equal(legacyCreated) {
		t.Fatalf("CreatedAt = %v, want the older legacy value %v (paired-since must not jump forward)", got.CreatedAt, legacyCreated)
	}
}

// Mute is sticky across a merge: if EITHER identity was muted the surviving row
// stays muted. A merge must never silently re-enable notifications the user
// turned off.
func TestMergePreservesMuteFromEitherIdentity(t *testing.T) {
	token := "ExponentPushToken[abc]"
	now := time.Now().UTC()

	for _, tc := range []struct{ name, muteTarget string }{
		{"legacy row muted", "legacy-old"},
		{"install-id row muted", "real-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "push-devices.json")
			reg, _ := LoadRegistry(path)
			_ = reg.Upsert(PushDevice{InstallID: "legacy-old", Token: token, CreatedAt: now, LastSeenAt: now})
			_ = reg.Upsert(PushDevice{InstallID: "real-id", CreatedAt: now, LastSeenAt: now})
			if err := reg.SetMuted(tc.muteTarget, true); err != nil {
				t.Fatalf("mute %s: %v", tc.muteTarget, err)
			}

			if err := reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now}); err != nil {
				t.Fatalf("register: %v", err)
			}

			devices := reg.List()
			if len(devices) != 1 {
				t.Fatalf("devices = %d, want 1: %+v", len(devices), devices)
			}
			if !devices[0].Muted {
				t.Fatalf("merged row is unmuted; muting %s was silently discarded", tc.muteTarget)
			}
		})
	}
}

// No row may ever share a push token with another row, whatever order the
// announce and registration arrive in.
func TestNoTwoRowsEverShareAToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	token := "ExponentPushToken[abc]"

	_ = reg.Upsert(PushDevice{InstallID: "legacy-old", Token: token, CreatedAt: now, LastSeenAt: now})
	_ = reg.Upsert(PushDevice{InstallID: "real-id", CreatedAt: now, LastSeenAt: now})
	_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
	_ = reg.Upsert(PushDevice{InstallID: "third", Token: token, CreatedAt: now, LastSeenAt: now})

	seen := map[string]string{}
	for _, d := range reg.List() {
		if d.Token == "" {
			continue
		}
		if prev, dup := seen[d.Token]; dup {
			t.Fatalf("token %s held by both %q and %q", d.Token, prev, d.InstallID)
		}
		seen[d.Token] = d.InstallID
	}
}

// Losing a push token must not unpair the phone. A row represents a paired
// phone, so switching notifications off in the mobile app (or Expo reporting the
// token dead) leaves the device listed as "notifications off" — keeping its
// mute state and paired-since — instead of vanishing from the roster until it
// happens to announce again.
func TestUnregisterTokenKeepsThePairedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	token := "ExponentPushToken[a]"

	if err := reg.Upsert(PushDevice{
		InstallID: "real-id", Token: token, DeviceName: "Phone",
		CreatedAt: created, LastSeenAt: created,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.SetMuted("real-id", true); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if err := reg.UnregisterToken(token); err != nil {
		t.Fatalf("unregister: %v", err)
	}

	devices := reg.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want the paired phone to survive: %+v", len(devices), devices)
	}
	got := devices[0]
	if got.Token != "" {
		t.Fatalf("Token = %q, want cleared", got.Token)
	}
	if got.InstallID != "real-id" || got.DeviceName != "Phone" {
		t.Fatalf("identity lost: %+v", got)
	}
	if !got.Muted {
		t.Fatalf("mute lost when the token was unregistered: %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want preserved %v", got.CreatedAt, created)
	}
}

// A synthesized install ID is a placeholder, not an identity the phone knows.
// Once its token is gone the row can never be reclaimed by any announce, so it
// is deleted rather than stranded as a permanently unreachable tokenless row.
func TestUnregisterTokenDropsSynthesizedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	token := "ExponentPushToken[a]"

	if err := reg.Upsert(PushDevice{
		InstallID: LegacyInstallIDPrefix + "synthesized", Token: token,
		CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := reg.UnregisterToken(token); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("devices = %+v, want the placeholder row dropped", got)
	}
}

// The cleared token must survive a reload: a restart cannot resurrect a
// registration the phone already gave up, which would resume delivery.
func TestUnregisterTokenPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	token := "ExponentPushToken[a]"

	_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
	_ = reg.UnregisterToken(token)

	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	devices := reloaded.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1 after reload: %+v", len(devices), devices)
	}
	if devices[0].Token != "" {
		t.Fatalf("Token = %q after reload, want it to stay cleared", devices[0].Token)
	}
}

func TestUnregisterUnknownOrEmptyTokenIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now})

	if err := reg.UnregisterToken("ExponentPushToken[missing]"); err != nil {
		t.Fatalf("unknown token: %v", err)
	}
	if err := reg.UnregisterToken(""); err != nil {
		t.Fatalf("empty token: %v", err)
	}
	got := reg.List()
	if len(got) != 1 || got[0].Token != "ExponentPushToken[a]" {
		t.Fatalf("an unrelated row was touched: %+v", got)
	}
}

// Dead-token pruning goes through the same door, so a phone whose token Expo
// reports as dead stays paired and listed rather than disappearing from the
// desktop's roster.
func TestDeadTokenPruningKeepsThePairedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	token := "ExponentPushToken[dead]"
	_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, DeviceName: "Phone", CreatedAt: now, LastSeenAt: now})

	// What dispatcher.prune does when Expo answers DeviceNotRegistered.
	if err := reg.UnregisterToken(token); err != nil {
		t.Fatalf("prune: %v", err)
	}

	got := reg.List()
	if len(got) != 1 || got[0].InstallID != "real-id" || got[0].Token != "" {
		t.Fatalf("devices = %+v, want the phone still paired with no token", got)
	}
}

// SetMuted must tell an unknown device apart from a failed write. Reporting a
// disk fault as "unknown device" sends the user looking for a device that is
// plainly listed, and buries a real fault.
func TestSetMutedDistinguishesUnknownDeviceFromPersistFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not block writes on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mobile", "push-devices.json")
	reg, _ := LoadRegistry(path)
	now := time.Now().UTC()
	if err := reg.Upsert(PushDevice{InstallID: "real-id", Token: "ExponentPushToken[a]", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := reg.SetMuted("nope", true); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("unknown device error = %v, want ErrDeviceNotFound", err)
	}

	// Make the registry directory unwritable so the atomic write cannot land.
	regDir := filepath.Dir(path)
	if err := os.Chmod(regDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(regDir, 0o700) })

	err := reg.SetMuted("real-id", true)
	if err == nil {
		t.Fatal("expected an error when the registry cannot be written")
	}
	if errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("persist failure reported as ErrDeviceNotFound: %v", err)
	}

	// The rollback matters: dispatch must not honour a mute the caller was told
	// had failed, which would silently suppress notifications until restart.
	devices := reg.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %+v", devices)
	}
	if devices[0].Muted {
		t.Fatal("in-memory device is muted after the write failed; memory and disk have diverged")
	}
}

// Every registry mutation must be all-or-nothing with the file write. If the
// save fails, the running daemon has to keep behaving exactly as the file says —
// otherwise the API reports failure while memory quietly diverges, and a restart
// silently reverts whatever the user thought had happened.
func TestEveryMutationRollsBackWhenTheWriteFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not block writes on Windows")
	}
	token := "ExponentPushToken[a]"

	// setup seeds one device and returns the registry plus its directory.
	setup := func(t *testing.T) (*DeviceRegistry, string) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "mobile", "push-devices.json")
		reg, err := LoadRegistry(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		now := time.Now().UTC()
		if err := reg.Upsert(PushDevice{InstallID: "real-id", Token: token, DeviceName: "Phone", CreatedAt: now, LastSeenAt: now}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		regDir := filepath.Dir(path)
		if err := os.Chmod(regDir, 0o500); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(regDir, 0o700) })
		return reg, regDir
	}

	t.Run("Upsert", func(t *testing.T) {
		reg, _ := setup(t)
		now := time.Now().UTC()
		if err := reg.Upsert(PushDevice{InstallID: "second-id", Token: "ExponentPushToken[b]", CreatedAt: now, LastSeenAt: now}); err == nil {
			t.Fatal("expected the write to fail")
		}
		if got := reg.List(); len(got) != 1 || got[0].InstallID != "real-id" {
			t.Fatalf("memory kept a device the write never saved: %+v", got)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		reg, _ := setup(t)
		if err := reg.Delete("real-id"); err == nil {
			t.Fatal("expected the write to fail")
		}
		if got := reg.List(); len(got) != 1 {
			t.Fatalf("device removed from memory despite the failed write: %+v", got)
		}
	})

	t.Run("Delete leaves no tombstone", func(t *testing.T) {
		reg, regDir := setup(t)
		_ = reg.Delete("real-id")
		// Restore writability, then re-register: a tombstone from the failed
		// delete would silently mute a device the user never successfully removed.
		if err := os.Chmod(regDir, 0o700); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		now := time.Now().UTC()
		if err := reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now}); err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if got := reg.List(); got[0].Muted {
			t.Fatal("a tombstone survived a failed Delete and muted the device")
		}
	})

	t.Run("UnregisterToken", func(t *testing.T) {
		reg, _ := setup(t)
		if err := reg.UnregisterToken(token); err == nil {
			t.Fatal("expected the write to fail")
		}
		if got := reg.List(); len(got) != 1 || got[0].Token != token {
			t.Fatalf("token cleared in memory despite the failed write: %+v", got)
		}
	})

	t.Run("SetMuted", func(t *testing.T) {
		reg, _ := setup(t)
		if err := reg.SetMuted("real-id", true); err == nil {
			t.Fatal("expected the write to fail")
		}
		if got := reg.List(); got[0].Muted {
			t.Fatalf("device muted in memory despite the failed write: %+v", got)
		}
	})
}

// Unpairing and switching notifications off are different intents and must have
// different outcomes: a phone that turns notifications off is still paired and
// belongs in the roster; a phone that forgets this server is gone and must not
// be left behind on the old desktop.
func TestUnpairRemovesTheRowWhileUnregisterKeepsIt(t *testing.T) {
	token := "ExponentPushToken[a]"

	t.Run("notifications off keeps the phone", func(t *testing.T) {
		reg, _ := LoadRegistry(filepath.Join(t.TempDir(), "d.json"))
		now := time.Now().UTC()
		_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
		if err := reg.UnregisterToken(token); err != nil {
			t.Fatalf("unregister: %v", err)
		}
		got := reg.List()
		if len(got) != 1 || got[0].Token != "" {
			t.Fatalf("want the phone still paired with no token, got %+v", got)
		}
	})

	t.Run("forget server removes the phone by install id", func(t *testing.T) {
		reg, _ := LoadRegistry(filepath.Join(t.TempDir(), "d.json"))
		now := time.Now().UTC()
		_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
		if err := reg.Unpair("real-id"); err != nil {
			t.Fatalf("unpair: %v", err)
		}
		if got := reg.List(); len(got) != 0 {
			t.Fatalf("want the phone gone, got %+v", got)
		}
	})

	t.Run("forget server works by token for older builds", func(t *testing.T) {
		reg, _ := LoadRegistry(filepath.Join(t.TempDir(), "d.json"))
		now := time.Now().UTC()
		_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
		if err := reg.Unpair(token); err != nil {
			t.Fatalf("unpair by token: %v", err)
		}
		if got := reg.List(); len(got) != 0 {
			t.Fatalf("want the phone gone, got %+v", got)
		}
	})

	t.Run("unpair touches only the matching phone", func(t *testing.T) {
		reg, _ := LoadRegistry(filepath.Join(t.TempDir(), "d.json"))
		now := time.Now().UTC()
		_ = reg.Upsert(PushDevice{InstallID: "phone-a", Token: token, CreatedAt: now, LastSeenAt: now})
		_ = reg.Upsert(PushDevice{InstallID: "phone-b", Token: "ExponentPushToken[b]", CreatedAt: now, LastSeenAt: now})
		_ = reg.Unpair("phone-a")
		got := reg.List()
		if len(got) != 1 || got[0].InstallID != "phone-b" {
			t.Fatalf("want only phone-b left, got %+v", got)
		}
	})

	t.Run("unpair writes no tombstone", func(t *testing.T) {
		// The phone asked to leave, so a deliberate re-pair should come back
		// clean. Tombstones exist to re-mute a device the DESKTOP removed.
		reg, _ := LoadRegistry(filepath.Join(t.TempDir(), "d.json"))
		now := time.Now().UTC()
		_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
		_ = reg.Unpair("real-id")
		_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
		got := reg.List()
		if len(got) != 1 || got[0].Muted {
			t.Fatalf("a re-paired phone came back muted: %+v", got)
		}
	})

	t.Run("unknown or empty id is a no-op", func(t *testing.T) {
		reg, _ := LoadRegistry(filepath.Join(t.TempDir(), "d.json"))
		now := time.Now().UTC()
		_ = reg.Upsert(PushDevice{InstallID: "real-id", Token: token, CreatedAt: now, LastSeenAt: now})
		if err := reg.Unpair("nobody"); err != nil {
			t.Fatalf("unknown: %v", err)
		}
		if err := reg.Unpair(""); err != nil {
			t.Fatalf("empty: %v", err)
		}
		if got := reg.List(); len(got) != 1 {
			t.Fatalf("an unrelated row was touched: %+v", got)
		}
	})
}
