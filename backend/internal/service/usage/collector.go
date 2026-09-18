package usage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	maxUsageMetadataBytes = 256
	defaultDiscoveryLimit = 64
)

var nativeUsageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ErrUsageSessionNotFound reports that hook metadata targeted no durable AO session.
var ErrUsageSessionNotFound = errors.New("usage session not found")

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
// transcripts. opencode has no certified transcript pipeline, so no
// provider-owned roots are configured; hooks remain the only collection path.
type SourceRoots struct{}

// DefaultSourceRoots resolves provider-owned transcript directories. dataDir
// is AO's already-resolved durable data directory. opencode has no provider
// roots, so this returns an empty SourceRoots.
func DefaultSourceRoots(ctx context.Context, _ string) (SourceRoots, error) {
	if err := ctx.Err(); err != nil {
		return SourceRoots{}, err
	}
	return SourceRoots{}, nil
}

type collectorStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
	UpsertUsageBinding(context.Context, domain.UsageBindingRecord) (domain.UsageBindingRecord, error)
	GetUsageBinding(context.Context, domain.SessionID, domain.AgentHarness, string) (domain.UsageBindingRecord, bool, error)
	ListUsageBindingsForSession(context.Context, domain.SessionID) ([]domain.UsageBindingRecord, error)
	FinalizeUsageBindingsForSessionLaunch(context.Context, domain.SessionID, string, int64, time.Time) ([]domain.UsageBindingRecord, error)
	ListUsageDiscoveryBindings(context.Context, int64) ([]domain.UsageBindingRecord, error)
	UpdateUsageBindingState(context.Context, int64, domain.UsageBindingState, string, time.Time) (bool, error)
	CompleteUsageBindingIfSettled(context.Context, int64, time.Time) (bool, error)
}

// Collector maintains the session billing-binding lifecycle. opencode has no
// certified transcript pipeline, so no usage sources are ever registered: hooks
// record which sessions have a billable route, and the binding state tracks
// whether collection is active or finalizing.
type Collector struct {
	store                collectorStore
	roots                SourceRoots
	notifySourcesChanged func(reconcile bool)
	now                  func() time.Time
	mu                   sync.Mutex
}

// NewCollector constructs a Collector. roots is resolved by DefaultSourceRoots;
// notifySourcesChanged signals the pipeline that discovery or ingestion may be
// needed (reconcile=true) or that the registered inventory changed
// (reconcile=false).
func NewCollector(store collectorStore, roots SourceRoots, notifySourcesChanged func(reconcile bool)) *Collector {
	return &Collector{
		store:                store,
		roots:                roots,
		notifySourcesChanged: notifySourcesChanged,
		now:                  func() time.Time { return time.Now().UTC() },
	}
}

// FinalizeSession ends collection for every binding of a session launch. It is
// called by the lifecycle manager when a session exits.
func (c *Collector) FinalizeSession(
	ctx context.Context,
	sessionID domain.SessionID,
	expectedRuntimeLaunchID string,
	expectedSessionRevision int64,
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
	if len(bindings) > 0 {
		c.notifySourceInventory(false)
	}
	return nil
}

// ReactivateSession resumes collection for the native session relaunched by AO.
// Existing bindings remain untouched; the session binding is merely made
// watchable again so hooks are not required for continued accounting.
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

