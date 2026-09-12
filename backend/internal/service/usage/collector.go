package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

const (
	maxUsageMetadataBytes          = 256
	maxUsagePathBytes              = 4096
	maxCodexSessionMeta            = 1 << 20
	maxCodexChildIDs               = 4096
	defaultCodexLogicalSourceLimit = 4096
	defaultDiscoveryLimit          = 64
)

var nativeUsageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ErrUsageSessionNotFound reports that hook metadata targeted no durable AO session.
var ErrUsageSessionNotFound = errors.New("usage session not found")

type validatedSourceArtifact struct {
	path     string
	identity string
	size     int64
}

// HookSignal is the usage-specific metadata carried by an AO agent hook.
type HookSignal struct {
	Harness                domain.AgentHarness
	ProviderHint           string
	Event                  string
	LaunchID               string
	NativeSessionID        string
	ModelID                string
	TranscriptPath         string
	SubagentID             string
	SubagentTranscriptPath string
}

// SourceRoots are the provider-owned directories from which AO may read usage
// transcripts.
type SourceRoots struct {
	ClaudeProjects string
	CodexSessions  string
	CodexArchived  string
	KimiHome       string
}

// DefaultSourceRoots resolves provider-owned transcript directories. dataDir
// is AO's already-resolved durable data directory.
func DefaultSourceRoots(ctx context.Context, dataDir string) (SourceRoots, error) {
	if err := ctx.Err(); err != nil {
		return SourceRoots{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return SourceRoots{}, fmt.Errorf("resolve home directory: %w", err)
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if strings.TrimSpace(dataDir) == "" {
		dataDir = filepath.Join(home, ".ao", "data")
	}
	return SourceRoots{
		ClaudeProjects: filepath.Join(home, ".claude", "projects"),
		CodexSessions:  filepath.Join(codexHome, "sessions"),
		CodexArchived:  filepath.Join(codexHome, "archived_sessions"),
		KimiHome:       filepath.Join(dataDir, "kimi"),
	}, nil
}

type collectorStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
	UpsertUsageBinding(context.Context, domain.UsageBindingRecord) (domain.UsageBindingRecord, error)
	GetUsageBinding(context.Context, domain.SessionID, domain.AgentHarness, string) (domain.UsageBindingRecord, bool, error)
	ListUsageBindingsForSession(context.Context, domain.SessionID) ([]domain.UsageBindingRecord, error)
	FinalizeUsageBindingsForSessionLaunch(context.Context, domain.SessionID, string, time.Time, time.Time) ([]domain.UsageBindingRecord, error)
	ListUsageDiscoveryBindings(context.Context, int64) ([]domain.UsageBindingRecord, error)
	ListUsageBindingsForCodexParent(context.Context, string) ([]domain.UsageBindingRecord, error)
	ListLatestRetiredCodexReplacementClaimsByPath(context.Context, string) ([]domain.UsageSourceRecord, error)
	UpdateUsageBindingState(context.Context, int64, domain.UsageBindingState, string, time.Time) (bool, error)
	UpdateUsageBindingErrorCode(context.Context, int64, string, time.Time) (bool, error)
	CompleteUsageBindingIfSettled(context.Context, int64, time.Time) (bool, error)
	InsertUsageSource(context.Context, domain.UsageSourceRecord) (domain.UsageSourceRecord, error)
	ReplaceUsageSource(context.Context, int64, string, domain.UsageSourceRecord, time.Time) (domain.UsageSourceRecord, error)
	ListUsageSourcesForBinding(context.Context, int64) ([]domain.UsageSourceRecord, error)
	ReactivateUsageSource(context.Context, int64, time.Time) (bool, error)
}

// Collector registers provider transcript files and coordinates their source
// lifecycle. Parsing and cursor advancement remain in the usage ingestor.
type Collector struct {
	store                   collectorStore
	roots                   SourceRoots
	notifySourcesChanged    func(reconcile bool)
	notifyRouteResolved     func()
	codexLogicalSourceLimit int
	now                     func() time.Time
	mu                      sync.Mutex
	// Guarded separately from mu, which RecordHook holds across the whole hook.
	routeMu sync.RWMutex
}

// OnRouteResolved registers the handler called the first time a binding learns
// its billing route. Only Claude needs it: a Claude transcript names no
// provider, so events collected before the first hook are unattributed, and
// this is the moment the evidence to repair them finally exists.
func (c *Collector) OnRouteResolved(handler func()) {
	if c == nil {
		return
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.notifyRouteResolved = handler
}

func (c *Collector) routeResolvedHandler() func() {
	c.routeMu.RLock()
	defer c.routeMu.RUnlock()
	return c.notifyRouteResolved
}

// NewCollector constructs a transcript source registrar.
func NewCollector(store collectorStore, roots SourceRoots, notifySourcesChanged func(reconcile bool)) *Collector {
	return newCollectorWithCodexSourceLimit(store, roots, notifySourcesChanged, defaultCodexLogicalSourceLimit)
}

func newCollectorWithCodexSourceLimit(
	store collectorStore,
	roots SourceRoots,
	notifySourcesChanged func(reconcile bool),
	limit int,
) *Collector {
	if limit <= 0 {
		limit = defaultCodexLogicalSourceLimit
	}
	return &Collector{
		store:                   store,
		roots:                   roots,
		notifySourcesChanged:    notifySourcesChanged,
		codexLogicalSourceLimit: limit,
		now:                     time.Now,
	}
}

// FinalizeSession moves every known native binding for one observed runtime
// generation and session revision into finalization, then asks the ingestion
// pipeline to collect a stable final cursor. It is idempotent and safe to call
// before the session is terminated.
func (c *Collector) FinalizeSession(
	ctx context.Context,
	sessionID domain.SessionID,
	expectedRuntimeLaunchID string,
	expectedSessionRevision time.Time,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now().UTC()
	bindings, err := c.store.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sessionID,
		boundedUsageMetadata(expectedRuntimeLaunchID),
		expectedSessionRevision,
		now,
	)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if _, err := c.reactivateLatestSources(ctx, binding.ID, now); err != nil {
			return err
		}
	}
	if len(bindings) > 0 {
		c.notifySourceInventory(false)
	}
	return nil
}

// ReactivateSession resumes collection for the native session relaunched by AO.
// Existing cursors and events remain untouched; the source inventory is merely
// made watchable again so hooks are not required for continued accounting.
func (c *Collector) ReactivateSession(
	ctx context.Context,
	sessionID domain.SessionID,
	expectedRuntimeLaunchID string,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	session, ok, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUsageSessionNotFound, sessionID)
	}
	if session.IsTerminated || session.Metadata.RuntimeLaunchID != expectedRuntimeLaunchID ||
		!SupportedHarness(session.Harness) {
		return nil
	}

	bindings, err := c.store.ListUsageBindingsForSession(ctx, sessionID)
	if err != nil {
		return err
	}
	nativeID := usageNativeSessionID(session)
	var current *domain.UsageBindingRecord
	for index := len(bindings) - 1; index >= 0; index-- {
		binding := &bindings[index]
		if binding.Harness != session.Harness || nativeID != "" && binding.NativeRootID != nativeID {
			continue
		}
		current = binding
		break
	}
	if current != nil {
		if _, err := c.reactivateBinding(ctx, *current, c.now().UTC()); err != nil {
			return err
		}
	} else if nativeID != "" && nativeUsageIDPattern.MatchString(nativeID) {
		if err := c.backfillSession(ctx, session, nativeID); err != nil {
			return err
		}
	} else {
		return nil
	}
	c.notifySourceInventory(true)
	return nil
}

