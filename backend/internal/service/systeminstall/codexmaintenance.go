package systeminstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CodexOwnershipKind classifies which package manager (if any) owns the
// currently resolved Codex CLI executable. AO must know this before it can
// offer a correct "Update now" action: the same npm/brew commands used for a
// first install are wrong (or dangerous) to run blindly against a binary that
// turns out to be owned by something else.
type CodexOwnershipKind string

// The fixed set of ownership classifications. "standalone" covers Codex's own
// official installer and any other placement (cargo, a manually placed
// binary, ...) that isn't provably npm- or Homebrew-owned; those installs
// rely on Codex's own `codex update`, when the live binary proves it supports
// that subcommand. "unknown" is reserved for the small number of cases where
// AO cannot even resolve a Codex binary to inspect.
const (
	CodexOwnershipNPM        CodexOwnershipKind = "npm"
	CodexOwnershipHomebrew   CodexOwnershipKind = "homebrew"
	CodexOwnershipStandalone CodexOwnershipKind = "standalone"
	CodexOwnershipUnknown    CodexOwnershipKind = "unknown"
)

// codexVersionCacheTTL bounds how often AO re-queries a package registry
// (npm view / brew info) for the latest published Codex version. These are
// network-backed reads; six hours matches the trust window AO already uses
// for the Codex model catalog (see internal/adapters/agent/modelcatalog).
const codexVersionCacheTTL = 6 * time.Hour

// codexMaintenanceStatusCacheTTL bounds how often AO re-derives the whole
// maintenance status (including the live "does this binary support `codex
// update`" probe) for display. Short enough that a Settings visit sees fresh
// state, long enough that repeated renders don't repeatedly shell out.
const codexMaintenanceStatusCacheTTL = 60 * time.Second

// codexVersionProbeTimeout bounds every individual subprocess this file runs
// (version probes, `codex --help`, npm/brew lookups). All are advisory: a
// timeout degrades the advisory, it never blocks Codex launch or chat.
const codexVersionProbeTimeout = 5 * time.Second

// ErrCodexOwnershipChanged is returned when the installation AO resolved
// immediately before running the update command no longer matches the
// ownership the caller last saw advertised. The caller must refresh status
// and retry rather than have AO guess which installation to mutate.
var ErrCodexOwnershipChanged = errors.New("systeminstall: codex installation ownership changed since the advisory was issued")

// CodexMaintenanceStatus is the display-safe advisory surfaced on the Codex
// provider card: what AO resolved, how it believes that installation is
// owned, and whether an update looks available and actionable.
type CodexMaintenanceStatus struct {
	BinaryPath       string             `json:"binaryPath,omitempty" description:"The Codex executable AO currently launches sessions with."`
	Ownership        CodexOwnershipKind `json:"ownership" enum:"npm,homebrew,standalone,unknown" description:"How AO believes the resolved binary is owned."`
	InstalledVersion string             `json:"installedVersion,omitempty" description:"Version reported by the resolved binary's --version probe."`
	LatestVersion    string             `json:"latestVersion,omitempty" description:"Latest version AO could confirm from the owning package manager. Empty when unknown, not when current."`
	UpdateAvailable  bool               `json:"updateAvailable" description:"True only when both versions were resolved and the latest is strictly newer."`
	UpdateSupported  bool               `json:"updateSupported" description:"True when AO can run an update command for this installation without guessing."`
	UpdateCommand    string             `json:"updateCommand,omitempty" description:"Human-readable command Update now would run."`
	ManualReason     string             `json:"manualReason,omitempty" description:"Set when UpdateSupported is false: why, and what to do manually."`
	CheckedAt        time.Time          `json:"checkedAt" description:"When this status was derived."`
}

// codexUpdatePlan is the internal (non-serialized) resolution result used to
// both render CodexMaintenanceStatus and to execute StartCodexUpdate. Both
// call sites share this resolver so the command a user sees advertised is
// exactly the command AO runs.
type codexUpdatePlan struct {
	binaryPath   string
	ownership    CodexOwnershipKind
	argv         []string
	supported    bool
	manualReason string
}

