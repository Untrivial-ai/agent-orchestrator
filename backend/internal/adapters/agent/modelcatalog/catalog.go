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
	"aider":       {args: []string{"--no-check-update", "--no-git", "--no-gitignore", "--no-analytics", "--list-models", "."}, parser: parseIDLines},
	"opencode":    {args: []string{"--pure", "models"}, parser: parseIDLines},
	"grok":        {args: []string{"models"}, parser: parseGrokModels},
	"cursor":      {args: []string{"models"}, parser: parseCursorModels},
	"agy":         {args: []string{"models"}, parser: parseAgyModels},
	"kilocode":    {args: []string{"models"}, parser: parseIDLines},
	"pi":          {args: []string{"--list-models"}, parser: parsePiModels},
	"kimchi":      {args: []string{"--list-models"}, parser: parsePiModels},
	"prime-agent": {args: []string{"model", "list"}, parser: parsePiModels},
	"kimi":        {args: []string{"provider", "list", "--json"}, parser: parseJSONModels},
	"auggie":      {args: []string{"models", "list", "--json"}, parser: parseJSONModels},
	"devin":       {args: []string{"models", "list", "--format", "json"}, parser: parseJSONModels},
	"kiro":        {args: []string{"chat", "--list-models", "--format", "json"}, parser: parseJSONModels},
	"omp":         {args: []string{"models", "--json"}, parser: parseJSONModels},
	"copilot":     {args: []string{"help", "config"}, parser: parseCopilotConfigModels},
	"droid":       {args: []string{"exec", "--help"}, parser: parseDroidHelpModels},
	"crush":       {args: []string{"models"}, parser: parseIDLines},
}

// Base returns the picker behavior AO can provide without executing a CLI.
func Base(agentID string) ports.AgentModelCatalog {
	now := time.Now().UTC()
	entryMode := customModelEntryMode(agentID)
	switch agentID {
	case "muse":
		return catalog(agentID, "official-catalog", entryMode, now,
			model("muse-spark", "Muse Spark", true),
			model("muse-spark-1.1", "Muse Spark 1.1", false),
			model("muse-spark-1.2", "Muse Spark 1.2", false),
		)
	case "amp":
		c := catalog(agentID, "official-modes", entryMode, now,
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
				AgentID:          agentID,
				SelectionMode:    ports.ModelSelectionCatalog,
				Models:           []ports.AgentModelInfo{},
				CustomModelEntry: entryMode,
				AllowCustom:      entryMode == ports.CustomModelEntryDirect,
				Source:           "cli",
				FetchedAt:        now,
			}
		}
		return Manual(agentID)
	}
}

// Manual returns the capability-aware fallback used when an adapter has no
// reliable catalog or discovery fails before AO has a successful cache.
func Manual(agentID string) ports.AgentModelCatalog {
	entryMode := customModelEntryMode(agentID)
	selectionMode := ports.ModelSelectionCatalog
	if entryMode == ports.CustomModelEntryDirect {
		selectionMode = ports.ModelSelectionText
	}
	return ports.AgentModelCatalog{
		AgentID:          agentID,
		SelectionMode:    selectionMode,
		Models:           []ports.AgentModelInfo{},
		CustomModelEntry: entryMode,
		AllowCustom:      entryMode == ports.CustomModelEntryDirect,
		Source:           "manual",
		FetchedAt:        time.Now().UTC(),
	}
}

// customModelEntryMode is stable adapter capability policy. Model names and
// availability remain agent-owned and are never listed here.
func customModelEntryMode(agentID string) ports.CustomModelEntryMode {
	switch agentID {
	case "claude-code", "codex", "opencode", "grok", "cursor", "qwen",
		"kimi", "muse", "aider", "goose", "autohand":
		return ports.CustomModelEntryDirect
	case "continue", "cline", "kilocode", "vibe", "pi", "kimchi", "prime-agent":
		return ports.CustomModelEntryConfigured
	default:
		return ports.CustomModelEntryNone
	}
}

// Discoverer implements the model-discovery port for production daemon wiring.
type Discoverer struct {
	CodexModels  CodexModelListFunc
	ClineOptions ClineConfigOptionListFunc
}

// CodexModelListFunc obtains Codex's account-scoped app-server catalog without
// opening a provider thread.
type CodexModelListFunc func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.ChatModel, error)

// ClineConfigOptionListFunc obtains Cline's provider-owned model choices from
// the ACP configuration catalog advertised by session/new.
type ClineConfigOptionListFunc func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error)