// RecordHook registers transcript metadata and updates collection lifecycle for
// one native hook callback.
func (c *Collector) RecordHook(ctx context.Context, sessionID domain.SessionID, signal HookSignal) error {
	signal.LaunchID = boundedUsageMetadata(signal.LaunchID)
	finalizing := finalizingEvent(signal.Event)
	session, proceed, err := c.hookSession(ctx, sessionID, signal, finalizing)
	if err != nil || !proceed {
		return err
	}
	signal.Harness = session.Harness
	signal.ProviderHint = trustedClaudeProviderHint(session.Harness, signal.ProviderHint)
	signal.NativeSessionID = boundedUsageMetadata(signal.NativeSessionID)
	if signal.NativeSessionID == "" {
		signal.NativeSessionID = usageNativeSessionID(session)
	}
	if signal.NativeSessionID == "" {
		signal.NativeSessionID = nativeIDFromTranscript(signal.TranscriptPath)
	}

	mainPath := strings.TrimSpace(signal.TranscriptPath)
	existsForDiscovery := false
	if finalizing && signal.NativeSessionID != "" && session.Harness == domain.HarnessClaudeCode {
		_, existsForDiscovery, err = c.store.GetUsageBinding(ctx, sessionID, session.Harness, signal.NativeSessionID)
		if err != nil {
			return err
		}
	}
	if signal.NativeSessionID != "" && mainPath == "" &&
		(session.Harness == domain.HarnessCodex || session.Harness == domain.HarnessKimi ||
			finalizing && !existsForDiscovery) {
		mainPath, err = c.discoverPath(ctx, session.Harness, signal.NativeSessionID)
		if err != nil {
			return err
		}
	}
	subagentPath := strings.TrimSpace(signal.SubagentTranscriptPath)
	mainArtifact, err := c.validateHookArtifact(ctx, session.Harness, mainPath)
	if err != nil {
		return err
	}
	subagentArtifact, err := c.validateHookArtifact(ctx, session.Harness, subagentPath)
	if err != nil {
		return err
	}
	if finalizing && !existsForDiscovery {
		recoveryPath := mainPath
		if recoveryPath == "" && session.Harness == domain.HarnessClaudeCode {
			recoveryPath = subagentPath
		}
		if recoveryPath == "" {
			return nil
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	session, proceed, err = c.hookSession(ctx, sessionID, signal, finalizing)
	if err != nil || !proceed {
		return err
	}
	now := c.now().UTC()
	if finalizing {
		if err := c.finalizeSession(ctx, sessionID, now); err != nil {
			return err
		}
	}
	if signal.NativeSessionID == "" {
		c.notifySourceInventory(!finalizing)
		return nil
	}
	existing, exists, err := c.store.GetUsageBinding(ctx, sessionID, session.Harness, signal.NativeSessionID)
	if err != nil {
		return err
	}
	sessionLive := !session.IsTerminated && session.Activity.State != domain.ActivityExited
	reactivating := sessionLive && !finalizing && (signal.Event == "session-start" ||
		exists && existing.State == domain.UsageBindingFinalizing)
	state := domain.UsageBindingActive
	if exists {
		state = existing.State
	}
	switch {
	case finalizing:
		state = domain.UsageBindingFinalizing
	case reactivating:
		state = domain.UsageBindingActive
	case exists && (state == domain.UsageBindingComplete || state == domain.UsageBindingPartial) &&
		signal.Event == "subagent-stop":
		state = domain.UsageBindingFinalizing
	}
	inventoryChanged := !exists || state != existing.State
	needsReconcile := false
	lastErrorCode := ""
	if session.Harness == domain.HarnessCodex && strings.TrimSpace(signal.TranscriptPath) == "" {
		lastErrorCode = domain.UsageErrorSourceDiscoveryPending
	}
	// A binding that existed without a billable route, and now has one, owns
	// history whose inferred or unavailable price can be corrected immediately.
	routeResolved := exists && signal.ProviderHint != "" &&
		(existing.ProviderHint == "" || existing.ProviderHint == pricing.UnidentifiedBillingRoute) &&
		existing.ProviderHint != signal.ProviderHint
	binding, err := c.store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:      sessionID,
		Harness:        session.Harness,
		NativeRootID:   signal.NativeSessionID,
		InitialModelID: boundedUsageMetadata(signal.ModelID),
		ProviderHint:   signal.ProviderHint,
		State:          state,
		LastErrorCode:  lastErrorCode,
		UpdatedAt:      now,
	})
	if err != nil {
		return err
	}
	if routeResolved {
		if notify := c.routeResolvedHandler(); notify != nil {
			notify()
		}
	}

	if mainArtifact != nil {
		kind, ok := sourceKindForHarness(session.Harness)
		if !ok {
			return fmt.Errorf("usage: unsupported harness %q", session.Harness)
		}
		changed, err := c.registerHookSource(
			ctx,
			binding,
			kind,
			signal.NativeSessionID,
			"",
			*mainArtifact,
			now,
			signal.Event == "session-start" || finalizing,
		)
		if err != nil {
			c.notifySourceInventory(true)
			return err
		}
		inventoryChanged = inventoryChanged || changed
		if session.Harness == domain.HarnessKimi {
			if err := c.registerDiscoveredKimiAgents(ctx, binding, mainArtifact.path, now, false); err != nil {
				return err
			}
		}
		sourceErrorCode := ""
		if c.codexDiscoveryStillPending(ctx, signal.Event, signal.TranscriptPath, mainPath) {
			sourceErrorCode = domain.UsageErrorSourceDiscoveryPending
		}
		if _, err := c.store.UpdateUsageBindingErrorCode(ctx, binding.ID, sourceErrorCode, now); err != nil {
			return err
		}
	} else {
		needsReconcile = true
	}
	if subagentArtifact != nil && session.Harness == domain.HarnessClaudeCode {
		changed, err := c.registerHookSource(
			ctx,
			binding,
			domain.UsageSourceClaudeSubagent,
			signal.NativeSessionID,
			boundedUsageMetadata(signal.SubagentID),
			*subagentArtifact,
			now,
			finalizing || signal.Event == "subagent-stop",
		)
		if err != nil {
			c.notifySourceInventory(true)
			return err
		}
		inventoryChanged = inventoryChanged || changed
	}
	if reactivating {
		changed, err := c.reactivateBinding(ctx, binding, now)
		if err != nil {
			return err
		}
		inventoryChanged = inventoryChanged || changed
		if session.Harness == domain.HarnessCodex {
			needsReconcile = true
		}
		if c.codexDiscoveryStillPending(ctx, signal.Event, signal.TranscriptPath, mainPath) {
			if _, err := c.store.UpdateUsageBindingState(
				ctx,
				binding.ID,
				domain.UsageBindingActive,
				domain.UsageErrorSourceDiscoveryPending,
				now,
			); err != nil {
				return err
			}
		}
	} else if state == domain.UsageBindingFinalizing {
		if err := c.settleFinalizingBinding(ctx, binding.ID, now); err != nil {
			return err
		}
	}
	switch {
	case needsReconcile:
		c.notifySourceInventory(true)
	case inventoryChanged:
		c.notifySourceInventory(false)
	}
	return nil
}

func trustedClaudeProviderHint(harness domain.AgentHarness, raw string) string {
	if harness != domain.HarnessClaudeCode {
		return ""
	}
	return pricing.TrustedClaudeRoute(boundedUsageMetadata(raw))
}

func finalizingEvent(event string) bool {
	return event == "session-end" || event == "process-exited"
}