// codexMaintenanceCache holds the daemon's single, mutex-guarded snapshot of
// Codex maintenance status. Latest-version lookups are cached separately
// (longer-lived, see codexVersionCache) so a cheap re-derivation doesn't
// necessarily re-hit the network.
type codexMaintenanceCache struct {
	mu      sync.Mutex
	status  CodexMaintenanceStatus
	fetched bool
}

// codexVersionCache remembers the last successfully resolved "latest
// published version" per package-manager scope, so Settings being open
// doesn't repeatedly shell out to npm/brew.
type codexVersionCache struct {
	mu      sync.Mutex
	entries map[CodexOwnershipKind]codexVersionCacheEntry
}

type codexVersionCacheEntry struct {
	version   string
	err       error
	fetchedAt time.Time
}

// resolveCodexBinary is overridden in tests; production wiring defers to the
// same resolver the Codex adapter itself uses, so the update flow always
// targets the exact executable AO would launch.
var resolveCodexBinaryDefault = codex.ResolveCodexBinary

// CodexMaintenanceStatus derives (and caches) the installer-aware update
// advisory shown on the Codex provider card. It never blocks agent launch or
// chat: callers invoke it only from the Settings/provider-card poll, and
// every subprocess it runs is bounded by codexVersionProbeTimeout.
func (s *Service) CodexMaintenanceStatus(ctx context.Context) (CodexMaintenanceStatus, error) {
	if err := ctx.Err(); err != nil {
		return CodexMaintenanceStatus{}, err
	}
	s.codexMaintenance.mu.Lock()
	if s.codexMaintenance.fetched && time.Since(s.codexMaintenance.status.CheckedAt) < codexMaintenanceStatusCacheTTL {
		status := s.codexMaintenance.status
		s.codexMaintenance.mu.Unlock()
		return status, nil
	}
	s.codexMaintenance.mu.Unlock()

	status, err := s.deriveCodexMaintenanceStatus(ctx)
	if err != nil {
		return CodexMaintenanceStatus{}, err
	}

	s.codexMaintenance.mu.Lock()
	s.codexMaintenance.status = status
	s.codexMaintenance.fetched = true
	s.codexMaintenance.mu.Unlock()
	return status, nil
}

func (s *Service) deriveCodexMaintenanceStatus(ctx context.Context) (CodexMaintenanceStatus, error) {
	now := time.Now().UTC()
	plan, err := s.resolveCodexUpdatePlan(ctx)
	if err != nil {
		return CodexMaintenanceStatus{
			Ownership: CodexOwnershipUnknown, ManualReason: err.Error(), CheckedAt: now,
		}, nil //nolint:nilerr // a resolution failure is advisory content, not a caller error.
	}

	status := CodexMaintenanceStatus{
		BinaryPath: plan.binaryPath, Ownership: plan.ownership,
		UpdateSupported: plan.supported, UpdateCommand: displayArgv(plan.argv),
		ManualReason: plan.manualReason, CheckedAt: now,
	}

	if plan.binaryPath != "" {
		if version, err := s.codexInstalledVersion(ctx, plan.binaryPath); err == nil {
			status.InstalledVersion = version
		}
	}
	if latest, err := s.codexLatestVersion(ctx, plan.ownership); err == nil && latest != "" {
		status.LatestVersion = latest
		installed, installedOK := parseToolVersion(status.InstalledVersion)
		latestParsed, latestOK := parseToolVersion(latest)
		if installedOK && latestOK {
			status.UpdateAvailable = versionAtLeast(latestParsed, installed) && latestParsed != installed
		}
	}
	return status, nil
}