// Discover uses the agent-owned model surface configured for this adapter.
func (d Discoverer) Discover(ctx context.Context, request ports.AgentModelDiscoveryRequest) (ports.AgentModelCatalog, error) {
	if request.AgentID == "claude-code" {
		return discoverClaudeCatalog(request), nil
	}
	if request.AgentID == "muse" {
		return Base(request.AgentID), nil
	}
	if request.AgentID == "codex" {
		return discoverCodexCatalog(ctx, request, d.CodexModels)
	}
	if request.AgentID == "cline" && d.ClineOptions != nil {
		if catalog, err := discoverClineCatalog(ctx, request, d.ClineOptions); err == nil {
			return catalog, nil
		}
		// Older Cline releases may not expose ACP config options. Fall back to
		// the configured provider selections already stored by Cline.
	}
	return Discover(ctx, request.AgentID, request.Binary, request.WorkingDir, request.Env)
}

// claudeCodeModels is the static Claude Code model catalog. It mirrors the
// model choices a Claude Code ACP session advertises through session/new, so AO
// can render the picker without spawning the Agent SDK. Claude Code owns the
// real list; refresh this snapshot when the advertised models change.
func claudeCodeModels() []ports.AgentModelInfo {
	return []ports.AgentModelInfo{
		{ID: "sonnet", Label: "Sonnet"},
		{ID: "fable", Label: "Fable 5.1"},
		{ID: "opus", Label: "Opus"},
		{ID: "haiku", Label: "Haiku"},
		{ID: "opus[1m]", Label: "Opus (1M context)"},
	}
}

// discoverClaudeCatalog returns the static Claude Code catalog, marking the row
// matching the project/user configured model as default. The list is static, so
// discovery never fails and never launches the Agent SDK or an interactive
// Claude client.
func discoverClaudeCatalog(request ports.AgentModelDiscoveryRequest) ports.AgentModelCatalog {
	base := Base(request.AgentID)
	base.Models = applyClaudeConfiguredDefault(normalize(claudeCodeModels()), request.WorkingDir, request.Env)
	base.Source = "catalog"
	base.FetchedAt = time.Now().UTC()
	return base
}

func applyClaudeConfiguredDefault(models []ports.AgentModelInfo, workingDir string, env map[string]string) []ports.AgentModelInfo {
	configured := claudeCodeResolvedModel(workingDir, env)
	if configured == "" {
		return models
	}
	matched := false
	for i := range models {
		models[i].IsDefault = strings.EqualFold(models[i].ID, configured)
		matched = matched || models[i].IsDefault
	}
	if !matched {
		// Claude accepts custom aliases and pinned snapshots beyond the static
		// picker snapshot. Keep the effective configured model visible.
		models = append(models, ports.AgentModelInfo{ID: configured, Label: configured, IsDefault: true})
	}
	return models
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
		return discoverClaudeCatalog(ports.AgentModelDiscoveryRequest{AgentID: agentID, WorkingDir: workingDir, Env: env}), nil
	}
	if agentID == "muse" {
		return base, nil
	}
	if agentID == "codex" {
		return base, errors.New("codex model discovery requires app-server")
	}
	if hasConfigDiscoverySource(agentID) {
		return discoverConfigCatalog(agentID, workingDir, env)
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

func discoverCodexCatalog(ctx context.Context, request ports.AgentModelDiscoveryRequest, list CodexModelListFunc) (ports.AgentModelCatalog, error) {
	base := Base(request.AgentID)
	if list == nil {
		return base, errors.New("codex app-server model discovery is unavailable")
	}
	models, err := list(ctx, request)
	if err != nil {
		return base, fmt.Errorf("codex model discovery: %w", err)
	}
	normalized := make([]ports.AgentModelInfo, 0, len(models))
	for _, item := range models {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		label := strings.TrimSpace(item.DisplayName)
		if label == "" {
			label = id
		}
		provider := ""
		if prefix, _, ok := strings.Cut(id, "/"); ok {
			provider = prefix
		}
		normalized = append(normalized, ports.AgentModelInfo{ID: id, Label: label, Provider: provider, IsDefault: item.Default})
	}
	if len(normalized) == 0 {
		return base, errors.New("codex model discovery returned no models")
	}
	base.Models = normalize(normalized)
	base.Source = "cli"
	base.FetchedAt = time.Now().UTC()
	return base, nil
}

func discoverClineCatalog(
	ctx context.Context,
	request ports.AgentModelDiscoveryRequest,
	list ClineConfigOptionListFunc,
) (ports.AgentModelCatalog, error) {
	base := Base(request.AgentID)
	options, err := list(ctx, request)
	if err != nil {
		return base, fmt.Errorf("cline ACP model discovery: %w", err)
	}
	var models []ports.AgentModelInfo
	for _, option := range options {
		if option.Type != ports.ChatConfigOptionSelect ||
			(option.Category != "model" && option.ID != "model") {
			continue
		}
		for _, choice := range option.Choices {
			id := strings.TrimSpace(choice.Value)
			if id == "" {
				continue
			}
			label := strings.TrimSpace(choice.Name)
			if label == "" {
				label = id
			}
			provider := strings.TrimSpace(choice.Group)
			if provider == "" {
				if prefix, _, ok := strings.Cut(id, "/"); ok {
					provider = prefix
				}
			}
			models = append(models, ports.AgentModelInfo{
				ID: id, Label: label, Provider: provider,
				IsDefault: id == strings.TrimSpace(option.Current.Select),
			})
		}
	}
	models = normalize(models)
	if len(models) == 0 {
		return base, errors.New("cline ACP model discovery returned no models")
	}
	base.Models = models
	base.Source = "acp"
	base.FetchedAt = time.Now().UTC()
	return base, nil
}

// claudeCodeSettingsReadLimit bounds how much of a settings file AO parses. The
// documented files are small; a pathological one must not stall discovery.
const claudeCodeSettingsReadLimit = 1 << 20

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
	switch agentID {
	case "claude-code", "codex":
		return true
	}
	if hasConfigDiscoverySource(agentID) {
		return true
	}
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
	if agentID == "claude-code" {
		return "model=" + claudeCodeResolvedModel(workingDir, env)
	}
	if config := configDiscoveryFingerprint(agentID, workingDir, env); config != "" {
		return "config=" + config
	}
	return ""
}

