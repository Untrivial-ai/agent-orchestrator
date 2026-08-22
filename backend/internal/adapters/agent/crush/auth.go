package crush

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := crushLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func crushLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range []string{
		"HYPER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "VERCEL_API_KEY",
		"GEMINI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY", "SYNTHETIC_API_KEY",
		"HF_TOKEN", "CEREBRAS_API_KEY", "OPENROUTER_API_KEY", "IONET_API_KEY",
		"ALIBABA_SINGAPORE_API_KEY", "ALIBABA_US_API_KEY", "GROQ_API_KEY",
		"AVIAN_API_KEY", "OPENCODE_API_KEY", "AZURE_OPENAI_API_KEY", "MOONSHOT_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) != "" &&
		strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	if strings.TrimSpace(os.Getenv("AWS_PROFILE")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	if strings.TrimSpace(os.Getenv("VERTEXAI_PROJECT")) != "" &&
		strings.TrimSpace(os.Getenv("VERTEXAI_LOCATION")) != "" &&
		crushGoogleCredentialConfigured() {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	// providers.json is Crush's downloaded model/provider catalog, not a
	// credential store. It can contain provider metadata and must not be used
	// as evidence that a user is authenticated.
	return ports.AgentAuthStatusUnknown, false, nil
}

func crushGoogleCredentialConfigured() bool {
	if path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); path != "" {
		return crushNonEmptyRegularFile(path)
	}
	if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		return crushNonEmptyRegularFile(filepath.Join(configHome, "gcloud", "application_default_credentials.json"))
	}
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		return crushNonEmptyRegularFile(filepath.Join(appData, "gcloud", "application_default_credentials.json"))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return crushNonEmptyRegularFile(filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"))
}

func crushNonEmptyRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}