// resolveCodexUpdatePlan performs the full, uncached ownership resolution:
// resolve the binary AO actually uses, classify who owns it, and (only for a
// standalone placement, where AO cannot infer support from a package
// manager) probe whether that exact binary understands `codex update`.
//
// This is deliberately re-run, uncached, immediately before StartCodexUpdate
// executes anything: an advisory shown a minute ago is not proof the same
// ownership still holds.
func (s *Service) resolveCodexUpdatePlan(ctx context.Context) (codexUpdatePlan, error) {
	s.mu.Lock()
	resolveBinary := s.resolveCodexBinary
	s.mu.Unlock()
	if resolveBinary == nil {
		resolveBinary = resolveCodexBinaryDefault
	}
	binaryPath, err := resolveBinary(ctx)
	if err != nil {
		return codexUpdatePlan{ownership: CodexOwnershipUnknown}, fmt.Errorf("resolve installed codex binary: %w", err)
	}

	planner, err := s.newRequestPlanner(ctx)
	if err != nil {
		return codexUpdatePlan{}, fmt.Errorf("inspect package-manager capabilities: %w", err)
	}

	ownership := classifyCodexOwnership(s.goos, binaryPath, planner.capabilities)
	switch ownership {
	case CodexOwnershipNPM:
		return s.planCodexNPMUpdate(binaryPath, planner)
	case CodexOwnershipHomebrew:
		return s.planCodexHomebrewUpdate(binaryPath, planner)
	default:
		return s.planCodexStandaloneUpdate(ctx, binaryPath)
	}
}

// classifyCodexOwnership is a pure function over already-probed capabilities
// so it stays unit-testable without a real npm/Homebrew install: given the
// resolved binary path and one capability snapshot, which package manager (if
// any) provably owns it. Anything that isn't provably npm- or Homebrew-owned
// is "standalone" — the official installer, a source build, a hand-placed
// binary, or any layout AO doesn't otherwise recognize. AO never guesses a
// *different* package manager than the one that owns the path.
func classifyCodexOwnership(goos, binaryPath string, capabilities *ports.InstallCapabilities) CodexOwnershipKind {
	if binaryPath == "" {
		return CodexOwnershipUnknown
	}
	real := binaryPath
	if resolved, err := filepathEvalSymlinks(binaryPath); err == nil && resolved != "" {
		real = resolved
	}
	if capabilities != nil {
		if npm := capabilities.NPM; npm.Err == nil && npm.GlobalPrefix != "" {
			prefixBin := npm.GlobalPrefix
			if goos != "windows" {
				prefixBin = filepath.Join(npm.GlobalPrefix, "bin")
			}
			if withinDir(real, prefixBin) {
				return CodexOwnershipNPM
			}
		}
		if brew := capabilities.Homebrew; brew.Err == nil && brew.Prefix != "" {
			if withinDir(real, brew.Prefix) && (strings.Contains(real, "/Cellar/") || strings.Contains(real, "/Caskroom/") || brew.Casks["codex"] || brew.Formulae["codex"]) {
				return CodexOwnershipHomebrew
			}
		}
	}
	return CodexOwnershipStandalone
}

func withinDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepathRel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "")
}

// The following three indirections exist purely so codexmaintenance_test.go
// can exercise classifyCodexOwnership deterministically across OSes without
// depending on the real filesystem for symlink resolution.
var (
	filepathEvalSymlinks = filepath.EvalSymlinks
	filepathRel          = filepath.Rel
)

func (s *Service) planCodexNPMUpdate(binaryPath string, planner requestPlanner) (codexUpdatePlan, error) {
	npm := planner.capabilities.NPM
	if npm.Err != nil || npm.GlobalPrefix == "" {
		return codexUpdatePlan{binaryPath: binaryPath, ownership: CodexOwnershipNPM, manualReason: "npm's global install state could not be inspected. Run `npm install -g @openai/codex@latest` manually."}, nil
	}
	if !npm.PrefixWritable {
		return codexUpdatePlan{binaryPath: binaryPath, ownership: CodexOwnershipNPM, manualReason: fmt.Sprintf("npm's global prefix %s is not writable by the current user. AO will not use sudo; update manually.", npm.GlobalPrefix)}, nil
	}
	return codexUpdatePlan{
		binaryPath: binaryPath, ownership: CodexOwnershipNPM,
		argv: []string{"npm", "install", "-g", "@openai/codex@latest"}, supported: true,
	}, nil
}

