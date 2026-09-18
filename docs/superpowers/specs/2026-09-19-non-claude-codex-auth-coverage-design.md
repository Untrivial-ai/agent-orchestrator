# Non-Claude/Codex Agent Authentication Coverage Design

## Scope

This change makes authentication detection accurate for every registered agent except Claude Code and Codex. Those two adapters, their credential resolution, and their account-management behavior are explicitly out of scope.

The included adapters are Agy, Aider, Amp, Auggie, Autohand, Cline, Continue (`cn`), Copilot CLI, Crush, Cursor CLI, Devin, Droid, Goose, Grok, Kilo Code, Kimchi, Kimi Code, Kiro, Muse, OpenCode, OMP, Pi, Prime Agent, Qwen Code, and Mistral Vibe.

The work is backend-only. The frontend recovery experience is a separate follow-up PR after this contract is merged.

## Status Contract

Authentication checks use five states:

- `authorized`: an official native probe or provider request validated the effective credential successfully.
- `configured`: the effective credential or credential chain exists locally but has not been validated remotely.
- `unauthorized`: an authoritative native or provider response rejected or expired the effective credential.
- `unknown`: AO lacks enough evidence, cannot safely resolve the effective provider, or a probe failed inconclusively.
- `not_applicable`: the effective provider/model is explicitly local or otherwise documented to require no authentication.

Installation remains separate. A missing binary continues to produce the existing unavailable installation outcome; it must not be represented as an authentication result.

Local file, environment, database, keychain, AWS profile, ADC, Azure identity, or CLI “credential listed” evidence never produces `authorized` by itself. Malformed or incomplete local evidence is ignored in favor of another valid source; if no source remains, the result is `unknown`. A locally expired credential may produce `unauthorized` only when the credential format has a trustworthy expiry field and no refresh path that the native client can use.

## Global and Scoped Checks

The existing `AgentAuthChecker.AuthStatus(context.Context)` remains the device-wide compatibility boundary. It is used by the catalog and cached readiness coordinator and cannot assume a workspace or selected model.

Add an optional `AgentScopedAuthChecker` capability:

```go
type AgentAuthCheck struct {
	WorkingDir string
	DataDir    string
	Config     AgentConfig
	Env        map[string]string
	Args       []string
	Interactive bool
}

type AgentScopedAuthChecker interface {
	AuthStatusFor(context.Context, AgentAuthCheck) (AgentAuthStatus, error)
}
```

Adapters that support project configuration, provider/model selection, or invocation-specific credentials implement the scoped capability. Their existing `AuthStatus` delegates to the same resolver with an empty scope. Launch and agent-switch paths prefer the scoped checker after the effective agent configuration, workspace, data directory, environment, and command arguments are known. Global readiness remains cached by agent ID; scoped results are ephemeral and must never overwrite the device-wide cache because two projects can select different providers.

Claude Code and Codex continue using their existing implementations. Adding the optional interface must not require modifying either adapter.

## Shared Local-Evidence Utilities

Add a small internal `authutil` package for behavior repeated by multiple adapters:

- bounded file reads with regular-file checks;
- environment lookup through an injectable function;
- JSON, TOML, YAML, and dotenv decoding without recursive “any key-looking field” heuristics;
- upward project-file discovery that stops at the filesystem root;
- expiry validation for numeric and RFC3339 timestamps;
- bounded, injectable command execution;
- macOS Keychain generic-password lookup with fixed service/account selectors;
- AWS default-chain evidence, Google ADC evidence, and Azure CLI/identity evidence;
- evidence reduction into the five status values.

Helpers return metadata and state, never raw secrets to logs or API responses. Keychain commands use `-w`, fixed service names, a short timeout, and no shell. Cloud-chain resolution is bounded; metadata-service, SSO refresh, and CLI failures remain `unknown` or fall back to other evidence. Tests inject environment, filesystem roots, clocks, operating system, command runners, and cloud-chain loaders.

Do not turn `backend/pkg/agentcreds` into a generic parser. It owns provider round-trip validation and can be reused when an adapter can identify a supported provider precisely. Adapter-specific precedence and schemas remain beside each adapter.

## Native Probe Rules

Generic substring matching is not authoritative enough for structured commands. Cursor and Kiro parse their documented JSON output. Amp parses `amp usage --no-color`; Devin parses `devin auth status`; Pi uses `pi auth check --provider`; other adapters use only their documented commands.

A native command can return:

- `authorized` when it actually validates access with the provider;
- `configured` when it only lists or reports locally stored credentials;
- `unauthorized` when its documented output explicitly reports rejection or signed-out state;
- `unknown` for timeouts, unsupported versions, unexpected output, or execution errors without a documented authentication meaning.

The shared `authprobe` package must stop treating generic positive phrases as proof of `authorized`. Adapter-specific parsers own positive classification; the generic helper may continue recognizing explicit negative output conservatively.

## Adapter Requirements

### Crush