func catalog(agentID, source string, entryMode ports.CustomModelEntryMode, at time.Time, models ...ports.AgentModelInfo) ports.AgentModelCatalog {
	return ports.AgentModelCatalog{
		AgentID:          agentID,
		SelectionMode:    ports.ModelSelectionCatalog,
		Models:           models,
		CustomModelEntry: entryMode,
		AllowCustom:      entryMode == ports.CustomModelEntryDirect,
		Source:           source,
		FetchedAt:        at,
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

func parseAgyModels(output []byte) ([]ports.AgentModelInfo, error) {
	text := ansiPattern.ReplaceAllString(string(output), "")
	var models []ports.AgentModelInfo
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		id := strings.Trim(fields[0], "`\"'[](),:")
		if !looksLikeModelID(id) || !strings.ContainsAny(id, "-./:_") {
			continue
		}
		label := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if label == "" {
			label = id
		}
		models = append(models, ports.AgentModelInfo{ID: id, Label: label})
	}
	return normalize(models), nil
}

func parseGrokModels(output []byte) ([]ports.AgentModelInfo, error) {
	return parseSectionModels(string(output), "Available models:", "")
}

func parseCursorModels(output []byte) ([]ports.AgentModelInfo, error) {
	return parseSectionModels(string(output), "Available models", "Tip:")
}

func parseCopilotConfigModels(output []byte) ([]ports.AgentModelInfo, error) {
	text := ansiPattern.ReplaceAllString(string(output), "")
	inModels := false
	var models []ports.AgentModelInfo
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if !inModels {
			if strings.HasPrefix(line, "`model`:") {
				inModels = true
			}
			continue
		}
		if strings.HasPrefix(line, "`contextTier`:") {
			break
		}
		if !strings.HasPrefix(line, "-") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		id = strings.Trim(id, "`\"'")
		if looksLikeModelID(id) {
			models = append(models, ports.AgentModelInfo{ID: id, Label: id})
		}
	}
	return normalize(models), nil
}

func parseDroidHelpModels(output []byte) ([]ports.AgentModelInfo, error) {
	text := ansiPattern.ReplaceAllString(string(output), "")
	inModels := false
	var models []ports.AgentModelInfo
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if !inModels {
			if line == "Available Models:" {
				inModels = true
			}
			continue
		}
		if line == "" {
			if len(models) > 0 {
				break
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !looksLikeModelID(fields[0]) {
			break
		}
		id := fields[0]
		label := strings.TrimSpace(strings.TrimPrefix(line, id))
		isDefault := strings.HasSuffix(strings.ToLower(label), "(default)")
		if isDefault {
			label = strings.TrimSpace(label[:len(label)-len("(default)")])
		}
		models = append(models, ports.AgentModelInfo{ID: id, Label: label, IsDefault: isDefault})
	}
	return normalize(models), nil
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
			id := firstString(node, "selector", "modelId", "model_id", "model_uid", "slug", "model")
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
