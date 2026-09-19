package agentcreds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Credential resolution for the local machine.
//
// Cloud is handed a secret; the desktop daemon has to find one. That search is
// this file, and it is ordered: Claude Code consults its sources in a fixed
// precedence, and AO must report on the same one the agent will actually send.
// Reporting on a different source is worse than not checking, because it
// produces a confident verdict about a credential that is not in play.
//
// The precedence below was corrected by experiment, not read off the docs:
// CLAUDE_CODE_OAUTH_TOKEN beats ANTHROPIC_API_KEY. Each source is tagged with
// the Kind that selects its header, so no downstream code has to guess.

// Env is an environment lookup, injectable for tests.
type Env func(string) string

// ResolveOptions tunes local credential discovery.
type ResolveOptions struct {
	// Env reads environment variables. Defaults to the process environment.
	Env Env
	// ConfigDir overrides Claude Code's config directory.
	ConfigDir string
	// AllowKeychain permits reading the macOS keychain.
	//
	// This is the switch behind open question Q1. The Cloud provisioning
	// design forbids extracting local keychain state, but that rule is about
	// shipping a developer's credentials into a Cloud sandbox; whether it also
	// bars the local daemon from reading the local keychain for local
	// validation is not settled by its text. The assumption here is that it
	// does not — and every keychain call sits behind one function and this one
	// flag, so reversing the assumption is a one-line change that costs only
	// source 5.
	AllowKeychain bool
	// Runner executes the keychain helper. Defaults to the real one.
	Runner commandRunner
	// GOOS overrides platform detection, for tests.
	GOOS string
	// WorkingDir and CommandEnv reproduce the project launch context for AWS and
	// gcloud credential-chain commands.
	WorkingDir string
	CommandEnv map[string]string
}

// WithClaudeSettings applies the shared settings resolver to the launch context.
// Explicit command environment values are also used for credential resolution;
// unrelated launch variables remain available to commands without loading them
// from settings files. Neither the caller's options nor its map is mutated.
func (o ResolveOptions) WithClaudeSettings() ResolveOptions {
	settings := ResolveClaudeSettings(o.WorkingDir, o.CommandEnv, o)
	base := o
	merged := make(map[string]string, len(o.CommandEnv)+len(settings.Env))
	for key, value := range o.CommandEnv {
		merged[key] = value
	}
	for key, value := range settings.Env {
		merged[key] = value
	}
	o.CommandEnv = merged
	o.Env = func(key string) string {
		if value, ok := merged[key]; ok {
			return value
		}
		return base.env(key)
	}
	return o
}

func (o ResolveOptions) commandInvocation() commandInvocation {
	return commandInvocation{WorkingDir: o.WorkingDir, Env: o.CommandEnv}
}

func (o ResolveOptions) env(name string) string {
	if o.Env != nil {
		return strings.TrimSpace(o.Env(name))
	}
	return strings.TrimSpace(os.Getenv(name))
}

func (o ResolveOptions) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

// ResolveProvider decides which API surface the local configuration points at.
//
// It is a gate, not a guess: an unrecognized apiProvider yields ok=false so
// the caller stays silent rather than probing api.anthropic.com with a
// credential that belongs to Bedrock. Explicit project environment selects the
// provider first because it is also what launch will use; only then may the
// CLI's report refine an otherwise implicit provider choice.
func ResolveProvider(reported string, opts ResolveOptions) (Provider, bool) {
	selected := make([]Provider, 0, 3)
	for _, candidate := range []struct {
		name     string
		provider Provider
	}{
		{"CLAUDE_CODE_USE_BEDROCK", ProviderBedrock},
		{"CLAUDE_CODE_USE_VERTEX", ProviderVertex},
		{"CLAUDE_CODE_USE_FOUNDRY", ProviderFoundry},
	} {
		if envTruthy(opts.env(candidate.name)) {
			selected = append(selected, candidate.provider)
		}
	}
	if len(selected) > 1 {
		return "", false
	}
	if len(selected) == 1 {
		return selected[0], true
	}
	if opts.env("ANTHROPIC_BASE_URL") != "" {
		return ProviderGateway, true
	}
	if trimmed := strings.TrimSpace(reported); trimmed != "" {
		return ParseProvider(trimmed)
	}
	return ProviderFirstParty, true
}

func envTruthy(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}

// ResolveLocal finds the credential Claude Code will use for the given
// provider. ok=false means nothing was found, which is Unknown — never a
// statement that the user is signed out.
func ResolveLocal(ctx context.Context, provider Provider, opts ResolveOptions) (Credential, bool) {
	switch provider {
	case ProviderFirstParty:
		return resolveFirstParty(ctx, opts)
	case ProviderGateway:
		cred, ok := resolveFirstParty(ctx, opts)
		if !ok {
			return Credential{}, false
		}
		cred.Provider = ProviderGateway
		cred.BaseURL = opts.env("ANTHROPIC_BASE_URL")
		return cred, true
	case ProviderBedrock:
		return Credential{}, false
	case ProviderVertex:
		return resolveVertex(opts)
	default:
		return Credential{}, false
	}
}

