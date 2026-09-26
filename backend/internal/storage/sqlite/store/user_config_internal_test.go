package store

import (
	"testing"
)

func TestUnmarshalAgentConfigDegradesGracefully(t *testing.T) {
	// SQL NULL / empty → zero config.
	if got := unmarshalAgentConfig(""); !got.IsZero() {
		t.Fatalf("NULL config = %#v, want zero", got)
	}

	// Valid JSON decodes.
	if got := unmarshalAgentConfig(`{"model":"m"}`); got.Model != "m" {
		t.Fatalf("valid config Model = %q, want m", got.Model)
	}

	// Corrupt JSON must NOT error — it degrades to a zero config so the user-config
	// row stays accessible (mirrors the project-config resilience policy).
	if got := unmarshalAgentConfig(`{not json`); !got.IsZero() {
		t.Fatalf("corrupt config = %#v, want zero (degraded)", got)
	}
}
