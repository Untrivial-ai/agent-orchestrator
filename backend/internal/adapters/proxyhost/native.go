package proxyhost

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

// ImportNative imports this computer's login or API key unless seen names it; never twice.
func (c *Client) ImportNative(ctx context.Context, provider string, apiKey bool, known func(identity string) bool) (ports.VerifiedProviderLogin, string, error) {
	request := ports.ProviderLoginRequest{Provider: provider, Mode: "import"}
	id, secret, identity := "native-"+provider+"-", "", ""
	var err error
	if apiKey {
		id, request.Mode, request.Label = "native-key-"+provider+"-", "api_key", "Global API key"
		if request.APIKey, request.BaseURL, err = nativeAPIKey(ctx, provider, c.Endpoint()); request.APIKey != "" {
			secret = provider + "\x00" + request.APIKey + "\x00" + request.BaseURL
		}
	} else {
		request.CredentialJSON, identity, err = nativeLogin(ctx, provider)
		secret = request.CredentialJSON
	}
	// A read that was cut short must not pass for a computer with no login.
	if err = cmp.Or(err, ctx.Err()); err != nil || secret == "" {
		return ports.VerifiedProviderLogin{}, "", err
	}
	sum := sha256.Sum256([]byte(secret))
	fingerprint := hex.EncodeToString(sum[:])
	if apiKey {
		identity = fingerprint
	}
	if known(identity) {
		return ports.VerifiedProviderLogin{}, identity, nil
	}
	id += fingerprint
	verified, err := c.LoginResult(ctx, id)
	if notFound(err) {
		_, err = c.StartLogin(ctx, id, request)
		// The helper may acknowledge a sign-in before it has loaded it.
		for ; err == nil; time.Sleep(100 * time.Millisecond) {
			if verified, err = c.LoginResult(ctx, id); !notFound(err) {
				break
			}
			err = ctx.Err()
		}
	}
	return verified, cmp.Or(identity, signInIdentity(verified.Email)), err
}

// signInIdentity names whose sign-in a login is, without any of its tokens.
func signInIdentity(email string) string {
	if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
		return "id:" + email
	}
	return ""
}

// readCodexAuth reads Codex's own auth file; none is a computer not signed in.
func readCodexAuth() (auth any, err error) {
	user, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(cmp.Or(os.Getenv("CODEX_HOME"), filepath.Join(user, ".codex")), "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &auth)
	}
	return auth, err
}

// nativeLogin is this computer's sign-in as a helper file; its bytes feed the fingerprint.
func nativeLogin(ctx context.Context, provider string) (login, identity string, err error) {
	value := map[string]any{"type": provider}
	if provider == "claude" {
		value["access_token"], value["refresh_token"] = agentcreds.LocalOAuth(ctx, agentcreds.ResolveOptions{AllowKeychain: true})
		// Claude Code records whose login it holds beside its settings, not in the login.
		home, _ := os.UserHomeDir()
		data, _ := os.ReadFile(filepath.Join(cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), home), ".claude.json"))
		var settings any
		_ = json.Unmarshal(data, &settings)
		identity = signInIdentity(text(settings, "oauthAccount", "emailAddress"))
	} else {
		auth, err := readCodexAuth()
		if err != nil {
			return "", "", err
		}
		tokens := at(auth, "tokens")
		access, idToken := text(tokens, "access_token"), text(tokens, "id_token")
		claims := jwtClaims(idToken)
		value["access_token"], value["refresh_token"], value["id_token"] = access, text(tokens, "refresh_token"), idToken
		scope := at(claims, "https://api.openai.com/auth")
		value["email"], value["account_id"], value["plan_type"] = claims["email"], at(scope, "chatgpt_account_id"), at(scope, "chatgpt_plan_type")
		identity = signInIdentity(text(claims, "email"))
		if account := text(tokens, "account_id"); account != "" {
			value["account_id"] = account
		}
		if expiry, ok := jwtClaims(access)["exp"].(float64); ok {
			value["expired"] = time.Unix(int64(expiry), 0).UTC().Format(time.RFC3339)
		}
	}
	if value["access_token"] == "" {
		return "", "", nil
	}
	encoded, err := json.Marshal(value)
	return string(encoded), identity, err
}

func jwtClaims(token string) (claims map[string]any) {
	if parts := strings.Split(token, "."); len(parts) == 3 {
		data, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(data, &claims)
	}
	return claims
}

// nativeAPIKey finds the key the agent really uses and its address, never AO's own helper.
func nativeAPIKey(ctx context.Context, provider, helper string) (key, base string, err error) {
	if provider == "claude" {
		// Stored sign-ins are imported as sign-ins; only a key is wanted here.
		opts := (agentcreds.ResolveOptions{DisableStoredCredentials: true}).WithClaudeSettings(ctx)
		route, _ := agentcreds.ResolveProvider("", opts)
		found, _ := agentcreds.ResolveLocal(ctx, route, opts)
		if found.Kind == agentcreds.KindAPIKey || found.Kind == agentcreds.KindAuthToken {
			key, base = found.Secret, cmp.Or(found.BaseURL, claudeAPI)
		}
	} else {
		auth, err := readCodexAuth()
		if err != nil {
			return "", "", err
		}
		// Older files do not say which sign-in is in use; there ChatGPT wins.
		mode := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(text(auth, "auth_mode")))
		if mode == "apikey" || mode == "" && text(auth, "tokens", "access_token") == "" {
			key, base = text(auth, "OPENAI_API_KEY"), cmp.Or(os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
		}
	}
	key, base = strings.TrimSpace(key), strings.TrimRight(strings.TrimSpace(base), "/")
	if host := baseHost(base); key == "" || host == "" || "http://"+host == helper {
		return "", "", nil
	}
	return key, base, nil
}