// firstPartySources is the precedence ladder, highest first. apiKeyHelper is
// deliberately absent: honoring it means executing a command named in a
// settings file, which Claude Code itself gates behind workspace trust. AO
// will not turn a credential check into a path for running arbitrary commands
// out of a repository, so a helper-sourced credential resolves to nothing here
// and the verdict stays Unknown.
var firstPartySources = []struct {
	env  string
	kind Kind
}{
	{"CLAUDE_CODE_OAUTH_TOKEN", KindOAuthToken},
	{"ANTHROPIC_API_KEY", KindAPIKey},
	{"ANTHROPIC_AUTH_TOKEN", KindAuthToken},
}

func resolveFirstParty(ctx context.Context, opts ResolveOptions) (Credential, bool) {
	for _, source := range firstPartySources {
		if secret := opts.env(source.env); secret != "" {
			return Credential{
				Kind: source.kind, Secret: secret, Source: source.env, Provider: ProviderFirstParty,
			}, true
		}
	}
	// Source 5: the subscription login, stored in the keychain on macOS and in
	// a plain file everywhere else.
	if secret, source, kind, ok := loadOAuth(ctx, opts); ok {
		return Credential{
			Kind: kind, Secret: secret, Source: source, Provider: ProviderFirstParty,
		}, true
	}
	return Credential{}, false
}

// loadOAuth is the entire platform surface of this package.
//
// macOS keeps the subscription token in the keychain; Linux and Windows keep
// it in .credentials.json, and the Claude Code binary contains no reference to
// Windows Credential Manager, wincred, CredRead, or DPAPI — so there is no
// third storage backend to implement. The macOS path falls through to the file
// on any failure, which is the path the other two platforms always take, so
// non-Mac platforms exercise strictly less code rather than different code.
func loadOAuth(ctx context.Context, opts ResolveOptions) (secret, source string, kind Kind, ok bool) {
	if opts.goos() == "darwin" && opts.AllowKeychain {
		if secret, kind, ok := readKeychain(ctx, opts); ok {
			return secret, "keychain", kind, true
		}
		// Absent, locked, or denied. Fall through to the file.
	}
	s, src, ok := readCredentialsFile(ctx, opts)
	if !ok {
		return "", "", "", false
	}
	return s, src, KindOAuthToken, true
}

// readCredentialsFile reads ~/.claude/.credentials.json.
func readCredentialsFile(ctx context.Context, opts ResolveOptions) (secret, source string, ok bool) {
	_ = ctx
	dir, err := claudeConfigDir(opts)
	if err != nil {
		return "", "", false
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return "", "", false
	}
	token, ok := oauthTokenFromCredentialsJSON(data)
	if !ok {
		return "", "", false
	}
	return token, "credentials-file", true
}

// oauthTokenFromCredentialsJSON pulls the access token out of the credential
// file, tolerating both the nested and flat shapes Claude Code has written.
func oauthTokenFromCredentialsJSON(data []byte) (string, bool) {
	var payload struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false
	}
	for _, candidate := range []string{payload.ClaudeAiOauth.AccessToken, payload.AccessToken} {
		if token := strings.TrimSpace(candidate); token != "" {
			return token, true
		}
	}
	return "", false
}

// claudeConfigDir resolves Claude Code's config directory, honoring the same
// overrides the CLI does. Relative directories are anchored to the launch cwd,
// rather than the daemon cwd, so readers and child commands agree.
func claudeConfigDir(opts ResolveOptions) (string, error) {
	dir := strings.TrimSpace(opts.ConfigDir)
	if dir == "" {
		dir = opts.env("CLAUDE_CONFIG_DIR")
	}
	if dir == "" {
		homeKey := "HOME"
		if opts.goos() == "windows" {
			homeKey = "USERPROFILE"
		}
		home := opts.env(homeKey)
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("agentcreds: resolve home directory: %w", err)
			}
		}
		dir = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(strings.TrimSpace(opts.WorkingDir), dir)
	}
	return filepath.Abs(dir)
}

func configuredFoundryModels(opts ResolveOptions) []Model {
	seen := map[string]struct{}{}
	models := make([]Model, 0, 3)
	for _, name := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		id := opts.env(name)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, Model{ID: id})
	}
	return models
}

func resolveVertex(opts ResolveOptions) (Credential, bool) {
	region := firstNonEmpty(opts.env("CLOUD_ML_REGION"), opts.env("GOOGLE_CLOUD_REGION"), "us-east5")
	project := firstNonEmpty(opts.env("ANTHROPIC_VERTEX_PROJECT_ID"), opts.env("GOOGLE_CLOUD_PROJECT"))
	baseURL := opts.env("ANTHROPIC_VERTEX_BASE_URL")
	if token := opts.env("GOOGLE_OAUTH_ACCESS_TOKEN"); token != "" {
		return Credential{
			Kind: KindGoogleAccessToken, Secret: token, Source: "GOOGLE_OAUTH_ACCESS_TOKEN",
			Provider: ProviderVertex, Region: region, Project: project, BaseURL: baseURL,
		}, true
	}
	return Credential{}, false
}