func (s *Service) planCodexHomebrewUpdate(binaryPath string, planner requestPlanner) (codexUpdatePlan, error) {
	brew := planner.capabilities.Homebrew
	if brew.Err != nil || brew.Prefix == "" {
		return codexUpdatePlan{binaryPath: binaryPath, ownership: CodexOwnershipHomebrew, manualReason: "Homebrew's installation state could not be inspected. Run `brew upgrade --cask codex` manually."}, nil
	}
	if !brew.PrefixWritable {
		return codexUpdatePlan{binaryPath: binaryPath, ownership: CodexOwnershipHomebrew, manualReason: fmt.Sprintf("Homebrew's prefix %s is not writable by the current user. AO will not use sudo; update manually.", brew.Prefix)}, nil
	}
	return codexUpdatePlan{
		binaryPath: binaryPath, ownership: CodexOwnershipHomebrew,
		argv: []string{"brew", "upgrade", "--cask", "codex"}, supported: true,
	}, nil
}

// planCodexStandaloneUpdate is the only branch that must run a live probe:
// nothing about PATH or package-manager capabilities tells AO whether this
// particular Codex build understands `codex update`, and older CLIs may not.
// The probe only inspects --help output; it never mutates anything.
func (s *Service) planCodexStandaloneUpdate(ctx context.Context, binaryPath string) (codexUpdatePlan, error) {
	if s.commands == nil {
		return codexUpdatePlan{binaryPath: binaryPath, ownership: CodexOwnershipStandalone, manualReason: "AO could not verify this installation's update support. See https://github.com/openai/codex for manual update instructions."}, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, codexVersionProbeTimeout)
	defer cancel()
	out := &capturedOutput{max: maxOutputBytes}
	err := s.commands.Run(probeCtx, []string{binaryPath, "--help"}, out, out)
	if err != nil || !strings.Contains(out.String(), "update") {
		return codexUpdatePlan{
			binaryPath: binaryPath, ownership: CodexOwnershipStandalone,
			manualReason: "AO could not confirm this Codex installation supports `codex update`. See https://github.com/openai/codex for manual update instructions.",
		}, nil
	}
	return codexUpdatePlan{
		binaryPath: binaryPath, ownership: CodexOwnershipStandalone,
		argv: []string{binaryPath, "update"}, supported: true,
	}, nil
}

