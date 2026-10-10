package zcode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeCredentialConfigurationIsNeverAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       ports.AgentAuthStatus
	}{
		{"encrypted zai", `{"oauth:zai:access_token":"enc:v1:opaque"}`, ports.AgentAuthStatusConfigured},
		{"encrypted bigmodel", `{"oauth:bigmodel:access_token":"enc:v1:opaque"}`, ports.AgentAuthStatusConfigured},
		{"legacy opaque", `{"oauth:zai:access_token":"not-a-verified-token"}`, ports.AgentAuthStatusConfigured},
		{"empty token", `{"oauth:zai:access_token":" "}`, ports.AgentAuthStatusUnknown},
		{"other credentials", `{"unrelated":"value"}`, ports.AgentAuthStatusUnknown},
		{"malformed", `invalid`, ports.AgentAuthStatusUnknown},
		{"missing", "", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("ZCODE_DATA_BASE_DIR", base)
			dir := filepath.Join(base, ".zcode", "v2")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.body != "" {
				if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := &Plugin{resolvedBinary: "zcode"}
			got, err := p.AuthStatus(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("status = %s,%v; want %s", got, err, tc.want)
			}
		})
	}
}
