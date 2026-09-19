package modelcatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register sqlite driver for OMP credential probes

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// discoverOMPCatalog lists models through `omp models --json` and keeps only
// entries whose provider has an enabled credential in OMP's own stores
// (agent.db's auth_credentials table, or the legacy auth.json). OMP reports
// every model its registry knows, including providers the user never
// configured, so without this filter the picker offers choices that can only
// fail at launch.
func discoverOMPCatalog(ctx context.Context, binary, workingDir string, env map[string]string) (ports.AgentModelCatalog, error) {
	base, models, err := runModelCommand(ctx, "omp", binary, workingDir, env)
	if err != nil {
		return base, err
	}
	if len(models) == 0 {
		return base, errors.New("omp model discovery returned no models")
	}
	providers, authoritative, provErr := ompConfiguredProviders(env)
	switch {
	case provErr != nil:
		// A store that exists but cannot be read must not empty the picker:
		// publish the unfiltered catalog and let the service attach the warning.
		base.Models = models
		base.Source = "cli"
		base.FetchedAt = time.Now().UTC()
		return base, fmt.Errorf("omp provider credential probe failed: %w", provErr)
	case authoritative:
		models = filterModelsByConfiguredProviders(models, providers)
		if len(models) == 0 {
			return base, errors.New("omp has no models from providers with a configured API credential")
		}
	}
	// Not authoritative (no credential store exists yet, for example a setup
	// authenticated purely through provider environment variables): fail open
	// with the full catalog rather than hide every model.
	base.Models = models
	base.Source = "cli"
	base.FetchedAt = time.Now().UTC()
	return base, nil
}

// filterModelsByConfiguredProviders keeps models whose provider is configured.
// Models without provider attribution are kept: dropping unattributed entries
// risks hiding valid choices if OMP changes its output shape.
func filterModelsByConfiguredProviders(models []ports.AgentModelInfo, providers map[string]bool) []ports.AgentModelInfo {
	if len(providers) == 0 {
		return nil
	}
	out := make([]ports.AgentModelInfo, 0, len(models))
	for _, item := range models {
		if item.Provider == "" || providers[item.Provider] {
			out = append(out, item)
		}
	}
	return out
}

// ompStoreState classifies one OMP credential store probe.
type ompStoreState int

const (
	// ompStoreMissing means the store file does not exist.
	ompStoreMissing ompStoreState = iota
	// ompStoreEmpty means the store exists but holds no usable credential, so
	// it cannot decide what is configured.
	ompStoreEmpty
	// ompStoreDecisive means the store authoritatively reports the configured
	// providers (possibly an empty set when every credential is disabled).
	ompStoreDecisive
)

// ompConfiguredProviders returns the providers holding an enabled credential in
// OMP's stores. authoritative is false when no store can decide, in which case
// discovery must not filter (OMP may be authenticated through provider
// environment variables AO cannot enumerate).
func ompConfiguredProviders(env map[string]string) (providers map[string]bool, authoritative bool, err error) {
	dir, ok := ompConfigDir(env)
	if !ok {
		return nil, false, nil
	}
	providers = map[string]bool{}
	dbProviders, dbState, err := ompAgentDBProviders(filepath.Join(dir, "agent.db"))
	if err != nil {
		return nil, false, err
	}
	jsonProviders, jsonState, err := ompAuthJSONProviders(filepath.Join(dir, "auth.json"))
	if err != nil {
		return nil, false, err
	}
	for provider := range dbProviders {
		providers[provider] = true
	}
	for provider := range jsonProviders {
		providers[provider] = true
	}
	return providers, dbState == ompStoreDecisive || jsonState == ompStoreDecisive, nil
}

// ompConfigDir resolves OMP's config directory: an explicit project or process
// PI_CODING_AGENT_DIR first, then the default ~/.omp/agent. This mirrors the
// omp adapter's auth probe so the picker and the readiness badge agree.
func ompConfigDir(env map[string]string) (string, bool) {
	if dir := strings.TrimSpace(env["PI_CODING_AGENT_DIR"]); dir != "" {
		return dir, true
	}
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return dir, true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".omp", "agent"), true
}

// ompAgentDBProviders reads the enabled providers from OMP's SQLite credential
// store. The enabled predicate matches the omp adapter's auth probe: a
// credential counts when it carries data and is not disabled.
func ompAgentDBProviders(path string) (map[string]bool, ompStoreState, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, ompStoreMissing, nil
	} else if err != nil {
		return nil, ompStoreMissing, err
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, ompStoreMissing, err
	}
	defer func() {
		_ = db.Close()
	}()

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_credentials`).Scan(&total); err != nil {
		return nil, ompStoreMissing, err
	}
	if total == 0 {
		return nil, ompStoreEmpty, nil
	}
	rows, err := db.Query(`SELECT provider FROM auth_credentials WHERE trim(coalesce(data, '')) <> '' AND disabled_cause IS NULL`)
	if err != nil {
		return nil, ompStoreMissing, err
	}
	defer func() {
		_ = rows.Close()
	}()
	providers := map[string]bool{}
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, ompStoreMissing, err
		}
		if trimmed := strings.TrimSpace(provider); trimmed != "" {
			providers[trimmed] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, ompStoreMissing, err
	}
	// Rows exist: either some are enabled, or every credential is disabled and
	// definitively nothing is configured through this store.
	return providers, ompStoreDecisive, nil
}

// ompAuthJSONProviders reads providers with a stored key from OMP's legacy
// auth.json. Entries without a key do not count, matching the omp adapter's
// auth probe.
func ompAuthJSONProviders(path string) (map[string]bool, ompStoreState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, ompStoreMissing, nil
	}
	if err != nil {
		return nil, ompStoreMissing, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, ompStoreEmpty, nil
	}
	var entries map[string]struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, ompStoreMissing, err
	}
	providers := map[string]bool{}
	for provider, entry := range entries {
		if strings.TrimSpace(provider) == "" || strings.TrimSpace(entry.Key) == "" {
			continue
		}
		providers[provider] = true
	}
	if len(providers) == 0 {
		return nil, ompStoreEmpty, nil
	}
	return providers, ompStoreDecisive, nil
}

// ompProvidersFingerprint hashes the configured provider set so toggling an
// OMP credential invalidates the cached catalog. Secrets never enter the
// fingerprint; only provider identity does. "" means AO cannot decide, and
// the cache key stays binary-only.
func ompProvidersFingerprint(env map[string]string) string {
	providers, authoritative, err := ompConfiguredProviders(env)
	if err != nil || !authoritative {
		return ""
	}
	names := make([]string, 0, len(providers))
	for provider := range providers {
		names = append(names, provider)
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return fmt.Sprintf("%x", sum[:8])
}
