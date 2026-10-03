package deepseekharness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// credentialsPathEnv, when set, points AO at a DeepSeek Harness credential store
// other than the default location.
const credentialsPathEnv = "DSH_CREDENTIALS_PATH"

// deepseekCredentialKey is the credential DeepSeek Harness stores for the
// first-party "deepseek-official" model route, and the one a prompt needs. Other
// entries in the same store (an OpenCode Go key, for example) authorize a
// different provider and must not be read as DeepSeek Harness being ready.
//
// It names an environment variable and a store entry, and carries no secret of
// its own.
const deepseekCredentialKey = "DEEPSEEK_API_KEY" //nolint:gosec // a credential's name, not a credential value

// AuthStatus reports whether DeepSeek Harness has a DeepSeek credential. The
// Harness ACP server advertises no authentication surface (its initialize
// response carries no auth methods and authenticate always succeeds), so the
// only local evidence is the credential store and the environment. Absent
// evidence stays unknown: Harness may be routed through a provider configured in
// its profile, which AO cannot see from here.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := deepseekLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func deepseekLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(os.Getenv(deepseekCredentialKey)) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	path, err := deepseekCredentialsPath()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	return deepseekCredentialsStatus(path)
}

// deepseekCredentialsPath resolves the Harness credential store. DSH_HOME moves
// the whole Harness home, so honour it the same way AO's other adapters honour
// their CLI's home overrides.
func deepseekCredentialsPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(credentialsPathEnv)); override != "" {
		return override, nil
	}
	home := strings.TrimSpace(os.Getenv("DSH_HOME"))
	if home == "" {
		resolved, err := os.UserHomeDir()
		if err != nil || resolved == "" {
			return "", err
		}
		home = filepath.Join(resolved, ".dsh")
	}
	return filepath.Join(home, ".credentials.yaml"), nil
}

// deepseekCredentialsStatus reads the store without exposing its contents. The
// file is YAML: a current Harness writes `version: 1` with credentials under
// `refs:`, mapping each addressable name straight to its secret, so a
// DEEPSEEK_API_KEY ref with a non-empty value is the evidence AO looks for. The
// walk stays shape-tolerant on purpose — it also accepts the pre-release flat
// layout and a nested secret — because AO only decides whether to show the
// setup action, and Harness owns the document's real validation.
//
// A scoped record (`records: <scope>/<id>`) carries no such ref, so a store
// holding only those stays unknown rather than claiming a route AO cannot
// attribute to DeepSeek.
func deepseekCredentialsStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the Harness credential store, resolved above
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var store any
	if err := yaml.Unmarshal(data, &store); err != nil {
		return ports.AgentAuthStatusUnknown, false, fmt.Errorf("parse DeepSeek Harness credential store: %w", err)
	}
	if !supportedStoreLayout(store) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if credentialEntryHasSecret(store, deepseekCredentialKey) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// supportedStoreLayout reports whether the document is one a current DeepSeek
// Harness can load. Anything without `version: 1` — notably the pre-release
// flat layout of bare name/secret pairs — is refused outright by Harness
// ("uses the pre-release flat layout"), taking its whole credentials service
// down with it. Reading such a file as authorization would badge the harness
// ready for a session that cannot start, so it is not evidence.
func supportedStoreLayout(store any) bool {
	root, ok := store.(map[string]any)
	if !ok {
		return false
	}
	switch version := root["version"].(type) {
	case int:
		return version == 1
	case float64:
		return version == 1
	case string:
		return strings.TrimSpace(version) == "1"
	default:
		return false
	}
}

// credentialEntryHasSecret walks the store for the entry named key and reports
// whether that entry, and only that entry, holds a non-empty secret. Scoping to
// the entry matters: a populated OpenCode Go key in the same store says nothing
// about DeepSeek credentials.
func credentialEntryHasSecret(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for entryKey, child := range typed {
			if entryKey == key && secretValuePresent(child) {
				return true
			}
			if credentialEntryHasSecret(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if credentialEntryHasSecret(child, key) {
				return true
			}
		}
	}
	return false
}

// secretValuePresent reports whether a credential entry holds a usable secret,
// accepting the scalar form and the nested {payload: {secret: …}} form the
// Harness store uses.
func secretValuePresent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		for entryKey, child := range typed {
			switch strings.ToLower(entryKey) {
			case "secret", "value", "key", "token", "payload":
				if secretValuePresent(child) {
					return true
				}
			}
		}
		return false
	case []any:
		for _, child := range typed {
			if secretValuePresent(child) {
				return true
			}
		}
		return false
	default:
		return false
	}
}
