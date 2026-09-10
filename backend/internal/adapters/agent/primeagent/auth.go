package primeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus checks Prime Agent's documented credential sources without
// starting a session or making a provider request.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	status, ok, err := primeLocalAuthStatus(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var primeAPIKeyEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_OAUTH_TOKEN",
	"AZURE_OPENAI_API_KEY",
	"OPENAI_API_KEY",
	"PRIME_API_KEY",
	"DEEPSEEK_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_CLOUD_API_KEY",
	"MISTRAL_API_KEY",
	"GROQ_API_KEY",
	"CEREBRAS_API_KEY",
	"CLOUDFLARE_API_KEY",
	"XAI_API_KEY",
	"OPENROUTER_API_KEY",
	"AI_GATEWAY_API_KEY",
	"ZAI_API_KEY",
	"OPENCODE_API_KEY",
	"HF_TOKEN",
	"FIREWORKS_API_KEY",
	"KIMI_API_KEY",
	"MINIMAX_API_KEY",
	"MINIMAX_CN_API_KEY",
	"XIAOMI_API_KEY",
	"XIAOMI_TOKEN_PLAN_CN_API_KEY",
	"XIAOMI_TOKEN_PLAN_AMS_API_KEY",
	"XIAOMI_TOKEN_PLAN_SGP_API_KEY",
	"AWS_BEARER_TOKEN_BEDROCK",
}

func primeLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range primeAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	if primeAWSCredentialEnvConfigured() || primeGoogleCredentialConfigured() {
		return ports.AgentAuthStatusAuthorized, true, nil
	}

	dir, ok := primeConfigDir()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	status, found, err := primeCredentialJSONStatus(filepath.Join(dir, "auth.json"), true)
	if err != nil || found {
		return status, found, err
	}
	status, found, err = primeCredentialJSONStatus(filepath.Join(dir, "models.json"), false)
	if err != nil || found {
		return status, found, err
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func primeConfigDir() (string, bool) {
	for _, name := range []string{primeAgentCodingAgentDirEnv, "PI_CODING_AGENT_DIR"} {
		if dir := strings.TrimSpace(os.Getenv(name)); dir != "" {
			return dir, true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".prime", "agent"), true
}

func primeAWSCredentialEnvConfigured() bool {
	if strings.TrimSpace(os.Getenv("AWS_PROFILE")) != "" ||
		strings.TrimSpace(os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")) != "" ||
		strings.TrimSpace(os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) != "" &&
		strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) != "" &&
		strings.TrimSpace(os.Getenv("AWS_ROLE_ARN")) != ""
}

func primeGoogleCredentialConfigured() bool {
	project := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	if project == "" {
		project = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT_ID"))
	}
	if project == "" {
		project = strings.TrimSpace(os.Getenv("GCLOUD_PROJECT"))
	}
	if project == "" || strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION")) == "" {
		return false
	}
	if path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); path != "" {
		return primeNonEmptyRegularFile(path)
	}
	if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		return primeNonEmptyRegularFile(filepath.Join(configHome, "gcloud", "application_default_credentials.json"))
	}
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		return primeNonEmptyRegularFile(filepath.Join(appData, "gcloud", "application_default_credentials.json"))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return primeNonEmptyRegularFile(filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"))
}

func primeNonEmptyRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func primeCredentialJSONStatus(path string, allowPlainKey bool) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if primeJSONHasCredential(value, allowPlainKey) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func primeJSONHasCredential(value any, allowPlainKey bool) bool {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			if primeJSONHasCredential(child, allowPlainKey) {
				return true
			}
		}
	case map[string]any:
		for key, child := range value {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "mcp:") {
				continue
			}
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
				switch normalized {
				case "apikey", "access", "refresh", "accesstoken", "refreshtoken", "oauthtoken":
					return true
				case "key":
					if allowPlainKey {
						return true
					}
				}
			}
			if primeJSONHasCredential(child, allowPlainKey) {
				return true
			}
		}
	}
	return false
}