func (c *Collector) hookSession(
	ctx context.Context,
	sessionID domain.SessionID,
	signal HookSignal,
	finalizing bool,
) (domain.SessionRecord, bool, error) {
	session, ok, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		return domain.SessionRecord{}, false, err
	}
	if !ok {
		return domain.SessionRecord{}, false, fmt.Errorf("%w: %s", ErrUsageSessionNotFound, sessionID)
	}
	if !SupportedHarness(session.Harness) {
		return session, false, nil
	}
	if signal.LaunchID != "" && session.Metadata.RuntimeLaunchID != "" &&
		signal.LaunchID != session.Metadata.RuntimeLaunchID {
		return session, false, nil
	}
	if signal.Harness != "" && signal.Harness != session.Harness {
		return domain.SessionRecord{}, false, fmt.Errorf(
			"usage hook harness %s does not match session harness %s",
			signal.Harness,
			session.Harness,
		)
	}
	sessionLive := !session.IsTerminated && session.Activity.State != domain.ActivityExited
	if session.IsTerminated || (!finalizing && !sessionLive) {
		return session, false, nil
	}
	return session, true, nil
}

func (c *Collector) validateHookArtifact(
	ctx context.Context,
	harness domain.AgentHarness,
	path string,
) (*validatedSourceArtifact, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	resolved, identity, size, err := c.validateSourcePath(ctx, harness, path)
	if err != nil {
		return nil, err
	}
	return &validatedSourceArtifact{path: resolved, identity: identity, size: size}, nil
}

