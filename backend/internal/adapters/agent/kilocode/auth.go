package kilocode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"

	_ "modernc.org/sqlite" // register sqlite driver for KiloCode auth database probes
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status. Missing
// credentials remain unknown because Kilo can still run eligible public free
// models without a provider login.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := kilocodeLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := aoprocess.CommandContext(probeCtx, binary, "auth", "list").CombinedOutput()
	if probeCtx.Err() != nil {
		if probeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	status, ok := kilocodeAuthListStatus(string(out))
	if ok {
		return status, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var kilocodeAPIKeyEnvVars = []string{
	"KILO_API_KEY",
	"KILOCODE_API_KEY",
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"OPENROUTER_API_KEY",
	"DEEPSEEK_API_KEY",
	"GROQ_API_KEY",
	"XAI_API_KEY",
	"MISTRAL_API_KEY",
	"COHERE_API_KEY",
}

func kilocodeLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range kilocodeAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	dataDir, ok := kilocodeDataDir()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	authorized, known, err := kilocodeAuthJSONStatus(filepath.Join(dataDir, "auth.json"))
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if known && authorized {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	authorized, known, err = kilocodeDBAuthStatus(ctx, filepath.Join(dataDir, "kilo.db"))
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if known && authorized {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func kilocodeDataDir() (string, bool) {
	if dataDir := strings.TrimSpace(os.Getenv("KILO_DATA_DIR")); dataDir != "" {
		return dataDir, true
	}
	if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
		return filepath.Join(dataHome, "kilo"), true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".local", "share", "kilo"), true
}

func kilocodeAuthJSONStatus(path string) (authorized, known bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return false, false, nil
	}
	var providers map[string]map[string]any
	if err := json.Unmarshal(data, &providers); err != nil {
		return false, false, err
	}
	for _, provider := range providers {
		if len(provider) == 0 {
			continue
		}
		known = true
		for _, key := range []string{"key", "apiKey", "api_key", "access_token", "token"} {
			if strings.TrimSpace(asString(provider[key])) != "" {
				return true, true, nil
			}
		}
	}
	return false, known, nil
}

func asString(value any) string {
	s, _ := value.(string)
	return s
}

func kilocodeDBAuthStatus(ctx context.Context, path string) (authorized, known bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, false, nil
	} else if err != nil {
		return false, false, err
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return false, false, err
	}
	defer func() {
		_ = db.Close()
	}()
	return kilocodeDBHasAuthorizedAccount(ctx, db)
}

func kilocodeDBHasAuthorizedAccount(ctx context.Context, db *sql.DB) (authorized, known bool, err error) {
	for _, query := range []string{
		`SELECT COUNT(*) FROM account_state WHERE active_account_id IS NOT NULL AND trim(active_account_id) != ''`,
		`SELECT COUNT(*) FROM account WHERE trim(access_token) != ''`,
		`SELECT COUNT(*) FROM control_account WHERE active = 1 AND trim(access_token) != ''`,
	} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") {
				continue
			}
			return false, false, err
		}
		known = true
		if count > 0 {
			return true, true, nil
		}
	}
	return false, known, nil
}

var kilocodeAuthListCountRE = regexp.MustCompile(`(?m)\b([1-9][0-9]*)\s+(credentials?|environment variables?)\b`)

func kilocodeAuthListStatus(output string) (ports.AgentAuthStatus, bool) {
	text := strings.ToLower(output)
	if kilocodeAuthListCountRE.MatchString(text) {
		return ports.AgentAuthStatusAuthorized, true
	}
	if strings.Contains(text, "0 credentials") && strings.Contains(text, "0 environment variable") {
		return ports.AgentAuthStatusUnknown, true
	}
	return ports.AgentAuthStatusUnknown, false
}
