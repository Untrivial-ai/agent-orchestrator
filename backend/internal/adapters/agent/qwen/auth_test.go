package qwen

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestQwenLocalAuthStatusAuthorizedWithProviderEnv(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "zai-key")

	status, ok, err := qwenLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenLocalAuthStatusAuthorizedWithDocumentedProtocolEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	status, ok, err := qwenLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenAuthStatusFromSettingsAuthorizedWithModelProviderAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{
		"modelProviders": {
			"zai": {
				"baseUrl": "https://api.z.ai/api/coding/paas/v4",
				"apiKey": "zai-key"
			}
		},
		"defaultModel": "glm-4.5"
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenAuthStatusFromSettingsAuthorizedWithSecurityAuthAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{
		"security": {
			"auth": {
				"apiKey": "openai-compatible-key"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenAuthStatusFromSettingsAuthorizedWithConfiguredEnvKey(t *testing.T) {
	t.Setenv("CUSTOM_QWEN_KEY", "configured-key")
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"modelProviders":{"custom":{"envKey":"CUSTOM_QWEN_KEY"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenAuthStatusFromSettingsAuthorizedWithDocumentedEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"GEMINI_API_KEY":"gemini-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenEnvFileAuthStatusAuthorizedWithDocumentedProviderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("OPENROUTER_API_KEY=router-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenEnvFileAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenGlobalEnvAuthStatusUsesQwenHome(t *testing.T) {
	home := t.TempDir()
	qwenHome := filepath.Join(home, "custom-qwen")
	if err := os.MkdirAll(qwenHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qwenHome, ".env"), []byte("GOOGLE_API_KEY=google-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenGlobalEnvAuthStatus(qwenHome, home)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenGlobalEnvAuthStatusIgnoresDaemonWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	qwenHome := filepath.Join(root, "qwen")
	if err := os.MkdirAll(qwenHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("QWEN_API_KEY=project-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	status, ok, err := qwenGlobalEnvAuthStatus(qwenHome, filepath.Join(root, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestQwenEnvFileAuthStatusAuthorizedWithConfiguredProviderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("CUSTOM_QWEN_KEY=custom-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenEnvFileAuthStatus(path, "CUSTOM_QWEN_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestQwenAuthStatusFromSettingsUnknownWhenMissing(t *testing.T) {
	status, ok, err := qwenAuthStatusFromSettings(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestAuthStatusWithoutLocalEvidenceStaysUnknown(t *testing.T) {
	for _, name := range qwenAPIKeyEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("QWEN_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	status, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}