Resolve Crush configuration using its documented precedence: global data, global config, `crushrc`, project `.crushrc`/`.crush.json`, and top-level `env`. Recognize `providers.<id>.oauth` and `providers.<id>.api_key`, the full AWS default credential chain, Azure Entra ID, Google ADC where the selected provider supports it, and explicit local/custom no-auth providers. Validate only the active provider/model. Update login guidance to include `crush login openai`. Fix tests to clear every supported credential variable.

### Qwen Code

Resolve user, system, system-default, and upward project settings and dotenv files, including both system-path override variables. Respect `security.auth.selectedType`, the selected model/provider, custom `envKey`, and required base URL. Add the documented provider variables, Vertex ADC with `GOOGLE_CLOUD_PROJECT` and `GOOGLE_MODEL`, and CLI-supplied scope when AO has one. Discontinued OAuth cache presence is not valid evidence.

### OpenCode

Keep the official `auth.json` and `opencode auth list` sources, classify them as configured unless the command performs validation, and add Bedrock AWS default-profile/credential-file evidence. Do not make implementation-derived database locations part of the documented contract. Local/free selected providers return `not_applicable` when scope identifies them.

### Grok

Honor `GROK_AUTH_PATH`, inline `GROK_AUTH`, stored `key`, `XAI_API_KEY`, `GROK_DEPLOYMENT_KEY`, `[endpoints].deployment_key`, and model `api_key`/`env_key`. Remove `access_token` as the primary stored-field assumption and remove stale `GROK_API_KEY` guidance.

### Cursor CLI

Keep `CURSOR_API_KEY` and the native status command. Parse `agent status --format json` rather than undocumented `~/.cursor/cli-config.json/authInfo` or generic text. The transient `--api-key` flag is considered only when present in scoped launch configuration.

### GitHub Copilot CLI

