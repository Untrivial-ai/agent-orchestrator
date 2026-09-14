package policyauthority

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const generation = "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"

func readRaw(t *testing.T, raw string) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry_policy.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := New(path).ReadAgentSwitchFailureAuthority(context.Background())
	return snapshot, err
}

func TestReaderTreatsVersionOneAsConsentGivenWhileGated(t *testing.T) {
	got, err := readRaw(t, `{"schema_version":1,"events_enabled":true,"consent_generation":"`+generation+`","updated_at":"2026-08-28T10:15:30.000Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || !got.EventsEnabled || got.ConsentProductionEnabled || got.ConsentGeneration != generation {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestReaderReadsVersionTwoConsentProductionState(t *testing.T) {
	for _, want := range []bool{true, false} {
		value := "false"
		if want {
			value = "true"
		}
		got, err := readRaw(t, `{"schema_version":2,"events_enabled":true,"consent_generation":"`+generation+`","consent_production_enabled":`+value+`,"updated_at":"2026-08-28T10:15:30.000Z"}`)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Present || !got.EventsEnabled || got.ConsentProductionEnabled != want {
			t.Fatalf("consent_production_enabled=%s: snapshot = %+v", value, got)
		}
	}
}

func TestReaderRejectsRecordsWhoseShapeDoesNotMatchTheirVersion(t *testing.T) {
	for name, raw := range map[string]string{
		"version 2 without gate state": `{"schema_version":2,"events_enabled":true,"consent_generation":"` + generation + `","updated_at":"2026-08-28T10:15:30.000Z"}`,
		"version 1 with gate state":    `{"schema_version":1,"events_enabled":true,"consent_generation":"` + generation + `","consent_production_enabled":true,"updated_at":"2026-08-28T10:15:30.000Z"}`,
		"version 2 null gate state":    `{"schema_version":2,"events_enabled":true,"consent_generation":"` + generation + `","consent_production_enabled":null,"updated_at":"2026-08-28T10:15:30.000Z"}`,
		"version 2 string gate state":  `{"schema_version":2,"events_enabled":true,"consent_generation":"` + generation + `","consent_production_enabled":"yes","updated_at":"2026-08-28T10:15:30.000Z"}`,
		"unknown version":              `{"schema_version":3,"events_enabled":true,"consent_generation":"` + generation + `","consent_production_enabled":true,"updated_at":"2026-08-28T10:15:30.000Z"}`,
		"unknown extra key":            `{"schema_version":2,"events_enabled":true,"consent_generation":"` + generation + `","consent_production_enabled":true,"updated_at":"2026-08-28T10:15:30.000Z","extra":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := readRaw(t, raw); err == nil {
				t.Fatalf("accepted %s: %+v", name, got)
			}
		})
	}
}
