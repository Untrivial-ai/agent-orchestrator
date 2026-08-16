package store

import (
	"database/sql"
	"testing"
)

func TestUnmarshalAgentConfigDegradesGracefully(t *testing.T) {
	// SQL NULL / empty → zero config.
	if got := unmarshalAgentConfig(sql.NullString{}); !got.IsZero() {
		t.Fatalf("NULL config = %#v, want zero", got)
	}

	// Valid JSON decodes.
	if got := unmarshalAgentConfig(sql.NullString{String: `{"model":"m"}`, Valid: true}); got.Model != "m" {
		t.Fatalf("valid config Model = %q, want m", got.Model)
	}

	// Corrupt JSON must NOT error — it degrades to a zero config so the user-config
	// row stays accessible (mirrors the project-config resilience policy).
	if got := unmarshalAgentConfig(sql.NullString{String: `{not json`, Valid: true}); !got.IsZero() {
		t.Fatalf("corrupt config = %#v, want zero (degraded)", got)
	}
}
