// Package modelcatalog normalizes the heterogeneous model-list surfaces
// exposed by supported agent CLIs.
package modelcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	// Model-list commands may initialize provider registries or refresh remote
	// metadata before printing their catalog. Kilo Code and OpenCode can exceed
	// eight seconds on a normal warm installation, so keep one consistent,
	// bounded allowance for every command-backed adapter.
	commandTimeout = 20 * time.Second

	// Some CLIs leave descendants holding stdout/stderr open after the parent is
	// canceled. Bound that pipe-drain wait so a timed-out discovery request does
	// not linger well beyond commandTimeout.
	commandTerminationWait = 2 * time.Second
)

type commandSpec struct {
	args   []string
	parser func([]byte) ([]ports.AgentModelInfo, error)
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[[:alpha:]]`)

var commandSpecs = map[string]commandSpec{
	"aider":    {args: []string{"--no-check-update", "--no-git", "--no-gitignore", "--no-analytics", "--list-models", "."}, parser: parseIDLines},
	"autohand": {args: []string{"models", "list"}, parser: parseIDLines},
	"opencode": {args: []string{"--pure", "models"}, parser: parseIDLines},
	"grok":     {args: []string{"models"}, parser: parseGrokModels},
	"cursor":   {args: []string{"models"}, parser: parseCursorModels},
	"agy":      {args: []string{"models"}, parser: parseIDLines},
	"kilocode": {args: []string{"models"}, parser: parseIDLines},
	"pi":       {args: []string{"--list-models"}, parser: parsePiModels},
	"kimchi":   {args: []string{"--list-models"}, parser: parsePiModels},
	"kimi":     {args: []string{"provider", "list", "--json"}, parser: parseJSONModels},
	"auggie":   {args: []string{"models", "list", "--json"}, parser: parseJSONModels},
	"devin":    {args: []string{"models", "list", "--format", "json"}, parser: parseJSONModels},
	"kiro":     {args: []string{"chat", "--list-models", "--format", "json"}, parser: parseJSONModels},
}

// Base returns the picker behavior AO can provide without executing a CLI.
func Base(agentID string) ports.AgentModelCatalog {
	now := time.Now().UTC()
	switch agentID {
	case "claude-code":
		return catalog(agentID, "official-aliases", true, now,
			model("sonnet", "Sonnet", false),
			model("opus", "Opus", false),
			model("haiku", "Haiku", false),
		)
	case "codex":
		return catalog(agentID, "official-catalog", true, now,
			model("gpt-5.6-sol", "GPT-5.6 Sol", true),
			model("gpt-5.6-terra", "GPT-5.6 Terra", false),
			model("gpt-5.6-luna", "GPT-5.6 Luna", false),
			model("gpt-5.5", "GPT-5.5", false),
			model("gpt-5.4", "GPT-5.4", false),
			model("gpt-5.4-mini", "GPT-5.4 mini", false),
			model("gpt-5.3-codex", "GPT-5.3-Codex", false),
		)
	case "muse":
		return catalog(agentID, "official-catalog", true, now,
			model("muse-spark", "Muse Spark", true),
			model("muse-spark-1.1", "Muse Spark 1.1", false),
			model("muse-spark-1.2", "Muse Spark 1.2", false),
		)
	case "amp":
		c := catalog(agentID, "official-modes", false, now,
			model("low", "Low", false),
			model("medium", "Medium", true),
			model("high", "High", false),
			model("ultra", "Ultra", false),
		)
		c.SelectionMode = ports.ModelSelectionModeList
		return c
	default:
		if hasDiscoverySource(agentID) {
			return ports.AgentModelCatalog{
				AgentID:       agentID,
				SelectionMode: ports.ModelSelectionCatalog,
				Models:        []ports.AgentModelInfo{},
				AllowCustom:   true,
				Source:        "cli",
				FetchedAt:     now,
			}
		}
		return Manual(agentID)
	}
}

// Manual returns the free-text fallback used when an adapter has no reliable
// catalog or when discovery fails before AO has a successful cached catalog.
func Manual(agentID string) ports.AgentModelCatalog {
	return ports.AgentModelCatalog{
		AgentID:       agentID,
		SelectionMode: ports.ModelSelectionText,
		Models:        []ports.AgentModelInfo{},
		AllowCustom:   true,
		Source:        "manual",
		FetchedAt:     time.Now().UTC(),
	}
}

// Discoverer implements the model-discovery port for production daemon wiring.
type Discoverer struct{}

// Discover executes a documented non-interactive model-list command when the
// agent exposes one. Static catalogs are returned without executing the binary.
func (Discoverer) Discover(ctx context.Context, request ports.AgentModelDiscoveryRequest) (ports.AgentModelCatalog, error) {
	return Discover(ctx, request.AgentID, request.Binary, request.WorkingDir, request.Env)
}

// CatalogFingerprint returns a stable fingerprint of the discovery inputs: the
// installed agent binary plus the configuration this adapter reads to build the
// catalog. Folding configuration in is what lets a settings edit invalidate a
// cached catalog, since the binary alone does not change when settings do.
func (Discoverer) CatalogFingerprint(ctx context.Context, request ports.AgentModelDiscoveryRequest) string {
	return CatalogFingerprint(ctx, request.AgentID, request.Binary, request.WorkingDir, request.Env)
}

// Manual returns the manual-entry fallback catalog for an agent.
func (Discoverer) Manual(agentID string) ports.AgentModelCatalog { return Manual(agentID) }

// Discover executes model catalog discovery for an agent binary.
func Discover(ctx context.Context, agentID, binary, workingDir string, env map[string]string) (ports.AgentModelCatalog, error) {
	base := Base(agentID)
	if agentID == "claude-code" {
		return withClaudeCodeDefault(base, workingDir, env), nil
	}
	spec, ok := commandSpecs[agentID]
	if !ok {
		return base, nil
	}
	if strings.TrimSpace(binary) == "" {
		return base, errors.New("agent binary is not installed")
	}
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := modelCommand(runCtx, binary, spec.args, workingDir, env)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return base, modelDiscoveryError(runCtx, agentID, err)
	}
	models, err := spec.parser(output)
	if err != nil {
		return base, fmt.Errorf("%s model discovery: %w", agentID, err)
	}
	models = normalize(models)
	if len(models) == 0 {
		return base, fmt.Errorf("%s model discovery returned no models", agentID)
	}
	base.Models = models
	base.Source = "cli"
	base.FetchedAt = time.Now().UTC()
	return base, nil
}

// claudeCodeSettingsReadLimit bounds how much of a settings file AO parses. The
// documented files are small; a pathological one must not stall discovery.
const claudeCodeSettingsReadLimit = 1 << 20

// withClaudeCodeDefault flags the model Claude Code will actually run with.
// Claude Code exposes no non-interactive command that reports its resolved
// model, but the value it resolves is readable: AO passes no --model, so the
// choice comes from ANTHROPIC_MODEL or the "model" key in the settings files,
// nearest scope first. When none of them set a model the CLI picks internally
// and AO must not guess, so the catalog keeps no default and the picker keeps
// showing "Agent default".
func withClaudeCodeDefault(base ports.AgentModelCatalog, workingDir string, env map[string]string) ports.AgentModelCatalog {
	resolved := claudeCodeResolvedModel(workingDir, env)
	if resolved == "" {
		return base
	}
	models := make([]ports.AgentModelInfo, len(base.Models))
	copy(models, base.Models)
	base.Models = models
	for i := range base.Models {
		if strings.EqualFold(base.Models[i].ID, resolved) {
			base.Models[i].IsDefault = true
			return base
		}
	}
	// A configured model that is not one of the published aliases (an explicit
	// snapshot id, or an alias variant such as "opus[1m]") still has to be
	// selectable, otherwise the picker would show a default it cannot round-trip.
	base.Models = append(base.Models, model(resolved, resolved, true))
	return base
}

// claudeCodeResolvedModel returns the configured Claude Code model, or "" when
// no scope sets one. Order mirrors Claude Code's own precedence, narrowed to the
// sources AO can read without running the CLI.
func claudeCodeResolvedModel(workingDir string, env map[string]string) string {
	if fromEnv := strings.TrimSpace(env["ANTHROPIC_MODEL"]); fromEnv != "" {
		return fromEnv
	}
	if fromEnv := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")); fromEnv != "" {
		return fromEnv
	}
	var candidates []string
	if dir := strings.TrimSpace(workingDir); dir != "" {
		candidates = append(candidates,
			filepath.Join(dir, ".claude", "settings.local.json"),
			filepath.Join(dir, ".claude", "settings.json"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".claude", "settings.json"))
	}
	for _, candidate := range candidates {
		if configured := claudeCodeSettingsModel(candidate); configured != "" {
			return configured
		}
	}
	return ""
}

// claudeCodeSettingsModel reads one settings file's "model". An unreadable or
// malformed file is not an error worth surfacing: the picker degrades to no
// default, exactly as if the key were absent.
func claudeCodeSettingsModel(path string) string {
	file, err := os.Open(path) //nolint:gosec // path is derived from the project dir and the user's home
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, claudeCodeSettingsReadLimit))
	if err != nil {
		return ""
	}
	var settings struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return ""
	}
	return strings.TrimSpace(settings.Model)
}

func hasDiscoverySource(agentID string) bool {
	_, hasCommand := commandSpecs[agentID]
	return hasCommand
}

func modelCommand(ctx context.Context, binary string, args []string, workingDir string, env map[string]string) *exec.Cmd {
	cmd := aoprocess.CommandContext(ctx, binary, args...) //nolint:gosec // binary is adapter-resolved, args are static
	cmd.WaitDelay = commandTerminationWait
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = mergedEnvironment(os.Environ(), env)
	return cmd
}

func mergedEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(base)+len(keys))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[key]; !replaced {
			out = append(out, item)
		}
	}
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

func modelDiscoveryError(runCtx context.Context, agentID string, commandErr error) error {
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s model discovery timed out after %s", agentID, commandTimeout)
	}
	if errors.Is(runCtx.Err(), context.Canceled) {
		return fmt.Errorf("%s model discovery canceled: %w", agentID, context.Canceled)
	}
	return fmt.Errorf("%s model discovery: %w", agentID, commandErr)
}

// BinaryVersion returns a short non-sensitive executable-metadata fingerprint
// for cache invalidation. It deliberately does not start the agent: statting
// the resolved executable keeps cache validation fast and side-effect free.
func BinaryVersion(ctx context.Context, binary string) string {
	if ctx.Err() != nil || strings.TrimSpace(binary) == "" {
		return ""
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return ""
	}
	if evaluated, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = evaluated
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(resolved))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatInt(info.ModTime().UnixNano(), 10)))
	return fmt.Sprintf("%x", hash.Sum(nil)[:8])
}

// CatalogFingerprint hashes every discovery input for an agent: the resolved
// executable, plus the configuration values the adapter reads. A cached catalog
// stays valid only while this is unchanged, so configuration AO reads during
// discovery must be represented here or an edit would never take effect.
func CatalogFingerprint(ctx context.Context, agentID, binary, workingDir string, env map[string]string) string {
	binaryVersion := BinaryVersion(ctx, binary)
	config := discoveryConfigInputs(agentID, workingDir, env)
	if config == "" {
		// Keep the executable-only fingerprint byte-identical to what earlier
		// daemons wrote, so upgrading does not invalidate every cached catalog.
		return binaryVersion
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(binaryVersion))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(config))
	return fmt.Sprintf("%x", hash.Sum(nil)[:8])
}

// discoveryConfigInputs returns the configuration an agent's discovery consults,
// or "" when the catalog depends on the binary alone.
func discoveryConfigInputs(agentID, workingDir string, env map[string]string) string {
	if agentID != "claude-code" {
		return ""
	}
	return "model=" + claudeCodeResolvedModel(workingDir, env)
}

func catalog(agentID, source string, allowCustom bool, at time.Time, models ...ports.AgentModelInfo) ports.AgentModelCatalog {
	return ports.AgentModelCatalog{
		AgentID:       agentID,
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        models,
		AllowCustom:   allowCustom,
		Source:        source,
		FetchedAt:     at,
	}
}

func model(id, label string, isDefault bool) ports.AgentModelInfo {
	return ports.AgentModelInfo{ID: id, Label: label, IsDefault: isDefault}
}

func parseIDLines(output []byte) ([]ports.AgentModelInfo, error) {
	text := ansiPattern.ReplaceAllString(string(output), "")
	var models []ports.AgentModelInfo
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "•*-✓>│├└ "))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 1 || !looksLikeModelID(fields[0]) {
			continue
		}
		id := strings.Trim(fields[0], "`\"'[](),:")
		models = append(models, ports.AgentModelInfo{ID: id, Label: id})
	}
	return normalize(models), nil
}

