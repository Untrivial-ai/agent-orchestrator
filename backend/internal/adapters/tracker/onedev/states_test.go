package onedev

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDefaultStateMapNormalize(t *testing.T) {
	m := DefaultStateMap()
	cases := []struct {
		native string
		want   domain.NormalizedIssueState
		mapped bool
	}{
		{"Open", domain.IssueOpen, true},
		{"open", domain.IssueOpen, true},
		{"  OPEN  ", domain.IssueOpen, true},
		{"Closed", domain.IssueDone, true},
		{"in progress", domain.IssueInProgress, true},
		{"In Review", domain.IssueInReview, true},
		{"Review", domain.IssueInReview, true},
	}
	for _, tc := range cases {
		got, ok := m.Normalize(tc.native)
		if got != tc.want || ok != tc.mapped {
			t.Errorf("Normalize(%q) = %q,%v; want %q,%v", tc.native, got, ok, tc.want, tc.mapped)
		}
	}
}

// TestNormalizeUnmappedStateDefaultsToOpen pins the direction of the unmapped
// default. Flipping it to done would make AO treat live work as finished, and
// nothing would report the issues that silently disappeared.
func TestNormalizeUnmappedStateDefaultsToOpen(t *testing.T) {
	m := DefaultStateMap()
	for _, native := range []string{"Blocked", "Needs Design", "Won't Fix", ""} {
		got, ok := m.Normalize(native)
		if ok {
			t.Fatalf("Normalize(%q) reported mapped; want unmapped", native)
		}
		if got != domain.IssueOpen {
			t.Errorf("Normalize(%q) = %q; want %q", native, got, domain.IssueOpen)
		}
	}
	if UnmappedState != domain.IssueOpen {
		t.Errorf("UnmappedState = %q; want %q", UnmappedState, domain.IssueOpen)
	}
}

func TestWithOverridesAddsInstanceStates(t *testing.T) {
	m, err := NewStateMap(map[string]string{"Blocked": "open", "Verifying": "review", "Won't Fix": "cancelled"})
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	for native, want := range map[string]domain.NormalizedIssueState{
		"Blocked":   domain.IssueOpen,
		"verifying": domain.IssueInReview,
		"WON'T FIX": domain.IssueCancelled,
		"Open":      domain.IssueOpen,
		"Closed":    domain.IssueDone,
	} {
		got, ok := m.Normalize(native)
		if !ok {
			t.Fatalf("Normalize(%q) unmapped; want %q", native, want)
		}
		if got != want {
			t.Errorf("Normalize(%q) = %q; want %q", native, got, want)
		}
	}
}

// TestWithOverridesReplacesDefaultCaseInsensitively guards against an override
// landing beside the default it meant to replace, which would leave the
// winning value up to map iteration order.
func TestWithOverridesReplacesDefaultCaseInsensitively(t *testing.T) {
	m, err := NewStateMap(map[string]string{"closed": "cancelled"})
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	if len(m) != len(DefaultStateMap()) {
		t.Fatalf("map has %d entries; want %d (override should replace, not add)", len(m), len(DefaultStateMap()))
	}
	got, ok := m.Normalize("Closed")
	if !ok || got != domain.IssueCancelled {
		t.Errorf("Normalize(\"Closed\") = %q,%v; want %q,true", got, ok, domain.IssueCancelled)
	}
}

func TestWithOverridesRejectsUnknownNormalizedState(t *testing.T) {
	_, err := NewStateMap(map[string]string{"Blocked": "stuck"})
	if err == nil {
		t.Fatal("NewStateMap accepted an unknown normalized state; want an error")
	}
	if !strings.Contains(err.Error(), "Blocked") {
		t.Errorf("error %q does not name the offending state", err)
	}
}

func TestNewStateMapWithNoOverridesEqualsDefault(t *testing.T) {
	m, err := NewStateMap(nil)
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	if !reflect.DeepEqual(m, DefaultStateMap()) {
		t.Errorf("NewStateMap(nil) = %v; want DefaultStateMap()", m)
	}
}

func TestTerminalStatesIsSortedAndCoversCancelled(t *testing.T) {
	m, err := NewStateMap(map[string]string{"Won't Fix": "cancelled", "Blocked": "open"})
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	got := m.TerminalStates()
	want := []string{"Closed", "Won't Fix"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TerminalStates() = %v; want %v", got, want)
	}
}
