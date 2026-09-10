package agentcreds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// OSEnv reads the real process environment.
func OSEnv(name string) string { return os.Getenv(name) }

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
// credential that belongs to Bedrock. The value should come from the CLI's own
// reported apiProvider where available; the env fallbacks below only apply
// when the CLI could not be asked.
func ResolveProvider(reported string, opts ResolveOptions) (Provider, bool) {
	if trimmed := strings.TrimSpace(reported); trimmed != "" {
		return ParseProvider(trimmed)
	}
	switch {
	case opts.env("CLAUDE_CODE_USE_BEDROCK") != "":
		return ProviderBedrock, true
	case opts.env("CLAUDE_CODE_USE_VERTEX") != "":
		return ProviderVertex, true
	case opts.env("ANTHROPIC_FOUNDRY_API_KEY") != "" || opts.env("ANTHROPIC_FOUNDRY_AUTH_TOKEN") != "":
		return ProviderFoundry, true
	case opts.env("ANTHROPIC_BASE_URL") != "":
		return ProviderGateway, true
	default:
		return ProviderFirstParty, true
	}
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
	case ProviderFoundry:
		return resolveFoundry(opts)
	case ProviderBedrock:
		return resolveBedrock(opts)
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
	if secret, source, ok := loadOAuth(ctx, opts); ok {
		return Credential{
			Kind: KindOAuthToken, Secret: secret, Source: source, Provider: ProviderFirstParty,
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
func loadOAuth(ctx context.Context, opts ResolveOptions) (secret, source string, ok bool) {
	if opts.goos() == "darwin" && opts.AllowKeychain {
		if secret, ok := readKeychain(ctx, opts); ok {
			return secret, "keychain", true
		}
		// Absent, locked, or denied. Fall through to the file.
	}
	return readCredentialsFile(opts)
}

// readCredentialsFile reads ~/.claude/.credentials.json.
func readCredentialsFile(opts ResolveOptions) (secret, source string, ok bool) {
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
// overrides the CLI does.
func claudeConfigDir(opts ResolveOptions) (string, error) {
	if opts.ConfigDir != "" {
		return opts.ConfigDir, nil
	}
	if dir := opts.env("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir := opts.env("XDG_CONFIG_HOME"); dir != "" && opts.goos() != "windows" {
		return filepath.Join(dir, "claude"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agentcreds: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

func resolveFoundry(opts ResolveOptions) (Credential, bool) {
	resource := opts.env("ANTHROPIC_FOUNDRY_RESOURCE")
	if key := opts.env("ANTHROPIC_FOUNDRY_API_KEY"); key != "" {
		return Credential{
			Kind: KindAzureAPIKey, Secret: key, Source: "ANTHROPIC_FOUNDRY_API_KEY",
			Provider: ProviderFoundry, Resource: resource, BaseURL: opts.env("ANTHROPIC_FOUNDRY_BASE_URL"),
		}, true
	}
	if token := opts.env("ANTHROPIC_FOUNDRY_AUTH_TOKEN"); token != "" {
		return Credential{
			Kind: KindAuthToken, Secret: token, Source: "ANTHROPIC_FOUNDRY_AUTH_TOKEN",
			Provider: ProviderFoundry, Resource: resource, BaseURL: opts.env("ANTHROPIC_FOUNDRY_BASE_URL"),
		}, true
	}
	return Credential{}, false
}

func resolveBedrock(opts ResolveOptions) (Credential, bool) {
	region := firstNonEmpty(opts.env("AWS_REGION"), opts.env("AWS_DEFAULT_REGION"))
	if token := opts.env("AWS_BEARER_TOKEN_BEDROCK"); token != "" {
		return Credential{
			Kind: KindAuthToken, Secret: token, Source: "AWS_BEARER_TOKEN_BEDROCK",
			Provider: ProviderBedrock, Region: region,
		}, true
	}
	accessKey := opts.env("AWS_ACCESS_KEY_ID")
	secretKey := opts.env("AWS_SECRET_ACCESS_KEY")
	if accessKey != "" && secretKey != "" {
		// Packed as one secret so there is a single field to keep out of logs.
		packed := accessKey + "\n" + secretKey + "\n" + opts.env("AWS_SESSION_TOKEN")
		return Credential{
			Kind: KindAWSSigV4, Secret: packed, Source: "AWS_ACCESS_KEY_ID",
			Provider: ProviderBedrock, Region: region,
		}, true
	}
	// Anything else is chain-sourced: IMDS, container credentials, an SSO
	// profile, credential_process. Not resolvable here by design.
	return Credential{}, false
}

func resolveVertex(opts ResolveOptions) (Credential, bool) {
	region := firstNonEmpty(opts.env("CLOUD_ML_REGION"), opts.env("GOOGLE_CLOUD_REGION"), "us-east5")
	project := firstNonEmpty(opts.env("ANTHROPIC_VERTEX_PROJECT_ID"), opts.env("GOOGLE_CLOUD_PROJECT"))
	if token := opts.env("GOOGLE_OAUTH_ACCESS_TOKEN"); token != "" {
		return Credential{
			Kind: KindGoogleAccessToken, Secret: token, Source: "GOOGLE_OAUTH_ACCESS_TOKEN",
			Provider: ProviderVertex, Region: region, Project: project,
		}, true
	}
	if path := opts.env("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Credential{}, false
		}
		cred := Credential{
			Kind: KindGoogleServiceAccount, Secret: string(data), Source: "GOOGLE_APPLICATION_CREDENTIALS",
			Provider: ProviderVertex, Region: region, Project: project,
		}
		if cred.Project == "" {
			// A service-account key names its own project, which saves the
			// user from having to set a second variable.
			var key serviceAccountKey
			if json.Unmarshal(data, &key) == nil {
				cred.Project = key.ProjectID
			}
		}
		return cred, true
	}
	return Credential{}, false
}