func parseGrokModels(output []byte) ([]ports.AgentModelInfo, error) {
	return parseSectionModels(string(output), "Available models:", "")
}

func parseCursorModels(output []byte) ([]ports.AgentModelInfo, error) {
	return parseSectionModels(string(output), "Available models", "Tip:")
}

func parseSectionModels(output, startMarker, stopMarker string) ([]ports.AgentModelInfo, error) {
	output = ansiPattern.ReplaceAllString(output, "")
	inModels := false
	var models []ports.AgentModelInfo
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if !inModels {
			if line == startMarker {
				inModels = true
			}
			continue
		}
		if stopMarker != "" && strings.HasPrefix(line, stopMarker) {
			break
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "•*-✓>│├└ "))
		fields := strings.Fields(line)
		if len(fields) == 0 || !looksLikeModelID(fields[0]) {
			continue
		}
		id := strings.Trim(fields[0], "`\"'[](),:")
		label := id
		if before, after, ok := strings.Cut(line, " - "); ok && strings.TrimSpace(before) == id {
			label = strings.TrimSpace(after)
			if suffix, _, found := strings.Cut(label, " ("); found {
				label = strings.TrimSpace(suffix)
			}
		}
		models = append(models, ports.AgentModelInfo{
			ID:        id,
			Label:     label,
			IsDefault: strings.Contains(strings.ToLower(line), "(default)"),
		})
	}
	return normalize(models), nil
}