// BackfillActive discovers transcript files only for live/resumable AO
// sessions. It deliberately does not import terminated session history.
func (c *Collector) BackfillActive(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	sessions, err := c.store.ListAllSessions(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, session := range sessions {
		if session.IsTerminated || !SupportedHarness(session.Harness) {
			continue
		}
		nativeID := usageNativeSessionID(session)
		if nativeID == "" || !nativeUsageIDPattern.MatchString(nativeID) {
			continue
		}
		if err := c.backfillSession(ctx, session, nativeID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// usageNativeSessionID returns the provider transcript identity for the
// session's committed interface. Chat controllers persist that identity as the
// provider conversation id because they have no terminal hook to populate the
// agent session id. TUI controllers populate the agent session id through their
// native hook pipeline. Prefer the Chat field only when it is present so older
// migrated records can still fall back to their hook-derived identity.
func usageNativeSessionID(session domain.SessionRecord) string {
	nativeID := session.Metadata.AgentSessionID
	if domain.NormalizeSessionMode(session.Mode) == domain.SessionModeChat &&
		strings.TrimSpace(session.Metadata.ProviderConversationID) != "" {
		nativeID = session.Metadata.ProviderConversationID
	}
	return boundedUsageMetadata(nativeID)
}

func (c *Collector) backfillSession(ctx context.Context, session domain.SessionRecord, nativeID string) error {
	now := c.now().UTC()
	existing, exists, err := c.store.GetUsageBinding(ctx, session.ID, session.Harness, nativeID)
	if err != nil {
		return err
	}
	path, err := c.discoverPath(ctx, session.Harness, nativeID)
	if err != nil {
		return err
	}
	if exists && session.Activity.State == domain.ActivityExited &&
		(existing.State == domain.UsageBindingComplete || existing.State == domain.UsageBindingPartial) {
		return nil
	}
	if exists && session.Activity.State != domain.ActivityExited {
		budgetLimited := existing.LastErrorCode == domain.UsageErrorCodexSourceBudgetExceeded
		if _, err := c.reactivateBinding(ctx, existing, now); err != nil {
			return err
		}
		existing.State = domain.UsageBindingActive
		existing.LastErrorCode = ""
		if budgetLimited {
			existing.State = domain.UsageBindingPartial
			existing.LastErrorCode = domain.UsageErrorCodexSourceBudgetExceeded
		}
	}

	state := existing.State
	if !exists {
		state = domain.UsageBindingActive
		switch {
		case session.Activity.State == domain.ActivityExited:
			state = domain.UsageBindingFinalizing
		case path == "":
			state = domain.UsageBindingDiscovering
		}
	} else if session.Activity.State == domain.ActivityExited &&
		(state == domain.UsageBindingDiscovering || state == domain.UsageBindingActive) {
		state = domain.UsageBindingFinalizing
	} else if state == domain.UsageBindingDiscovering && path != "" {
		state = domain.UsageBindingActive
	}
	lastErrorCode := ""
	if path == "" {
		lastErrorCode = domain.UsageErrorSourceDiscoveryPending
	}
	binding, err := c.store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:      session.ID,
		Harness:        session.Harness,
		NativeRootID:   nativeID,
		InitialModelID: existing.InitialModelID,
		ProviderHint:   existing.ProviderHint,
		State:          state,
		LastErrorCode:  lastErrorCode,
		UpdatedAt:      now,
	})
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}

	kind, ok := sourceKindForHarness(session.Harness)
	if !ok {
		return fmt.Errorf("usage: unsupported harness %q", session.Harness)
	}
	if _, err := c.registerSource(ctx, binding, kind, nativeID, "", path, now, false); err != nil {
		return err
	}
	switch session.Harness {
	case domain.HarnessClaudeCode:
		if err := c.registerDiscoveredClaudeSubagents(ctx, binding, path, now, false); err != nil {
			return err
		}
	case domain.HarnessCodex:
		if err := c.registerDiscoveredCodexChildren(ctx, binding, now); err != nil {
			return err
		}
	case domain.HarnessKimi:
		if err := c.registerDiscoveredKimiAgents(ctx, binding, path, now, false); err != nil {
			return err
		}
	}
	if state == domain.UsageBindingFinalizing {
		return c.settleFinalizingBinding(ctx, binding.ID, now)
	}
	return nil
}

// ReconcileSources discovers provider artifacts that hooks could not name,
// appeared after their hook, or moved between provider-owned directories.
// A negative limit reconciles every eligible binding; zero uses the bounded
// default.
func (c *Collector) ReconcileSources(ctx context.Context, limit int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if limit == 0 {
		limit = defaultDiscoveryLimit
	}
	bindings, err := c.store.ListUsageDiscoveryBindings(ctx, limit)
	if err != nil {
		return err
	}
	now := c.now().UTC()
	var errs []error
	for _, binding := range bindings {
		if err := c.reconcileBinding(ctx, binding, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ReconcilePath uses Codex rollout metadata to validate replacements and reach
// newly-created child bindings independently of the bounded discovery queue.
func (c *Collector) ReconcilePath(ctx context.Context, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	resolved, _, _, err := c.validateSourcePath(ctx, domain.HarnessCodex, path)
	if err != nil {
		return nil
	}
	meta, ok := readCodexSessionMeta(resolved)
	if !ok || len(meta.NativeSessionID) > maxUsageMetadataBytes ||
		!nativeUsageIDPattern.MatchString(meta.NativeSessionID) {
		return nil
	}
	claims, err := c.store.ListLatestRetiredCodexReplacementClaimsByPath(ctx, resolved)
	if err != nil {
		return err
	}
	now := c.now().UTC()
	if len(claims) > 0 {
		return c.reconcileRetiredCodexClaims(ctx, resolved, meta, claims, now)
	}
	if meta.ParentThreadID == "" || !validCanonicalUUID(meta.NativeSessionID) ||
		len(meta.ParentThreadID) > maxUsageMetadataBytes ||
		!nativeUsageIDPattern.MatchString(meta.ParentThreadID) {
		return nil
	}
	bindings, err := c.store.ListUsageBindingsForCodexParent(ctx, meta.ParentThreadID)
	if err != nil {
		return err
	}
	var errs []error
	for _, binding := range bindings {
		sources, listErr := c.store.ListUsageSourcesForBinding(ctx, binding.ID)
		if listErr != nil {
			errs = append(errs, listErr)
			continue
		}
		if !latestCodexSourceDiscovers(sources, meta.ParentThreadID, meta.NativeSessionID) {
			continue
		}
		if _, registerErr := c.registerSourceWithExpectedParent(
			ctx,
			binding,
			domain.UsageSourceCodexRollout,
			meta.NativeSessionID,
			meta.NativeSessionID,
			resolved,
			now,
			false,
			meta.ParentThreadID,
		); registerErr != nil {
			errs = append(errs, registerErr)
		}
	}
	return errors.Join(errs...)
}

func (c *Collector) reconcileRetiredCodexClaims(
	ctx context.Context,
	path string,
	meta codexSessionMeta,
	claims []domain.UsageSourceRecord,
	now time.Time,
) error {
	var errs []error
	for _, claim := range claims {
		if claim.SubagentID == "" {
			if meta.NativeSessionID != claim.NativeSessionID || meta.ParentThreadID != "" {
				continue
			}
			bindings, err := c.store.ListUsageBindingsForCodexParent(ctx, claim.NativeSessionID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, binding := range bindings {
				if binding.ID != claim.BindingID || binding.NativeRootID != claim.NativeSessionID {
					continue
				}
				if _, err := c.registerSourceWithExpectedParent(
					ctx,
					binding,
					domain.UsageSourceCodexRollout,
					claim.NativeSessionID,
					"",
					path,
					now,
					false,
					"",
				); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}

		storedNativeID, directParentID, ok := codexParserAttribution(claim.ParserStateJSON)
		if !ok || storedNativeID != claim.NativeSessionID || meta.NativeSessionID != claim.NativeSessionID ||
			claim.SubagentID != claim.NativeSessionID ||
			meta.ParentThreadID != directParentID ||
			!validCanonicalUUID(meta.NativeSessionID) ||
			len(directParentID) > maxUsageMetadataBytes ||
			!nativeUsageIDPattern.MatchString(directParentID) {
			continue
		}
		bindings, err := c.store.ListUsageBindingsForCodexParent(ctx, directParentID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, binding := range bindings {
			if binding.ID != claim.BindingID {
				continue
			}
			sources, err := c.store.ListUsageSourcesForBinding(ctx, binding.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !latestCodexSourceDiscovers(sources, directParentID, meta.NativeSessionID) {
				continue
			}
			if _, err := c.registerSourceWithExpectedParent(
				ctx,
				binding,
				domain.UsageSourceCodexRollout,
				claim.NativeSessionID,
				claim.SubagentID,
				path,
				now,
				false,
				directParentID,
			); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func codexParserAttribution(raw string) (string, string, bool) {
	var state struct {
		Version    int                    `json:"version"`
		SourceKind domain.UsageSourceKind `json:"source_kind"`
		Codex      *struct {
			NativeSessionID string `json:"native_session_id"`
			DirectParentID  string `json:"direct_parent_id"`
		} `json:"codex"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil || state.Version != 1 ||
		state.SourceKind != domain.UsageSourceCodexRollout || state.Codex == nil ||
		!nativeUsageIDPattern.MatchString(state.Codex.NativeSessionID) ||
		(state.Codex.DirectParentID != "" && !validCanonicalUUID(state.Codex.DirectParentID)) {
		return "", "", false
	}
	return state.Codex.NativeSessionID, state.Codex.DirectParentID, true
}

func (c *Collector) reconcileBinding(ctx context.Context, binding domain.UsageBindingRecord, now time.Time) error {
	session, ok, err := c.store.GetSession(ctx, binding.SessionID)
	if err != nil || !ok {
		return err
	}
	if session.IsTerminated && binding.State != domain.UsageBindingFinalizing {
		return nil
	}

	targetState := binding.State
	if session.Activity.State == domain.ActivityExited &&
		(binding.State == domain.UsageBindingDiscovering ||
			binding.State == domain.UsageBindingActive ||
			(binding.State == domain.UsageBindingPartial &&
				binding.LastErrorCode == domain.UsageErrorCodexSourceBudgetExceeded)) {
		targetState = domain.UsageBindingFinalizing
	}

	path, err := c.discoverPath(ctx, binding.Harness, binding.NativeRootID)
	if err != nil {
		return err
	}
	if path == "" {
		if binding.Harness == domain.HarnessCodex {
			if childErr := c.registerDiscoveredCodexChildren(ctx, binding, now); childErr != nil {
				return childErr
			}
		}
		targetState, lastErrorCode, stateErr := c.preserveCodexBudgetState(
			ctx,
			binding,
			targetState,
			domain.UsageErrorSourceDiscoveryPending,
		)
		if stateErr != nil {
			return stateErr
		}
		_, err = c.store.UpdateUsageBindingState(ctx, binding.ID, targetState, lastErrorCode, now)
		return err
	}
	if targetState == domain.UsageBindingDiscovering {
		targetState = domain.UsageBindingActive
	}

	kind, ok := sourceKindForHarness(binding.Harness)
	if !ok {
		return fmt.Errorf("usage: unsupported harness %q", binding.Harness)
	}
	if _, err := c.registerSource(ctx, binding, kind, binding.NativeRootID, "", path, now, false); err != nil {
		return err
	}
	switch binding.Harness {
	case domain.HarnessClaudeCode:
		if err := c.registerDiscoveredClaudeSubagents(ctx, binding, path, now, false); err != nil {
			return err
		}
	case domain.HarnessCodex:
		if err := c.registerDiscoveredCodexChildren(ctx, binding, now); err != nil {
			return err
		}
	case domain.HarnessKimi:
		if err := c.registerDiscoveredKimiAgents(ctx, binding, path, now, false); err != nil {
			return err
		}
	}
	lastErrorCode := ""
	if binding.Harness == domain.HarnessCodex &&
		binding.LastErrorCode == domain.UsageErrorSourceDiscoveryPending &&
		targetState == domain.UsageBindingActive &&
		!pathWithinRoot(ctx, path, c.roots.CodexSessions) {
		lastErrorCode = domain.UsageErrorSourceDiscoveryPending
	}
	targetState, lastErrorCode, err = c.preserveCodexBudgetState(
		ctx,
		binding,
		targetState,
		lastErrorCode,
	)
	if err != nil {
		return err
	}
	if _, err := c.store.UpdateUsageBindingState(ctx, binding.ID, targetState, lastErrorCode, now); err != nil {
		return err
	}
	if targetState == domain.UsageBindingFinalizing {
		return c.settleFinalizingBinding(ctx, binding.ID, now)
	}
	return nil
}

func (c *Collector) preserveCodexBudgetState(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	state domain.UsageBindingState,
	errorCode string,
) (domain.UsageBindingState, string, error) {
	if binding.Harness != domain.HarnessCodex {
		return state, errorCode, nil
	}
	current, ok, err := c.store.GetUsageBinding(
		ctx,
		binding.SessionID,
		binding.Harness,
		binding.NativeRootID,
	)
	if err != nil || !ok {
		return state, errorCode, err
	}
	if current.LastErrorCode == domain.UsageErrorCodexSourceBudgetExceeded {
		if state == domain.UsageBindingFinalizing {
			return state, current.LastErrorCode, nil
		}
		return current.State, current.LastErrorCode, nil
	}
	return state, errorCode, nil
}

type usageSourceIdentityKey struct {
	kind            domain.UsageSourceKind
	nativeSessionID string
	fileIdentity    string
}

type bindingSourceInventory struct {
	sources             []*domain.UsageSourceRecord
	byID                map[int64]*domain.UsageSourceRecord
	latestByPath        map[string]*domain.UsageSourceRecord
	latestCodexByNative map[string]*domain.UsageSourceRecord
	byIdentity          map[usageSourceIdentityKey][]*domain.UsageSourceRecord
	codexLogicalIDs     map[string]struct{}
	maxGeneration       int64
}

func newBindingSourceInventory(sources []domain.UsageSourceRecord) *bindingSourceInventory {
	inventory := &bindingSourceInventory{}
	for _, source := range sources {
		inventory.sources = append(inventory.sources, &domain.UsageSourceRecord{})
		*inventory.sources[len(inventory.sources)-1] = source
	}
	inventory.rebuildIndexes()
	return inventory
}

func (i *bindingSourceInventory) rebuildIndexes() {
	i.byID = make(map[int64]*domain.UsageSourceRecord, len(i.sources))
	i.latestByPath = make(map[string]*domain.UsageSourceRecord, len(i.sources))
	i.latestCodexByNative = make(map[string]*domain.UsageSourceRecord, len(i.sources))
	i.byIdentity = make(map[usageSourceIdentityKey][]*domain.UsageSourceRecord, len(i.sources))
	i.codexLogicalIDs = make(map[string]struct{}, len(i.sources))
	i.maxGeneration = 0
	for _, source := range i.sources {
		i.indexSource(source)
	}
}

func (i *bindingSourceInventory) indexSource(source *domain.UsageSourceRecord) {
	if source.ID != 0 {
		i.byID[source.ID] = source
	}
	if source.Generation >= i.maxGeneration {
		i.maxGeneration = source.Generation
	}
	latest := i.latestByPath[source.ArtifactPath]
	if latest == nil || source.Generation > latest.Generation {
		i.latestByPath[source.ArtifactPath] = source
	}
	if source.Kind == domain.UsageSourceCodexRollout && source.NativeSessionID != "" {
		i.codexLogicalIDs[source.NativeSessionID] = struct{}{}
		latest = i.latestCodexByNative[source.NativeSessionID]
		if latest == nil || source.Generation > latest.Generation ||
			(source.Generation == latest.Generation && source.ID > latest.ID) {
			i.latestCodexByNative[source.NativeSessionID] = source
		}
	}
	key := usageSourceIdentityKey{
		kind:            source.Kind,
		nativeSessionID: source.NativeSessionID,
		fileIdentity:    source.FileIdentity,
	}
	i.byIdentity[key] = append(i.byIdentity[key], source)
}

func (i *bindingSourceInventory) add(source domain.UsageSourceRecord) {
	if existing := i.byID[source.ID]; source.ID != 0 && existing != nil {
		*existing = source
		i.rebuildIndexes()
		return
	}
	record := &domain.UsageSourceRecord{}
	*record = source
	i.sources = append(i.sources, record)
	i.indexSource(record)
}

func (i *bindingSourceInventory) identityMatch(
	kind domain.UsageSourceKind,
	nativeSessionID string,
	fileIdentity string,
	size int64,
) *domain.UsageSourceRecord {
	key := usageSourceIdentityKey{
		kind:            kind,
		nativeSessionID: nativeSessionID,
		fileIdentity:    fileIdentity,
	}
	var match *domain.UsageSourceRecord
	for _, source := range i.byIdentity[key] {
		if size >= source.ByteOffset && (match == nil || source.Generation > match.Generation) {
			match = source
		}
	}
	return match
}

func (i *bindingSourceInventory) markState(
	id int64,
	state domain.UsageSourceState,
	errorCode string,
	now time.Time,
) {
	if source := i.byID[id]; source != nil {
		source.State = state
		source.LastErrorCode = errorCode
		source.NextRetryAt = nil
		source.UpdatedAt = now
	}
}

func (i *bindingSourceInventory) reactivate(id int64, now time.Time) {
	if source := i.byID[id]; source != nil {
		source.State = domain.UsageSourceActive
		source.FailureCount = 0
		source.NextRetryAt = nil
		source.LastErrorCode = ""
		source.UpdatedAt = now
	}
}

func (i *bindingSourceInventory) snapshot() []domain.UsageSourceRecord {
	sources := make([]domain.UsageSourceRecord, 0, len(i.sources))
	for _, source := range i.sources {
		sources = append(sources, *source)
	}
	return sources
}

func (c *Collector) registerSource(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	path string,
	now time.Time,
	reactivateExisting bool,
) (bool, error) {
	expectedParentID := ""
	if kind == domain.UsageSourceCodexRollout && subagentID != "" {
		expectedParentID = binding.NativeRootID
	}
	return c.registerSourceWithExpectedParent(
		ctx,
		binding,
		kind,
		nativeSessionID,
		subagentID,
		path,
		now,
		reactivateExisting,
		expectedParentID,
	)
}

func (c *Collector) registerHookSource(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	artifact validatedSourceArtifact,
	now time.Time,
	reactivateExisting bool,
) (bool, error) {
	expectedParentID := ""
	if kind == domain.UsageSourceCodexRollout && subagentID != "" {
		expectedParentID = binding.NativeRootID
	}
	if err := c.validateSourceAttribution(
		binding,
		kind,
		nativeSessionID,
		subagentID,
		artifact.path,
		expectedParentID,
	); err != nil {
		return false, err
	}
	sources, err := c.store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil {
		return false, err
	}
	return c.registerValidatedSource(
		ctx,
		binding,
		kind,
		nativeSessionID,
		subagentID,
		expectedParentID,
		artifact.path,
		artifact.identity,
		artifact.size,
		now,
		reactivateExisting,
		newBindingSourceInventory(sources),
	)
}

func (c *Collector) registerSourceWithExpectedParent(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	path string,
	now time.Time,
	reactivateExisting bool,
	expectedParentID string,
) (bool, error) {
	resolved, identity, size, err := c.validateSourcePath(ctx, binding.Harness, path)
	if err != nil {
		return false, err
	}
	if err := c.validateSourceAttribution(
		binding,
		kind,
		nativeSessionID,
		subagentID,
		resolved,
		expectedParentID,
	); err != nil {
		return false, err
	}
	sources, err := c.store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil {
		return false, err
	}
	return c.registerValidatedSource(
		ctx,
		binding,
		kind,
		nativeSessionID,
		subagentID,
		expectedParentID,
		resolved,
		identity,
		size,
		now,
		reactivateExisting,
		newBindingSourceInventory(sources),
	)
}

func (c *Collector) registerSourceWithInventory(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	path string,
	now time.Time,
	reactivateExisting bool,
	inventory *bindingSourceInventory,
	expectedParentID string,
) (bool, error) {
	resolved, identity, size, err := c.validateSourcePath(ctx, binding.Harness, path)
	if err != nil {
		return false, err
	}
	if err := c.validateSourceAttribution(
		binding,
		kind,
		nativeSessionID,
		subagentID,
		resolved,
		expectedParentID,
	); err != nil {
		return false, err
	}
	return c.registerValidatedSource(
		ctx,
		binding,
		kind,
		nativeSessionID,
		subagentID,
		expectedParentID,
		resolved,
		identity,
		size,
		now,
		reactivateExisting,
		inventory,
	)
}

func (c *Collector) registerValidatedSource(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	directParentID string,
	resolved string,
	identity string,
	size int64,
	now time.Time,
	reactivateExisting bool,
	inventory *bindingSourceInventory,
) (bool, error) {
	if c.codexSourceBudgetExceeded(inventory, kind, nativeSessionID, subagentID) {
		state := binding.State
		if state == domain.UsageBindingDiscovering || state == domain.UsageBindingActive {
			state = domain.UsageBindingPartial
		}
		_, err := c.store.UpdateUsageBindingState(
			ctx,
			binding.ID,
			state,
			domain.UsageErrorCodexSourceBudgetExceeded,
			now,
		)
		return false, err
	}
	latest := inventory.latestByPath[resolved]
	latestNative := inventory.latestCodexByNative[nativeSessionID]
	identityMatch := inventory.identityMatch(kind, nativeSessionID, identity, size)
	generation := inventory.maxGeneration
	if latest != nil && latest.FileIdentity == identity && size >= latest.ByteOffset &&
		latest.LastErrorCode != domain.UsageErrorArtifactReplaced {
		if reactivateExisting || latest.State == domain.UsageSourceError {
			changed, err := c.store.ReactivateUsageSource(ctx, latest.ID, now)
			if err == nil && changed {
				inventory.reactivate(latest.ID, now)
			}
			return changed, err
		}
		return false, nil
	}
	if len(inventory.sources) > 0 {
		generation++
	}
	var replaced *domain.UsageSourceRecord
	switch {
	case latest != nil:
		replaced = latest
	case kind == domain.UsageSourceCodexRollout && latestNative != nil:
		replaced = latestNative
	case identityMatch != nil:
		replaced = identityMatch
	}
	record := domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            kind,
		NativeSessionID: nativeSessionID,
		SubagentID:      subagentID,
		ArtifactPath:    resolved,
		FileIdentity:    identity,
		Generation:      generation,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	}
	if kind == domain.UsageSourceCodexRollout {
		state, err := initialCodexParserState(nativeSessionID, directParentID)
		if err != nil {
			return false, err
		}
		record.ParserStateJSON = state
	}
	if latest == nil && identityMatch != nil {
		resumeState := true
		if kind == domain.UsageSourceCodexRollout {
			storedNativeID, storedParentID, ok := codexParserAttribution(identityMatch.ParserStateJSON)
			resumeState = ok && storedNativeID == nativeSessionID && storedParentID == directParentID
		}
		if resumeState {
			record.ByteOffset = identityMatch.ByteOffset
			record.ParserStateJSON = identityMatch.ParserStateJSON
		}
	}
	var inserted domain.UsageSourceRecord
	var err error
	if replaced != nil {
		inserted, err = c.store.ReplaceUsageSource(ctx, replaced.ID, domain.UsageErrorArtifactReplaced, record, now)
	} else {
		inserted, err = c.store.InsertUsageSource(ctx, record)
	}
	if err != nil {
		return false, err
	}
	if replaced != nil {
		inventory.markState(replaced.ID, domain.UsageSourceComplete, domain.UsageErrorArtifactReplaced, now)
	}
	inventory.add(inserted)
	return true, nil
}

func initialCodexParserState(nativeSessionID, directParentID string) (string, error) {
	state := struct {
		Version    int                    `json:"version"`
		SourceKind domain.UsageSourceKind `json:"source_kind"`
		Integrity  struct{}               `json:"integrity"`
		Codex      struct {
			Baseline            struct{} `json:"baseline"`
			NativeSessionID     string   `json:"native_session_id"`
			DirectParentID      string   `json:"direct_parent_id,omitempty"`
			PendingSpawnCallIDs []string `json:"pending_spawn_call_ids"`
			DiscoveredChildIDs  []string `json:"discovered_child_ids"`
		} `json:"codex"`
	}{
		Version:    1,
		SourceKind: domain.UsageSourceCodexRollout,
	}
	state.Codex.NativeSessionID = nativeSessionID
	state.Codex.DirectParentID = directParentID
	state.Codex.PendingSpawnCallIDs = []string{}
	state.Codex.DiscoveredChildIDs = []string{}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encode Codex parser state: %w", err)
	}
	return string(encoded), nil
}

func (c *Collector) codexSourceBudgetExceeded(
	inventory *bindingSourceInventory,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
) bool {
	if kind != domain.UsageSourceCodexRollout || nativeSessionID == "" || subagentID == "" {
		return false
	}
	if _, exists := inventory.codexLogicalIDs[nativeSessionID]; exists {
		return false
	}
	return len(inventory.codexLogicalIDs) >= c.codexLogicalSourceLimit
}

func (c *Collector) registerDiscoveredClaudeSubagents(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	mainPath string,
	now time.Time,
	reactivateExisting bool,
) error {
	var errs []error
	paths, err := discoverClaudeSubagentPaths(ctx, mainPath)
	if err != nil {
		return err
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		subagentID := strings.TrimPrefix(name, "agent-")
		if _, err := c.registerSource(
			ctx,
			binding,
			domain.UsageSourceClaudeSubagent,
			binding.NativeRootID,
			boundedUsageMetadata(subagentID),
			path,
			now,
			reactivateExisting,
		); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Collector) registerDiscoveredKimiAgents(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	mainPath string,
	now time.Time,
	reactivateExisting bool,
) error {
	sessionDir := filepath.Dir(filepath.Dir(filepath.Dir(mainPath)))
	paths, err := filepath.Glob(filepath.Join(sessionDir, "agents", "*", "wire.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var errs []error
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		agentID := boundedUsageMetadata(filepath.Base(filepath.Dir(path)))
		if agentID == "" || !nativeUsageIDPattern.MatchString(agentID) {
			continue
		}
		subagentID := agentID
		if agentID == "main" {
			subagentID = ""
		}
		if _, err := c.registerSource(
			ctx,
			binding,
			domain.UsageSourceKimiWire,
			binding.NativeRootID,
			subagentID,
			path,
			now,
			reactivateExisting,
		); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func discoverClaudeSubagentPaths(ctx context.Context, mainPath string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(mainPath, filepath.Ext(mainPath))
	pattern := filepath.Join(base, "subagents", "agent-*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	candidates := make([]candidate, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.Before(candidates[j].mod) })
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.path)
	}
	return result, nil
}

func (c *Collector) registerDiscoveredCodexChildren(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	now time.Time,
) error {
	sources, err := c.store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil {
		return err
	}
	return c.registerDiscoveredCodexChildrenWithInventory(
		ctx,
		binding,
		now,
		newBindingSourceInventory(sources),
	)
}

func (c *Collector) registerDiscoveredCodexChildrenWithInventory(
	ctx context.Context,
	binding domain.UsageBindingRecord,
	now time.Time,
	inventory *bindingSourceInventory,
) error {
	sources := latestCodexSourcesByNativeSession(inventory.snapshot())
	seen := make(map[string]struct{})
	var errs []error
	for _, source := range sources {
		if source.Kind != domain.UsageSourceCodexRollout || source.NativeSessionID == "" {
			continue
		}
		for _, childID := range discoveredCodexChildIDs(source.ParserStateJSON) {
			key := source.NativeSessionID + "\x00" + childID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			path, err := c.discoverCodexPath(ctx, childID, source.NativeSessionID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if path == "" {
				continue
			}
			if _, err := c.registerSourceWithInventory(
				ctx,
				binding,
				domain.UsageSourceCodexRollout,
				childID,
				childID,
				path,
				now,
				false,
				inventory,
				source.NativeSessionID,
			); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func latestCodexSourcesByNativeSession(sources []domain.UsageSourceRecord) []domain.UsageSourceRecord {
	latest := make(map[string]domain.UsageSourceRecord)
	for _, source := range sources {
		if source.Kind != domain.UsageSourceCodexRollout || source.NativeSessionID == "" {
			continue
		}
		current, ok := latest[source.NativeSessionID]
		if !ok || source.Generation > current.Generation ||
			(source.Generation == current.Generation && source.ID > current.ID) {
			latest[source.NativeSessionID] = source
		}
	}
	result := make([]domain.UsageSourceRecord, 0, len(latest))
	for _, source := range latest {
		result = append(result, source)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Generation != result[j].Generation {
			return result[i].Generation < result[j].Generation
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func latestCodexSourceDiscovers(sources []domain.UsageSourceRecord, parentID, childID string) bool {
	for _, source := range latestCodexSourcesByNativeSession(sources) {
		if source.NativeSessionID == parentID && slices.Contains(discoveredCodexChildIDs(source.ParserStateJSON), childID) {
			return true
		}
	}
	return false
}

func discoveredCodexChildIDs(raw string) []string {
	var state struct {
		Version    int                    `json:"version"`
		SourceKind domain.UsageSourceKind `json:"source_kind"`
		Codex      *struct {
			DiscoveredChildIDs []string `json:"discovered_child_ids"`
		} `json:"codex"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil || state.Version != 1 ||
		state.SourceKind != domain.UsageSourceCodexRollout || state.Codex == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(state.Codex.DiscoveredChildIDs))
	result := make([]string, 0, len(state.Codex.DiscoveredChildIDs))
	for _, childID := range state.Codex.DiscoveredChildIDs {
		if len(result) >= maxCodexChildIDs {
			break
		}
		parsed, err := uuid.Parse(childID)
		if err != nil || parsed.String() != childID || len(childID) > maxUsageMetadataBytes {
			continue
		}
		if _, ok := seen[childID]; ok {
			continue
		}
		seen[childID] = struct{}{}
		result = append(result, childID)
	}
	return result
}

func (c *Collector) settleFinalizingBinding(ctx context.Context, bindingID int64, now time.Time) error {
	_, err := c.store.CompleteUsageBindingIfSettled(ctx, bindingID, now)
	return err
}

func (c *Collector) reactivateBinding(ctx context.Context, binding domain.UsageBindingRecord, now time.Time) (bool, error) {
	state := domain.UsageBindingActive
	errorCode := ""
	if binding.LastErrorCode == domain.UsageErrorCodexSourceBudgetExceeded {
		errorCode = binding.LastErrorCode
		state = domain.UsageBindingPartial
	}
	if _, err := c.store.UpdateUsageBindingState(ctx, binding.ID, state, errorCode, now); err != nil {
		return false, err
	}
	return c.reactivateLatestSources(ctx, binding.ID, now)
}

func (c *Collector) reactivateLatestSources(ctx context.Context, bindingID int64, now time.Time) (bool, error) {
	sources, err := c.store.ListUsageSourcesForBinding(ctx, bindingID)
	if err != nil {
		return false, err
	}
	latestByPath := make(map[string]domain.UsageSourceRecord)
	for _, source := range sources {
		key := source.ArtifactPath
		if source.Kind == domain.UsageSourceCodexRollout && source.NativeSessionID != "" {
			key = string(source.Kind) + "\x00" + source.NativeSessionID
		}
		latest, ok := latestByPath[key]
		if !ok || source.Generation > latest.Generation ||
			(source.Generation == latest.Generation && source.ID > latest.ID) {
			latestByPath[key] = source
		}
	}
	changed := false
	for _, source := range latestByPath {
		sourceChanged, err := c.store.ReactivateUsageSource(ctx, source.ID, now)
		if err != nil {
			return false, err
		}
		changed = changed || sourceChanged
	}
	return changed, nil
}

func (c *Collector) finalizeSession(ctx context.Context, sessionID domain.SessionID, now time.Time) error {
	bindings, err := c.store.ListUsageBindingsForSession(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		errorCode := ""
		if binding.LastErrorCode == domain.UsageErrorCodexSourceBudgetExceeded {
			errorCode = binding.LastErrorCode
		}
		if _, err := c.store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingFinalizing, errorCode, now); err != nil {
			return err
		}
		if _, err := c.reactivateLatestSources(ctx, binding.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) validateSourcePath(ctx context.Context, harness domain.AgentHarness, path string) (string, string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	if len(path) > maxUsagePathBytes {
		return "", "", 0, errors.New(domain.UsageErrorArtifactPathRejected)
	}
	if !filepath.IsAbs(path) || strings.ToLower(filepath.Ext(path)) != ".jsonl" {
		return "", "", 0, errors.New(domain.UsageErrorArtifactPathRejected)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", "", 0, errors.New(domain.UsageErrorArtifactMissing)
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", 0, errors.New(domain.UsageErrorArtifactMissing)
	}
	roots := c.allowedRoots(harness)
	allowed := false
	for _, root := range roots {
		if root == "" {
			continue
		}
		resolvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
		if rootErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(resolvedRoot, resolved)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", "", 0, errors.New(domain.UsageErrorArtifactPathRejected)
	}
	identity, err := SourceIdentity(ctx, resolved)
	if err != nil {
		return "", "", 0, err
	}
	return resolved, identity, info.Size(), nil
}

func validateSourceAttribution(
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	resolved string,
	expectedParentID string,
) error {
	rejected := func() error { return errors.New(domain.UsageErrorArtifactPathRejected) }
	switch kind {
	case domain.UsageSourceCodexRollout:
		if binding.Harness != domain.HarnessCodex {
			return rejected()
		}
		meta, ok := readCodexSessionMeta(resolved)
		if !ok || meta.NativeSessionID != nativeSessionID {
			return rejected()
		}
		if subagentID == "" {
			if meta.ParentThreadID != "" {
				return rejected()
			}
		} else if subagentID != nativeSessionID || expectedParentID == "" || meta.ParentThreadID != expectedParentID {
			return rejected()
		}
	case domain.UsageSourceClaudeMain:
		if binding.Harness != domain.HarnessClaudeCode || nativeSessionID != binding.NativeRootID ||
			filepath.Base(resolved) != nativeSessionID+".jsonl" {
			return rejected()
		}
	case domain.UsageSourceClaudeSubagent:
		subagentsDir := filepath.Dir(resolved)
		rootSessionDir := filepath.Dir(subagentsDir)
		if binding.Harness != domain.HarnessClaudeCode || nativeSessionID != binding.NativeRootID ||
			subagentID == "" || filepath.Base(subagentsDir) != "subagents" ||
			filepath.Base(rootSessionDir) != binding.NativeRootID ||
			filepath.Base(resolved) != "agent-"+subagentID+".jsonl" {
			return rejected()
		}
	case domain.UsageSourceKimiWire:
		agentDir := filepath.Dir(resolved)
		agentsDir := filepath.Dir(agentDir)
		sessionDir := filepath.Dir(agentsDir)
		agentID := filepath.Base(agentDir)
		wantSubagent := agentID
		if agentID == "main" {
			wantSubagent = ""
		}
		if binding.Harness != domain.HarnessKimi || nativeSessionID != binding.NativeRootID ||
			filepath.Base(resolved) != "wire.jsonl" || filepath.Base(agentsDir) != "agents" ||
			filepath.Base(sessionDir) != binding.NativeRootID || subagentID != wantSubagent {
			return rejected()
		}
	default:
		return rejected()
	}
	return nil
}

func (c *Collector) validateSourceAttribution(
	binding domain.UsageBindingRecord,
	kind domain.UsageSourceKind,
	nativeSessionID string,
	subagentID string,
	resolved string,
	expectedParentID string,
) error {
	if err := validateSourceAttribution(
		binding,
		kind,
		nativeSessionID,
		subagentID,
		resolved,
		expectedParentID,
	); err != nil {
		return err
	}
	if kind != domain.UsageSourceKimiWire {
		return nil
	}
	expectedRoot, err := filepath.EvalSymlinks(filepath.Join(c.roots.KimiHome, "sessions"))
	if err != nil {
		return errors.New(domain.UsageErrorArtifactPathRejected)
	}
	actualSessionDir := filepath.Dir(filepath.Dir(filepath.Dir(resolved)))
	rel, err := filepath.Rel(expectedRoot, actualSessionDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New(domain.UsageErrorArtifactPathRejected)
	}
	return nil
}

func (c *Collector) allowedRoots(harness domain.AgentHarness) []string {
	switch harness {
	case domain.HarnessClaudeCode:
		return []string{c.roots.ClaudeProjects}
	case domain.HarnessCodex:
		return []string{c.roots.CodexSessions, c.roots.CodexArchived}
	case domain.HarnessKimi:
		return []string{c.roots.KimiHome}
	default:
		return nil
	}
}

func sourceKindForHarness(harness domain.AgentHarness) (domain.UsageSourceKind, bool) {
	switch harness {
	case domain.HarnessClaudeCode:
		return domain.UsageSourceClaudeMain, true
	case domain.HarnessCodex:
		return domain.UsageSourceCodexRollout, true
	case domain.HarnessKimi:
		return domain.UsageSourceKimiWire, true
	default:
		return "", false
	}
}

func (c *Collector) codexDiscoveryStillPending(ctx context.Context, event, hookPath, discoveredPath string) bool {
	if strings.TrimSpace(hookPath) != "" {
		return false
	}
	if strings.TrimSpace(discoveredPath) == "" {
		return true
	}
	return event == "session-start" && !pathWithinRoot(ctx, discoveredPath, c.roots.CodexSessions)
}

func pathWithinRoot(ctx context.Context, path, root string) bool {
	if ctx.Err() != nil {
		return false
	}
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c *Collector) discoverPath(ctx context.Context, harness domain.AgentHarness, nativeID string) (string, error) {
	if !nativeUsageIDPattern.MatchString(nativeID) {
		return "", nil
	}
	var patterns []string
	switch harness {
	case domain.HarnessClaudeCode:
		patterns = []string{filepath.Join(c.roots.ClaudeProjects, "*", nativeID+".jsonl")}
	case domain.HarnessCodex:
		return c.discoverCodexPath(ctx, nativeID, "")
	case domain.HarnessKimi:
		return c.discoverKimiPath(ctx, nativeID)
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var matches []candidate
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return "", err
		}
		if len(paths) > 128 {
			paths = paths[:128]
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				matches = append(matches, candidate{path: path, mod: info.ModTime()})
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].mod.After(matches[j].mod) })
	if len(matches) == 0 {
		return "", nil
	}
	return matches[0].path, nil
}

type kimiIndexRecord struct {
	SessionID  string `json:"sessionId"`
	SessionDir string `json:"sessionDir"`
	Deleted    bool   `json:"deleted"`
}

func (c *Collector) discoverKimiPath(ctx context.Context, nativeID string) (string, error) {
	index, err := os.Open(filepath.Join(c.roots.KimiHome, "session_index.jsonl")) //nolint:gosec // configured provider-owned root.
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = index.Close() }()

	var latest kimiIndexRecord
	scanner := bufio.NewScanner(index)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var record kimiIndexRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.SessionID == nativeID {
			latest = record
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if latest.Deleted || latest.SessionID == "" || !filepath.IsAbs(latest.SessionDir) ||
		filepath.Base(filepath.Clean(latest.SessionDir)) != nativeID ||
		!pathWithinRoot(ctx, latest.SessionDir, filepath.Join(c.roots.KimiHome, "sessions")) {
		return "", nil
	}
	mainPath := filepath.Join(latest.SessionDir, "agents", "main", "wire.jsonl")
	if info, err := os.Stat(mainPath); err == nil && info.Mode().IsRegular() {
		return mainPath, nil
	}
	paths, err := filepath.Glob(filepath.Join(latest.SessionDir, "agents", "*", "wire.jsonl"))
	if err != nil || len(paths) == 0 {
		return "", err
	}
	sort.Strings(paths)
	return paths[0], nil
}

func (c *Collector) discoverCodexPath(ctx context.Context, nativeID, parentID string) (string, error) {
	if !nativeUsageIDPattern.MatchString(nativeID) {
		return "", nil
	}
	patterns := []string{
		filepath.Join(c.roots.CodexSessions, "*", "*", "*", "*"+nativeID+"*.jsonl"),
		filepath.Join(c.roots.CodexArchived, "*"+nativeID+"*.jsonl"),
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var matches []candidate
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		paths, _ := filepath.Glob(pattern)
		if len(paths) > 128 {
			paths = paths[:128]
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			resolved, _, _, err := c.validateSourcePath(ctx, domain.HarnessCodex, path)
			if err != nil || !codexSessionMetaMatches(resolved, nativeID, parentID) {
				continue
			}
			if info, err := os.Stat(resolved); err == nil {
				matches = append(matches, candidate{path: resolved, mod: info.ModTime()})
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].mod.After(matches[j].mod) })
	if len(matches) == 0 {
		return "", nil
	}
	return matches[0].path, nil
}

func codexSessionMetaMatches(path, nativeID, parentID string) bool {
	meta, ok := readCodexSessionMeta(path)
	if !ok || meta.NativeSessionID != nativeID {
		return false
	}
	return parentID == "" || meta.ParentThreadID == parentID
}

type codexSessionMeta struct {
	NativeSessionID string
	ParentThreadID  string
}

func readCodexSessionMeta(path string) (codexSessionMeta, bool) {
	file, err := os.Open(path) //nolint:gosec // candidate passed the provider-root boundary.
	if err != nil {
		return codexSessionMeta{}, false
	}
	defer func() { _ = file.Close() }()
	data, err := bufio.NewReaderSize(file, maxCodexSessionMeta+1).ReadSlice('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return codexSessionMeta{}, false
	}
	data = bytes.TrimSuffix(data, []byte{'\n'})
	data = bytes.TrimSuffix(data, []byte{'\r'})
	if len(data) > maxCodexSessionMeta {
		return codexSessionMeta{}, false
	}
	var envelope struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Type != "session_meta" || envelope.Payload.ID == "" {
		return codexSessionMeta{}, false
	}
	parentThreadID, ok := codexParentThreadID(envelope.Payload.Source)
	if !ok {
		return codexSessionMeta{}, false
	}
	return codexSessionMeta{
		NativeSessionID: envelope.Payload.ID,
		ParentThreadID:  parentThreadID,
	}, true
}

func codexParentThreadID(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	if raw[0] == '"' {
		var source string
		return "", json.Unmarshal(raw, &source) == nil
	}
	if raw[0] != '{' {
		return "", false
	}
	var source struct {
		Subagent struct {
			ThreadSpawn struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return "", false
	}
	return source.Subagent.ThreadSpawn.ParentThreadID, true
}

func validCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && len(value) <= maxUsageMetadataBytes
}

func (c *Collector) notifySourceInventory(reconcile bool) {
	if c.notifySourcesChanged != nil {
		c.notifySourcesChanged(reconcile)
	}
}

func nativeIDFromTranscript(path string) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(path)), filepath.Ext(path))
	if nativeUsageIDPattern.MatchString(base) {
		return base
	}
	return ""
}

func boundedUsageMetadata(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxUsageMetadataBytes {
		return ""
	}
	return value
}

// SourceIdentity returns the filesystem's stable file id. Transcript contents
// are append-only and therefore cannot participate in identity without making a
// newly created or partially written first record look like file replacement.
func SourceIdentity(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path) //nolint:gosec // validated provider-owned path.
	if err != nil {
		return "", errors.New(domain.UsageErrorSourceReadFailed)
	}
	defer func() { _ = file.Close() }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return SourceIdentityFromFile(file)
}

// SourceIdentityFromFile returns the filesystem's stable id for an already
// opened transcript descriptor.
func SourceIdentityFromFile(file *os.File) (string, error) {
	fileID, err := sourceFileID(file)
	if err != nil {
		return "", errors.New(domain.UsageErrorSourceReadFailed)
	}
	return fileID, nil
}
