package amp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusAuthorizedFromEnv(t *testing.T) {
	t.Setenv("AMP_API_KEY", "amp-key")

	got, err := (&Plugin{resolvedBinary: "amp"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAmpSettingsAuthStatusAuthorizedWithAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"amp.apiKey":"amp-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := ampSettingsAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestAmpSecretsAuthStatusAuthorizedWithDocumentedSecretsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(`{"accessToken":"amp-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := ampSecretsAuthStatus(path)
	if err != nil || !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v, %v), want (authorized, true, nil)", status, ok, err)
	}
}

func TestAmpSettingsAuthStatusUnknownWithEmptyAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"amp.apiKey":""}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := ampSettingsAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestAmpUsageAuthStatusAuthorizedWhenSignedIn(t *testing.T) {
	t.Setenv("AMP_API_KEY", "")
	t.Setenv("AMP_SETTINGS_FILE", filepath.Join(t.TempDir(), "missing-settings.json"))
	restore := mockAmpAuthProbeRunner(t, func(_ context.Context, name string, arg ...string) ([]byte, error) {
		if name != "amp" || !reflect.DeepEqual(arg, []string{"usage", "--no-color"}) {
			t.Fatalf("command = %s %#v, want amp usage --no-color", name, arg)
		}
		return []byte("Signed in as agentsubs@pkarnal.com\nIndividual credits: -$0.07 remaining"), nil
	})
	defer restore()

	got, err := (&Plugin{resolvedBinary: "amp"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAmpUsageAuthStatusUnknownWhenNotConfirmed(t *testing.T) {
	restore := mockAmpAuthProbeRunner(t, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("login required"), errors.New("exit status 1")
	})
	defer restore()

	got, err := ampUsageAuthStatus(context.Background(), "amp")
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func mockAmpAuthProbeRunner(t *testing.T, runner func(context.Context, string, ...string) ([]byte, error)) func() {
	t.Helper()
	previous := authprobe.CmdRunner
	authprobe.CmdRunner = runner
	return func() { authprobe.CmdRunner = previous }
}
