package pi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPiAuthJSONStatusAuthorizedWithProviderKey(t *testing.T) {
	path := writePiAuthJSON(t, `{"zai":{"type":"api_key","key":"test-key"}}`)

	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestPiAuthJSONStatusAuthorizedWithResolvedEnvKey(t *testing.T) {
	t.Setenv("PI_TEST_API_KEY", "resolved-key")
	path := writePiAuthJSON(t, `{"zai":{"type":"api_key","key":"$PI_TEST_API_KEY"}}`)

	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestPiAuthJSONStatusUnknownWithUnresolvedKey(t *testing.T) {
	t.Setenv("PI_MISSING_API_KEY", "")
	tests := map[string]string{
		"missing environment variable":        `{"zai":{"type":"api_key","key":"$PI_MISSING_API_KEY"}}`,
		"missing braced environment variable": `{"zai":{"type":"api_key","key":"${PI_MISSING_API_KEY}"}}`,
		"unverified command":                  `{"zai":{"type":"api_key","key":"!false"}}`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := writePiAuthJSON(t, content)

			status, ok, err := piAuthJSONStatus(path)
			if err != nil {
				t.Fatal(err)
			}
			if ok || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
			}
		})
	}
}

func TestPiAuthJSONStatusUnknownWhenEmpty(t *testing.T) {
	path := writePiAuthJSON(t, `{}`)

	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func writePiAuthJSON(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