func parsePiModels(output []byte) ([]ports.AgentModelInfo, error) {
	output = []byte(ansiPattern.ReplaceAllString(string(output), ""))
	var models []ports.AgentModelInfo
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.EqualFold(fields[0], "provider") {
			continue
		}
		provider := strings.TrimSpace(fields[0])
		modelID := strings.TrimSpace(fields[1])
		if !looksLikeModelID(provider) || !looksLikeModelID(modelID) {
			continue
		}
		id := provider + "/" + modelID
		models = append(models, ports.AgentModelInfo{ID: id, Label: modelID, Provider: provider})
	}
	return normalize(models), nil
}

func looksLikeModelID(value string) bool {
	value = strings.Trim(value, "`\"'[](),:")
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("./:_-@+", r):
		default:
			return false
		}
	}
	switch strings.ToLower(value) {
	case "model", "models", "provider":
		return false
	default:
		return true
	}
}

func parseJSONModels(output []byte) ([]ports.AgentModelInfo, error) {
	var root any
	if err := json.Unmarshal(output, &root); err != nil {
		return nil, err
	}
	var models []ports.AgentModelInfo
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, item := range node {
				walk(item)
			}
		case map[string]any:
			id := firstString(node, "modelId", "model_id", "model_uid", "slug", "model")
			if id == "" {
				if _, isProviderContainer := node["models"]; !isProviderContainer {
					id = firstString(node, "id")
				}
			}
			if id != "" {
				label := firstString(node, "displayName", "display_name", "model_name", "label", "name")
				if label == "" {
					label = id
				}
				models = append(models, ports.AgentModelInfo{
					ID:        id,
					Label:     label,
					Provider:  firstString(node, "provider", "providerId", "provider_id"),
					IsDefault: firstBool(node, "isDefault", "is_default", "default"),
				})
			}
			for key, child := range node {
				if key == "models" {
					if modelMap, ok := child.(map[string]any); ok {
						for alias, item := range modelMap {
							if modelNode, ok := item.(map[string]any); ok && strings.TrimSpace(alias) != "" && looksLikeModelAliasRecord(modelNode) {
								label := firstString(modelNode, "displayName", "display_name", "model_name", "label", "name")
								if label == "" {
									label = alias
								}
								models = append(models, ports.AgentModelInfo{
									ID:        strings.TrimSpace(alias),
									Label:     label,
									Provider:  firstString(modelNode, "provider", "providerId", "provider_id"),
									IsDefault: firstBool(modelNode, "isDefault", "is_default", "default"),
								})
								continue
							}
							walk(item)
						}
						continue
					}
				}
				walk(child)
			}
		}
	}
	walk(root)
	return normalize(models), nil
}

func looksLikeModelAliasRecord(node map[string]any) bool {
	if _, isModelContainer := node["models"]; isModelContainer {
		return false
	}
	return firstString(node,
		"modelId", "model_id", "model_uid", "slug", "model",
		"provider", "providerId", "provider_id",
	) != ""
}

func firstString(node map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := node[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstBool(node map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := node[key].(bool); ok {
			return value
		}
	}
	return false
}

func normalize(models []ports.AgentModelInfo) []ports.AgentModelInfo {
	byID := make(map[string]ports.AgentModelInfo, len(models))
	for _, item := range models {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		if strings.TrimSpace(item.Label) == "" {
			item.Label = item.ID
		}
		if previous, ok := byID[item.ID]; ok {
			if previous.Label == previous.ID && item.Label != item.ID {
				previous.Label = item.Label
			}
			if previous.Provider == "" {
				previous.Provider = item.Provider
			}
			previous.IsDefault = previous.IsDefault || item.IsDefault
			byID[item.ID] = previous
			continue
		}
		byID[item.ID] = item
	}
	out := make([]ports.AgentModelInfo, 0, len(byID))
	for _, item := range byID {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}
