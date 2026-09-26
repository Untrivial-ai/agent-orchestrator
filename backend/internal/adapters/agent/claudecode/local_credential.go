package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

// localOAuthTokenPath is where AO persists a captured Claude setup-token so the
// SAME credential authenticates both local sessions and the cloud copy pushed to
// the control plane ("one login for local + cloud"). It lives under the AO data
// dir, never the user's ~/.claude. Written by the daemon's set-credential
// endpoint; read here at launch.
func localOAuthTokenPath(dataDir string) string {
	return filepath.Join(dataDir, "harnesses", "claude-code", "oauth-token")
}

func readLocalOAuthToken(dataDir string) string {
	data, err := os.ReadFile(localOAuthTokenPath(dataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// AugmentRuntimeEnv injects AO's captured setup-token as CLAUDE_CODE_OAUTH_TOKEN
// for local sessions -- but ONLY when the machine has no native Claude login, so
// it can NEVER shadow a user's existing keychain / ~/.claude/.credentials.json
// login (CLAUDE_CODE_OAUTH_TOKEN sits at the top of agentcreds' resolution
// ladder, so an unconditional inject would beat a healthy native login -- the
// #5878 stale-credential-wins failure mode).
//
// The whole method is inert unless the user opted into the unified flow: no
// captured token on disk -> immediate return, zero change for existing users.
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, dataDir string) {
	if strings.TrimSpace(dataDir) == "" {
		return
	}
	token := readLocalOAuthToken(dataDir)
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A cheap presence resolve (no network): env vars, ~/.claude/.credentials.json,
	// and the keychain. If any native first-party credential is present, defer to
	// it and do not inject.
	if _, found := agentcreds.ResolveLocal(ctx, agentcreds.ProviderFirstParty, agentcreds.ResolveOptions{
		AllowKeychain: true,
		CommandEnv:    env,
	}); found {
		return
	}
	env["CLAUDE_CODE_OAUTH_TOKEN"] = token
}
