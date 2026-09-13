package domain

import (
	"regexp"
	"testing"
)

func TestRuntimeHandleNamePassesThroughConforming(t *testing.T) {
	if got := RuntimeHandleName("myproj-1"); got != "myproj-1" {
		t.Fatalf("RuntimeHandleName = %q, want unchanged", got)
	}
}

func TestRuntimeHandleNameSanitizesDottedProjectIDs(t *testing.T) {
	got := RuntimeHandleName("axisrow.github.io-38")
	if got == "axisrow.github.io-38" {
		t.Fatalf("dot-containing id must be sanitized, got %q", got)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(got) {
		t.Fatalf("sanitized name %q is not runtime-safe", got)
	}
	// Deterministic and distinct from other ids of the same slug.
	if again := RuntimeHandleName("axisrow.github.io-38"); again != got {
		t.Fatalf("not deterministic: %q vs %q", got, again)
	}
	if other := RuntimeHandleName("axisrow.github.io-40"); other == got {
		t.Fatalf("distinct ids collapsed to the same name %q", got)
	}
}

func TestRuntimeHandleNameEmptyInput(t *testing.T) {
	got := RuntimeHandleName("///")
	if got == "" || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(got) {
		t.Fatalf("RuntimeHandleName(///) = %q, want a runtime-safe fallback", got)
	}
}