func (s *Service) codexInstalledVersion(ctx context.Context, binaryPath string) (string, error) {
	if s.commands == nil {
		return "", errors.New("systeminstall: command runner is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, codexVersionProbeTimeout)
	defer cancel()
	out := &capturedOutput{max: maxOutputBytes}
	if err := s.commands.Run(probeCtx, []string{binaryPath, "--version"}, out, out); err != nil {
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	return extractVersionToken(out.String()), nil
}

// extractVersionToken pulls the last dotted-numeric token out of free-form
// `--version` output, e.g. "codex-cli 0.153.4" -> "0.153.4".
func extractVersionToken(output string) string {
	fields := strings.Fields(output)
	for i := len(fields) - 1; i >= 0; i-- {
		if _, ok := parseToolVersion(fields[i]); ok {
			return fields[i]
		}
	}
	return strings.TrimSpace(output)
}

// codexLatestVersion resolves the latest version the owning package manager
// publishes, cached for codexVersionCacheTTL. Standalone and unknown
// ownership never had a package-manager query to make; a Codex-native
// `codex update` is idempotent when already current, so AO surfaces "Update
// now" for those without gating it on a version diff it cannot compute.
func (s *Service) codexLatestVersion(ctx context.Context, ownership CodexOwnershipKind) (string, error) {
	if ownership != CodexOwnershipNPM && ownership != CodexOwnershipHomebrew {
		return "", nil
	}
	if s.commands == nil {
		return "", errors.New("systeminstall: command runner is not configured")
	}

	s.codexVersions.mu.Lock()
	if entry, ok := s.codexVersions.entries[ownership]; ok && time.Since(entry.fetchedAt) < codexVersionCacheTTL {
		s.codexVersions.mu.Unlock()
		return entry.version, entry.err
	}
	s.codexVersions.mu.Unlock()

	var version string
	var err error
	switch ownership {
	case CodexOwnershipNPM:
		version, err = s.npmLatestCodexVersion(ctx)
	case CodexOwnershipHomebrew:
		version, err = s.homebrewLatestCodexVersion(ctx)
	}

	s.codexVersions.mu.Lock()
	if s.codexVersions.entries == nil {
		s.codexVersions.entries = make(map[CodexOwnershipKind]codexVersionCacheEntry)
	}
	s.codexVersions.entries[ownership] = codexVersionCacheEntry{version: version, err: err, fetchedAt: time.Now()}
	s.codexVersions.mu.Unlock()
	return version, err
}

func (s *Service) npmLatestCodexVersion(ctx context.Context) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, codexVersionProbeTimeout)
	defer cancel()
	out := &capturedOutput{max: maxOutputBytes}
	if err := s.commands.Run(probeCtx, []string{"npm", "view", "@openai/codex", "version"}, out, out); err != nil {
		return "", fmt.Errorf("query npm registry for @openai/codex: %w", err)
	}
	version := strings.TrimSpace(out.String())
	if _, ok := parseToolVersion(version); !ok {
		return "", fmt.Errorf("npm view returned an unparseable version: %q", version)
	}
	return version, nil
}

// homebrewCaskInfo is the minimal shape read from `brew info --json=v2
// codex`; Homebrew's own JSON schema carries far more than this.
type homebrewCaskInfo struct {
	Casks []struct {
		Version string `json:"version"`
	} `json:"casks"`
}

func (s *Service) homebrewLatestCodexVersion(ctx context.Context) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, codexVersionProbeTimeout)
	defer cancel()
	out := &capturedOutput{max: maxOutputBytes}
	if err := s.commands.Run(probeCtx, []string{"brew", "info", "--json=v2", "--cask", "codex"}, out, out); err != nil {
		return "", fmt.Errorf("query homebrew for codex cask: %w", err)
	}
	var info homebrewCaskInfo
	if err := json.Unmarshal([]byte(out.String()), &info); err != nil {
		return "", fmt.Errorf("parse brew info output: %w", err)
	}
	if len(info.Casks) == 0 || info.Casks[0].Version == "" {
		return "", errors.New("brew info returned no codex cask version")
	}
	return info.Casks[0].Version, nil
}

// StartCodexUpdate runs the installer-aware Codex "Update now" action.
// expectedOwnership, when non-empty, must match the ownership AO freshly
// resolves right now — not the cached value from an earlier advisory. A
// mismatch (a second installation appeared, PATH changed, ...) fails closed
// with ErrCodexOwnershipChanged rather than risk mutating a different
// installation than the one the user was shown.
//
// Execution and job tracking are shared with every other harness install via
// startAgentPlan, so a Codex update: serializes against any other install or
// reinstall job already running for TargetCodex (same jobs-map key),
// persists through AgentInstallJobStore for Settings to recover after a
// remount, and on success (see daemon wiring of SetOnSucceeded) invalidates
// and rechecks Codex readiness and its cached model catalog automatically.
func (s *Service) StartCodexUpdate(ctx context.Context, expectedOwnership CodexOwnershipKind) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	plan, err := s.resolveCodexUpdatePlan(ctx)
	if err != nil {
		return Job{}, err
	}
	if expectedOwnership != "" && plan.ownership != expectedOwnership {
		return Job{}, fmt.Errorf("%w: now %s, advisory said %s", ErrCodexOwnershipChanged, plan.ownership, expectedOwnership)
	}

	jobPlan := Plan{
		Target: TargetCodex, Method: string(plan.ownership),
		Command: plan.argv, ExpectedDestination: plan.binaryPath, DocsURL: agentDocumentationURLs[TargetCodex],
	}
	if !plan.supported {
		jobPlan.Unsupported = true
		jobPlan.Reason = plan.manualReason
	}
	return s.startAgentPlan(ctx, TargetCodex, jobPlan, nil)
}

func displayArgv(argv []string) string {
	return strings.Join(argv, " ")
}
