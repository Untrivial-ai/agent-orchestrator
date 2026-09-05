package store_test

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestUserConfigStore_EmptyDBIsZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A fresh database has no user-config row; that state — shared by every user
	// until they set one — must resolve to a zero config and "not found," never
	// an error.
	cfg, found, err := s.GetUserConfig(ctx)
	if err != nil || found {
		t.Fatalf("GetUserConfig on empty DB = (%+v, %v, %v), want zero, false, nil", cfg, found, err)
	}
	if !cfg.IsZero() {
		t.Fatalf("GetUserConfig on empty DB = %+v, want zero AgentConfig", cfg)
	}
}

func TestUserConfigStore_UpsertRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	in := domain.AgentConfig{Model: "claude-opus-4-8", Permissions: domain.PermissionModeAcceptEdits}
	if err := s.UpsertUserConfig(ctx, in); err != nil {
		t.Fatalf("UpsertUserConfig: %v", err)
	}

	got, found, err := s.GetUserConfig(ctx)
	if err != nil || !found {
		t.Fatalf("GetUserConfig after upsert = (%+v, %v, %v), want found", got, found, err)
	}
	if got != in {
		t.Fatalf("round-trip got = %+v, want %+v", got, in)
	}

	// Wholesale replacement: a second upsert overwrites every field.
	replace := domain.AgentConfig{Model: "claude-haiku-4-5"}
	if err := s.UpsertUserConfig(ctx, replace); err != nil {
		t.Fatalf("UpsertUserConfig (replace): %v", err)
	}
	got, found, err = s.GetUserConfig(ctx)
	if err != nil || !found {
		t.Fatalf("GetUserConfig after replace = (%+v, %v, %v)", got, found, err)
	}
	if got.Model != "claude-haiku-4-5" || got.Permissions != "" {
		t.Fatalf("replace got = %+v, want model cleared permissions", got)
	}
}

func TestUserConfigStore_ZeroStoresNULL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Setting a real config then clearing it stores a zero (SQL NULL), which
	// reads back as a zero value — not a found row carrying an empty object.
	if err := s.UpsertUserConfig(ctx, domain.AgentConfig{Model: "m"}); err != nil {
		t.Fatalf("UpsertUserConfig: %v", err)
	}
	if err := s.UpsertUserConfig(ctx, domain.AgentConfig{}); err != nil {
		t.Fatalf("UpsertUserConfig (zero): %v", err)
	}
	got, found, err := s.GetUserConfig(ctx)
	if err != nil {
		t.Fatalf("GetUserConfig after clear: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("cleared config got = %+v, want zero", got)
	}
	// found is true: a row exists (id=1), it just holds NULL. Callers rely on the
	// zero value, not on found, to mean "inherit User defaults."
	_ = found
}