// RecordHook registers billable-route metadata and updates collection lifecycle
// for one native hook callback. opencode has no certified transcript pipeline,
// so hooks only maintain the session binding record; no transcript sources are
// registered.
func (c *Collector) RecordHook(ctx context.Context, sessionID domain.SessionID, signal HookSignal) error {
	signal.LaunchID = boundedUsageMetadata(signal.LaunchID)
	finalizing := finalizingEvent(signal.Event)
	session, proceed, err := c.hookSession(ctx, sessionID, signal, finalizing)
	if err != nil || !proceed {
		return err
	}
	signal.Harness = session.Harness
	signal.NativeSessionID = boundedUsageMetadata(signal.NativeSessionID)
	if signal.NativeSessionID == "" {
		signal.NativeSessionID = usageNativeSessionID(session)
	}
	if signal.NativeSessionID == "" {
		signal.NativeSessionID = nativeIDFromTranscript(signal.TranscriptPath)
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
	binding, err := c.store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:      sessionID,
		Harness:        session.Harness,
		NativeRootID:   signal.NativeSessionID,
		InitialModelID: boundedUsageMetadata(signal.ModelID),
		ProviderHint:   boundedUsageMetadata(signal.ProviderHint),
		State:          state,
		UpdatedAt:      now,
	})
	if err != nil {
		return err
	}
	// UpsertUsageBinding deliberately refuses to reopen a committed
	// finalizing/complete/partial binding from an 'active' write, so
	// reactivation has to move the state explicitly.
	changed := false
	if reactivating {
		changed, err = c.reactivateBinding(ctx, binding, now)
		if err != nil {
			return err
		}
	}
	if state == domain.UsageBindingFinalizing {
		if err := c.settleFinalizingBinding(ctx, binding.ID, now); err != nil {
			return err
		}
	}
	c.notifySourceInventory(changed || state == domain.UsageBindingFinalizing)
	return nil
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

// BackfillActive records bindings for live/resumable AO sessions. It
// deliberately does not import terminated session history.
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
	if exists && session.Activity.State == domain.ActivityExited &&
		(existing.State == domain.UsageBindingComplete || existing.State == domain.UsageBindingPartial) {
		return nil
	}
	if exists && session.Activity.State != domain.ActivityExited {
		if _, err := c.reactivateBinding(ctx, existing, now); err != nil {
			return err
		}
		existing.State = domain.UsageBindingActive
		existing.LastErrorCode = ""
	}

	state := existing.State
	if !exists {
		state = domain.UsageBindingActive
		if session.Activity.State == domain.ActivityExited {
			state = domain.UsageBindingFinalizing
		}
	} else if session.Activity.State == domain.ActivityExited &&
		(state == domain.UsageBindingDiscovering || state == domain.UsageBindingActive) {
		state = domain.UsageBindingFinalizing
	}
	binding, err := c.store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:      session.ID,
		Harness:        session.Harness,
		NativeRootID:   nativeID,
		InitialModelID: existing.InitialModelID,
		ProviderHint:   existing.ProviderHint,
		State:          state,
		UpdatedAt:      now,
	})
	if err != nil {
		return err
	}
	if state == domain.UsageBindingFinalizing {
		return c.settleFinalizingBinding(ctx, binding.ID, now)
	}
	return nil
}

// ReconcileSources is the bounded discovery pass. opencode registers no
// transcript sources, so discovery never produces work; the pass exists to keep
// the reconciled lifecycle complete for bindings created before a session
// exited or carrying a legacy discovering state.
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

func (c *Collector) reconcileBinding(ctx context.Context, binding domain.UsageBindingRecord, now time.Time) error {
	session, ok, err := c.store.GetSession(ctx, binding.SessionID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if (session.IsTerminated || session.Activity.State == domain.ActivityExited) && binding.State != domain.UsageBindingFinalizing {
		if _, err := c.store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingFinalizing, "", now); err != nil {
			return err
		}
		return c.settleFinalizingBinding(ctx, binding.ID, now)
	}
	if binding.State == domain.UsageBindingDiscovering {
		_, err := c.store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingActive, "", now)
		return err
	}
	return nil
}

func (c *Collector) finalizeSession(ctx context.Context, sessionID domain.SessionID, now time.Time) error {
	bindings, err := c.store.ListUsageBindingsForSession(ctx, sessionID)
	if err != nil {
		return err
	}
	var errs []error
	for _, binding := range bindings {
		if binding.State != domain.UsageBindingActive && binding.State != domain.UsageBindingDiscovering {
			continue
		}
		if _, err := c.store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingFinalizing, "", now); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := c.settleFinalizingBinding(ctx, binding.ID, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Collector) reactivateBinding(ctx context.Context, binding domain.UsageBindingRecord, now time.Time) (bool, error) {
	return c.store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingActive, "", now)
}

func (c *Collector) settleFinalizingBinding(ctx context.Context, bindingID int64, now time.Time) error {
	_, err := c.store.CompleteUsageBindingIfSettled(ctx, bindingID, now)
	return err
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