Preserve documented token precedence: `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, OS keychain service `copilot-cli`, `gh auth token`, then `COPILOT_HOME`/`~/.copilot/config.json` fallback. Session event logs are never authentication evidence.

### Kilo Code

Parse OAuth entries as `{type:"oauth", access, refresh, expires}`; inspect the current `credential` database table; join selected accounts to actual credentials; honor expiry; resolve the database through `KILO_DB` or `kilo db path`; inspect `KILO_AUTH_CONTENT`; and detect selected-provider `options.apiKey`. Fix zero-count `kilo auth list` parsing when the environment section is omitted. Do not rely on unsupported `KILO_DATA_DIR`. Free/local selected providers return `not_applicable`.

### Kimi Code

Use current and legacy configuration locations, but apply current Kimi Code semantics before legacy fallbacks. Add `api_key_env`, `KIMI_MODEL_NAME` plus `KIMI_MODEL_API_KEY`, Vertex ADC, and exact custom `Authorization` headers. Do not treat bare `KIMI_API_KEY`, `OPENAI_API_KEY`, `KIMI_CODE_API_KEY`, or `MOONSHOT_API_KEY` as current Kimi Code credentials unless legacy mode/config selects them. Keep source-confirmed legacy migration/keyring support conservative.

### Muse

When `auth.json` selects `storage: "keychain"`, query Muse’s documented macOS Keychain item. File-backed OAuth/API-key evidence and `META_API_KEY` remain supported. Do not add `settings.json` as an auth source.

### Droid

Recognize standard browser-login state, global and project `settings.json`/`settings.local.json`, legacy `config.json` custom models, scoped `--settings`, `apiKeyHelper`, static `extraHeaders`, AWS default-chain evidence, and active custom model/provider requirements. Keyless trusted endpoints are `not_applicable` only when the active configuration explicitly selects them.

### Agy

Replace installed-means-authorized behavior. Detect its OS keyring entry. Gemini API-key mode requires both `modelProvider: "gemini"` in `~/.gemini/antigravity-cli/settings.json` and `GEMINI_API_KEY`. Do not accept `GOOGLE_API_KEY`, dotenv files, or Google ADC.

### Amp

Run `amp usage --no-color` before local fallbacks. A successful live usage response is `authorized`; explicit signed-out/rejection is `unauthorized`. `AMP_API_KEY` is only configured evidence and must have the documented `sgamp_` shape. Parse only official credential fields in `secrets.json`; empty or unrecognized files do not suppress the live probe. Remove undocumented settings credential keys and support JSONC only for documented settings behavior.

### Goose

Resolve the Goose keyring (`service=goose`, `account=secrets`), `GOOSE_PATH_ROOT/config/secrets.yaml`, Windows AppData secrets, provider OAuth caches, AWS default chains, Google ADC, Azure CLI/AD evidence, custom `api_key_env`, and command-based credentials. Resolve the selected provider from provider metadata rather than accepting any catalog key. `config.yaml` alone is not a credential store. Ollama/LM Studio selected providers return `not_applicable`.

### Devin

Keep `devin auth status` authoritative and add XDG, default Unix, and Windows `credentials.toml` fallbacks as configured evidence. Do not treat `devin_api_url` as authentication. `DEVIN_API_KEY` remains conservative because it is documented for API/handoff rather than normal CLI login. `WINDSURF_API_KEY` is inspected only for an ACP-scoped check.

### Auggie

Parse `AUGMENT_SESSION_AUTH` as session JSON rather than accepting any non-empty string. Require the documented session fields, validate expiry, and support scoped `--augment-session-json` inline JSON or path. File and service-account sessions are configured evidence until validated. Missing/malformed input is `unknown`.

### Mistral Vibe

Resolve the active model and provider across global and project `.vibe/config.toml`. Parse TOML and dotenv using compatible parsers, respect the active provider’s `api_key_env_var`, and stop accepting unrelated-provider keys. Use `MISTRAL_API_KEY` for the default Vibe Code provider; remove standalone `VIBE_CODE_API_KEY` proof. Built-in `llamacpp` returns `not_applicable`.

### Autohand

Support JSON, TOML, YAML, and YML config; `AUTOHAND_HOME`; scoped `--config`; provider and extension `apiKey` values; `AUTOHAND_AI_API_KEY` and the documented provider variables; `auth.apiKeyHelper` bare mode; AWS, Google, and Azure chains; local Ollama/llama.cpp/MLX providers; and `auth.expiresAt`. Keep account auth distinct from active model-provider auth and remove undocumented standalone variables as proof.

### Cline

Honor `CLINE_DATA_DIR`, source-confirmed `CLINE_DIR` and `CLINE_PROVIDER_SETTINGS_PATH`, scoped `--config`, `--data-dir`, and `--key`, plus `settings.auth.apiKey`. Resolve AWS, Google ADC, Azure identity, SAP client credentials, local providers, and the selected `cline`/`cline-pass` provider entry.

### Kimchi

Add subscription/upstream-provider authentication, project `.kimchi/config.json` precedence, active endpoint/key pairing, and explicit rejection handling when an official probe is available. Local key or subscription presence is configured; malformed endpoints and unrelated keys do not pass.

### Pi

Parse OAuth entries using `access`, `refresh`, and `expires`, provider-scoped environment entries in `auth.json`, the complete documented provider environment list, AWS chains, Vertex ADC, and custom-provider `models.json`. Use the provider-specific `pi auth check --provider` command when the active provider is known. Local/no-auth providers return `not_applicable`; use `GEMINI_API_KEY`, not `GOOGLE_API_KEY`.

### Continue (`cn`)

Replace installed-means-authorized behavior. Recognize Continue browser-login state, `CONTINUE_API_KEY`, locally entered Anthropic keys, and provider credentials referenced from the selected `~/.continue/config.yaml` or scoped config. Because Continue has no documented non-interactive status command, all local evidence is `configured` and absence is `unknown`.

### Prime Agent

Resolve credentials from AO’s actual Prime runtime config directory before the default `~/.prime/agent`. Add Copilot token variables, `MOONSHOT_API_KEY`, `AWS_BEDROCK_SKIP_AUTH=1`, `AWS_WEB_IDENTITY_TOKEN_FILE` without requiring `AWS_ROLE_ARN`, and scoped runtime `--api-key`. Reject expired OAuth, unresolved command/environment references, and unrelated recursive fields. Do not add unsupported PI or Google-project aliases.

### OMP

Validate database credential type, payload, enabled state, and expiry; match the selected provider; honor `disabledProviders`; and add provider environment/dotenv, `models.yml` custom `apiKey`, scoped `--api-key`, and auth-broker/cache evidence. Local/custom `auth: none` providers return `not_applicable`. Undocumented `auth.json` is at most configured fallback evidence.

### Aider

Resolve dotenv and `.aider.conf.yml` from the session workspace/git root, plus `AIDER_ENV_FILE`, `--env-file`, and `--config`. Add documented LiteLLM variables, Bedrock AWS chains, Vertex configuration/ADC, Azure credentials, `GITHUB_COPILOT_TOKEN`, LM Studio, and optional Ollama key support. Parse YAML structurally and validate provider/key pairing. Selected Ollama without a required key returns `not_applicable`.

### Kiro

Run and structurally parse `kiro-cli whoami --format json` before considering `KIRO_API_KEY`. A successful browser-session result is `authorized` only when the command’s documented contract validates it; explicit signed-out output is `unauthorized`. `KIRO_API_KEY` is configured evidence and only applies to non-interactive scope.

## Testing

Every production behavior is introduced test-first. Adapter tests use temporary homes/workspaces, complete environment cleanup, literal fixtures matching official schemas, fake clocks, and injected command/keychain/cloud loaders. No test contacts a provider, OS credential service, metadata endpoint, or the user’s real home directory.

Contract tests cover all five states, readiness derivation, compatibility projections, API schema generation, scoped-check isolation, and launch/switch behavior. Each adapter covers every newly supported source, precedence, malformed and expired inputs, unrelated-provider rejection, and absence behavior.

Verification runs focused package tests first, then `go test ./...`, `go test -race ./...`, `go vet ./...`, `npm run api`, `npm run lint`, and `npm run frontend:typecheck`. Docker-backed workflow validation is reported separately if unavailable.

## Explicit Non-goals

- No Claude Code or Codex auth behavior changes.
- No frontend recovery UI; that is PR 2.
- No storage of credentials or probe results outside the existing in-memory readiness cache.
- No provider calls from tests.
- No claim that local credential presence proves authorization.
- No generic recursive search for fields containing `key`, `token`, or `secret`.
