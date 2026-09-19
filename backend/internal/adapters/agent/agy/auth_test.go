package agy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAgyAuthStatusRequiresSupportedLocalEvidence(t *testing.T) {
	for _, tt := range []struct {
		name     string
		settings string
		env      map[string]string
		files    map[string]string
		want     ports.AgentAuthStatus
	}{
		{
			name:     "Gemini provider and Gemini key",
			settings: `{"modelProvider":"gemini"}`,
			env:      map[string]string{"GEMINI_API_KEY": "test-gemini-key"},
			want:     ports.AgentAuthStatusConfigured,
		},
		{
			name:     "Gemini provider without key",
			settings: `{"modelProvider":"gemini"}`,
			want:     ports.AgentAuthStatusUnknown,
		},
		{
			name: "Gemini key without provider",
			env:  map[string]string{"GEMINI_API_KEY": "test-gemini-key"},
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name:     "Google key is not a Gemini CLI key",
			settings: `{"modelProvider":"gemini"}`,
			env:      map[string]string{"GOOGLE_API_KEY": "ignored-google-key"},
			want:     ports.AgentAuthStatusUnknown,
		},
		{
			name:     "ADC is not Agy evidence",
			settings: `{"modelProvider":"gemini"}`,
			env: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": "/tmp/ignored-adc.json",
				"GOOGLE_CLOUD_PROJECT":           "ignored-project",
			},
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "dotenv is not Agy evidence",
			files: map[string]string{
				".env":         "GEMINI_API_KEY=ignored\n",
				".gemini/.env": "GEMINI_API_KEY=ignored\n",
			},
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name:     "malformed settings",
			settings: `{"modelProvider":`,
			env:      map[string]string{"GEMINI_API_KEY": "test-gemini-key"},
			want:     ports.AgentAuthStatusUnknown,
		},
		{
			name:     "unrelated nested provider",
			settings: `{"other":{"modelProvider":"gemini"}}`,
			env:      map[string]string{"GEMINI_API_KEY": "test-gemini-key"},
			want:     ports.AgentAuthStatusUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.settings != "" {
				writeAgyAuthFixture(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), tt.settings)
			}
			for name, contents := range tt.files {
				writeAgyAuthFixture(t, filepath.Join(home, name), contents)
			}

			d := authutil.Dependencies{
				GOOS: "linux",
				Getenv: func(name string) string {
					if name == "HOME" {
						return home
					}
					return tt.env[name]
				},
				Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("non-keyring evidence test ran a command")
					return nil, nil
				},
			}
			got, err := agyAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAgyAuthStatusUsesFixedKeyringEntry(t *testing.T) {
	for _, tt := range []struct {
		name   string
		secret string
		runErr error
		want   ports.AgentAuthStatus
	}{
		{"present", `{"token":{"access_token":"test-access"}}`, nil, ports.AgentAuthStatusConfigured},
		{"empty", "  \n", nil, ports.AgentAuthStatusUnknown},
		{"unavailable", "", errors.New("keychain unavailable with secret detail"), ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			d := authutil.Dependencies{
				GOOS: "darwin",
				Getenv: func(name string) string {
					if name == "HOME" {
						return home
					}
					return ""
				},
				Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if name != "/usr/bin/security" || !reflect.DeepEqual(args, []string{"find-generic-password", "-s", "gemini", "-a", "antigravity", "-w"}) {
						t.Fatalf("unexpected keyring command: %s %#v", name, args)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("keyring lookup has no deadline")
					}
					return []byte(tt.secret), tt.runErr
				},
			}
			got, err := agyAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAgyMalformedSettingsFallsThroughToKeyring(t *testing.T) {
	home := t.TempDir()
	writeAgyAuthFixture(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"modelProvider":`)
	d := authutil.Dependencies{
		GOOS: "darwin",
		Getenv: func(name string) string {
			if name == "HOME" {
				return home
			}
			return ""
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("browser-login-secret"), nil
		},
	}
	got, err := agyAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, err = %v", got, err)
	}
}

func TestAgyGeminiModeSelectsOnlyGeminiAPIKey(t *testing.T) {
	home := t.TempDir()
	writeAgyAuthFixture(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"modelProvider":"gemini"}`)
	keyringCalls := 0
	d := authutil.Dependencies{
		GOOS: "darwin",
		Getenv: func(name string) string {
			if name == "HOME" {
				return home
			}
			return ""
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			keyringCalls++
			return []byte("browser-login-secret"), nil
		},
	}
	got, err := agyAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, err = %v; want unknown", got, err)
	}
	if keyringCalls != 0 {
		t.Fatalf("keyring calls = %d; Gemini API-key mode must not use browser-login credentials", keyringCalls)
	}
}

func TestAgyAuthStatusForUsesScopedEnvironment(t *testing.T) {
	home := t.TempDir()
	writeAgyAuthFixture(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"modelProvider":"gemini"}`)
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "inherited-key")
	checker, ok := any(&Plugin{resolvedBinary: "agy"}).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Agy must implement AgentScopedAuthChecker")
	}
	got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{Env: map[string]string{"GEMINI_API_KEY": ""}})
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, err = %v; want unknown", got, err)
	}
}

func TestAgyDoesNotResolveGoogleADC(t *testing.T) {
	home := t.TempDir()
	d := authutil.Dependencies{
		GOOS: "linux",
		Getenv: func(name string) string {
			if name == "HOME" {
				return home
			}
			if name == "GOOGLE_APPLICATION_CREDENTIALS" {
				return "/tmp/ignored-adc.json"
			}
			return ""
		},
		LoadGoogleADC: func(context.Context) (authutil.CloudCredential, error) {
			t.Fatal("Agy must not resolve Google ADC")
			return authutil.CloudCredential{}, nil
		},
	}
	got, err := agyAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, err = %v", got, err)
	}
}

func writeAgyAuthFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
