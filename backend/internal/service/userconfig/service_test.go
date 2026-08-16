package userconfig_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/userconfig"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func newManager(t *testing.T) userconfig.Manager {
	t.Helper()
	store, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return userconfig.New(store)
}

func TestUserService_GetEmptyIsZero(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()

	// No user has set a config: Get returns zero, never an error.
	got, err := m.Get(ctx)
	if err != nil {
		t.Fatalf("Get on empty: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("Get on empty = %+v, want zero", got)
	}
}

func TestUserService_SetThenGet(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()

	in := userconfig.SetUserConfigInput{
		AgentConfig: domain.AgentConfig{Model: "claude-opus-4-8", Permissions: domain.PermissionModeAuto},
	}
	got, err := m.Set(ctx, in)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got != in.AgentConfig {
		t.Fatalf("Set returned %+v, want %+v", got, in.AgentConfig)
	}

	stored, err := m.Get(ctx)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if stored != in.AgentConfig {
		t.Fatalf("stored = %+v, want %+v", stored, in.AgentConfig)
	}
}

func TestUserService_SetZeroClears(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()

	if _, err := m.Set(ctx, userconfig.SetUserConfigInput{
		AgentConfig: domain.AgentConfig{Model: "m"},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := m.Set(ctx, userconfig.SetUserConfigInput{}); err != nil {
		t.Fatalf("Set zero: %v", err)
	}

	got, err := m.Get(ctx)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("cleared = %+v, want zero", got)
	}
}

func TestUserService_SetRejectsInvalidPermissions(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()

	_, err := m.Set(ctx, userconfig.SetUserConfigInput{
		AgentConfig: domain.AgentConfig{Permissions: "yolo"},
	})
	var apiErr *apierr.Error
	if err == nil {
		t.Fatal("Set with bad permissions: want error, got nil")
	}
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *apierr.Error: %T", err)
	}
	if apiErr.Code != "INVALID_USER_CONFIG" {
		t.Fatalf("code = %q, want INVALID_USER_CONFIG", apiErr.Code)
	}
	if apiErr.Kind != apierr.KindInvalid {
		t.Fatalf("kind = %v, want KindInvalid", apiErr.Kind)
	}
}
