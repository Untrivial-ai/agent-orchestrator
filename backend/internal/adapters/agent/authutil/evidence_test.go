package authutil

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFirstDefinitive(t *testing.T) {
	configured := Evidence{Status: ports.AgentAuthStatus("configured"), Source: "environment"}
	unknown := Evidence{Status: ports.AgentAuthStatusUnknown}
	tests := []struct {
		name   string
		input  []Evidence
		want   ports.AgentAuthStatus
		source string
	}{
		{"empty", nil, "unknown", ""},
		{"configured beats unknown", []Evidence{unknown, configured}, "configured", "environment"},
		{"first equal source wins", []Evidence{configured, {Status: "configured", Source: "file"}}, "configured", "environment"},
		{"authoritative rejection beats local", []Evidence{configured, Authoritative(ports.AgentAuthStatusUnauthorized, "native")}, "unauthorized", "native"},
		{"successful validation beats rejection", []Evidence{Authoritative(ports.AgentAuthStatusUnauthorized, "native"), Authoritative(ports.AgentAuthStatusAuthorized, "provider"), configured}, "authorized", "provider"},
		{"raw rejection is not authoritative", []Evidence{{Status: "unauthorized", Source: "file"}, configured}, "configured", "environment"},
		{"raw success is not authoritative", []Evidence{{Status: "authorized", Source: "file"}}, "unknown", ""},
		{"raw no auth is not explicit", []Evidence{{Status: "not_applicable"}}, "unknown", ""},
		{"explicit no auth", []Evidence{NoAuthEvidence(true)}, "not_applicable", "selected-provider"},
		{"unselected no auth", []Evidence{NoAuthEvidence(false)}, "unknown", ""},
		{"trustworthy expired token", []Evidence{configured, ExpiryEvidence(time.Unix(100, 0), false, time.Unix(200, 0))}, "unauthorized", "credential-expiry"},
		{"refreshable expired token", []Evidence{configured, ExpiryEvidence(time.Unix(100, 0), true, time.Unix(200, 0))}, "configured", "environment"},
		{"unexpired token", []Evidence{ExpiryEvidence(time.Unix(300, 0), false, time.Unix(200, 0))}, "configured", "credential-expiry"},
		{"zero expiry is unknown", []Evidence{ExpiryEvidence(time.Time{}, false, time.Unix(200, 0))}, "unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FirstDefinitive(tt.input...)
			if got.Status != tt.want || got.Source != tt.source {
				t.Fatalf("got %#v, want %s from %q", got, tt.want, tt.source)
			}
		})
	}
}

func TestParseExpiry(t *testing.T) {
	for _, tt := range []struct {
		input any
		want  string
		valid bool
	}{
		{json.Number("1700000000"), "2023-11-14T22:13:20Z", true},
		{float64(1700000000), "2023-11-14T22:13:20Z", true},
		{int64(1700000000), "2023-11-14T22:13:20Z", true},
		{"2023-11-14T22:13:20Z", "2023-11-14T22:13:20Z", true},
		{"1700000000", "2023-11-14T22:13:20Z", true},
		{nil, "", false}, {"", "", false}, {"not-an-expiry", "", false}, {float64(-1), "", false},
	} {
		got, ok := ParseExpiry(tt.input)
		if ok != tt.valid || (ok && got.UTC().Format(time.RFC3339) != tt.want) {
			t.Errorf("ParseExpiry(%v) = %v, %v; want %s, %v", tt.input, got, ok, tt.want, tt.valid)
		}
	}
}
