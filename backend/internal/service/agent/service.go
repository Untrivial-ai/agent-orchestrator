package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	modelCatalogLoadTimeout = 30 * time.Second
	// Retained for compatibility with focused tests that construct an obviously
	// old timestamp; production freshness is calendar-day based below.
	modelCatalogTrustWindow = 6 * time.Hour
	// How long a cached catalog is trusted before AO asks a cache-first client to
	// revalidate in the background. Long, because rediscovery runs an agent CLI:
	// this covers drift a fingerprint cannot see, not routine correctness.
	modelCatalogMonitorInterval = time.Minute
	modelCatalogWakeThreshold   = 3 * time.Minute
	modelCatalogMaxRetries      = 3
	modelCatalogRetryDelays     = [...]time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
)

// catalogNeedsRevalidation uses the machine's calendar date, not an elapsed
// duration. A catalog remains fresh through the user's local day and becomes
// due after midnight, including after timezone changes or sleep/wake.
func catalogNeedsRevalidation(lastSuccess, now time.Time) bool {
	if lastSuccess.IsZero() {
		return true
	}
	y, m, d := now.Date()
	ly, lm, ld := lastSuccess.In(now.Location()).Date()
	return y != ly || m != lm || d != ld
}

func catalogClockDiscontinuity(previous, now time.Time, previousZone string, previousOffset int) bool {
	zone, offset := now.Zone()
	return catalogNeedsRevalidation(previous, now) || now.Sub(previous) > modelCatalogWakeThreshold || zone != previousZone || offset != previousOffset || now.Before(previous)
}

type modelLoadMode uint8

const (
	modelLoadCached modelLoadMode = iota
	modelLoadRevalidate
	modelLoadRefresh
)

type modelCatalogCall struct {
	done       chan struct{}
	catalog    ports.AgentModelCatalog
	err        error
	generation int64
}

// Service owns normalized harness readiness and the unchanged model catalog.
// Consumers share coordinator checks instead of probing adapters directly.
type Service struct {
	agents          []agentregistry.HarnessAgent
	readiness       *readinessCoordinator
	cache           ports.AgentModelCatalogCache
	discoverer      ports.AgentModelDiscoverer
	projects        ProjectLookup
	sessions        SessionUsageLookup
	resolverMu      map[string]*sync.Mutex
	modelCallMu     sync.Mutex
	modelCalls      map[string]*modelCatalogCall
	modelGeneration map[string]int64
	discoverySlots  chan struct{}
	ctx             context.Context
	now             func() time.Time
	codexAccounts   *codexAccountManager
	codexSwitches   *codexAccountSwitchCoordinator
}

// Deps contains optional durable dependencies for the agent catalog service.
type Deps struct {
	Cache                  ports.AgentModelCatalogCache
	Discoverer             ports.AgentModelDiscoverer
	Projects               ProjectLookup
	Sessions               SessionUsageLookup
	Context                context.Context
	Logger                 *slog.Logger
	CodexAccountRoot       string
	CodexPendingRoot       string
	CodexSwitchStagingRoot string
	CodexGlobalHome        string
	CodexAccounts          ports.CodexAccountClientFactory
	CodexAccountSwitches   ports.CodexAccountSwitchStore
	CodexOperationGate     ports.CodexOperationGate
	// Clock overrides time.Now for deterministic account-bootstrap retry tests.
	Clock func() time.Time
}

// ProjectLookup is retained for constructor compatibility. Model catalogs are
// global and never read project paths or environment.
type ProjectLookup interface {
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

// SessionUsageLookup provides durable session facts used to rank agent choices.
// The SQLite store satisfies this narrow read boundary.
type SessionUsageLookup interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

// New returns an agent service backed by the daemon's shipped adapter registry.
func New() *Service {
	return NewWithDeps(Deps{})
}

// NewWithDeps returns the production service with in-memory readiness and a
// durable model-catalog cache.
func NewWithDeps(deps Deps) *Service {
	agents := agentregistry.Harnessed()
	svc := newService(agents, deps.Cache, deps.Projects, deps.Discoverer)
	if deps.CodexAccountRoot != "" && deps.CodexGlobalHome != "" {
		svc.codexAccounts = newCodexAccountManager(deps.Context, deps.CodexAccountRoot, deps.CodexPendingRoot, deps.CodexSwitchStagingRoot, deps.CodexGlobalHome, deps.CodexAccounts, deps.Logger, deps.CodexOperationGate)
		if deps.Clock != nil {
			svc.codexAccounts.now = deps.Clock
		}
	}
	svc.readiness = newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: agents, Factory: agentregistry.Harnessed, Context: deps.Context, Logger: deps.Logger,
		AuthenticationCheck: svc.structuredCodexAuthentication,
	})
	if svc.codexAccounts != nil {
		svc.codexAccounts.onAuthenticationChanged = func() {
			svc.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
		}
	}
	if svc.codexAccounts != nil && deps.CodexAccountSwitches != nil && deps.CodexOperationGate != nil {
		svc.codexSwitches = newCodexAccountSwitchCoordinator(
			deps.Context, svc, deps.CodexAccountSwitches, deps.CodexOperationGate,
			deps.Clock, svc.PublishCodexAccounts,
		)
	}
	svc.sessions = deps.Sessions
	if deps.Context != nil {
		svc.ctx = deps.Context
	}
	if deps.Clock != nil {
		svc.now = deps.Clock
	}
	return svc
}

// NewWithAgents returns an agent service over a caller-provided adapter slice.
// It is used by focused tests.
func NewWithAgents(agents []agentregistry.HarnessAgent) *Service {
	svc := newService(agents, nil, nil, nil)
	svc.readiness = newReadinessCoordinator(readinessCoordinatorConfig{Agents: agents})
	return svc
}

func newService(agents []agentregistry.HarnessAgent, cache ports.AgentModelCatalogCache, projects ProjectLookup, discoverer ports.AgentModelDiscoverer) *Service {
	resolverMu := make(map[string]*sync.Mutex, len(agents))
	for _, item := range agents {
		resolverMu[string(item.Harness)] = &sync.Mutex{}
	}
	return &Service{agents: agents, readiness: newReadinessCoordinator(readinessCoordinatorConfig{Agents: agents}), cache: cache, discoverer: discoverer, projects: projects, resolverMu: resolverMu, modelCalls: map[string]*modelCatalogCall{}, modelGeneration: map[string]int64{}, discoverySlots: make(chan struct{}, 2), ctx: context.Background(), now: time.Now}
}

// WarmModelCatalogs starts the bounded cache scheduler. Readiness is never held
// up by model discovery; at most two adapter discoveries run at once.
func (s *Service) WarmModelCatalogs(ctx context.Context) {
	if s.cache == nil || s.discoverer == nil {
		return
	}
	go func() {
		s.prefetchModelCatalogs(ctx, false)
		s.monitorModelCatalogFreshness(ctx)
	}()
}

func (s *Service) prefetchModelCatalogs(ctx context.Context, force bool) {
	scopeCache, ok := s.cache.(ports.AgentModelCatalogScopeCache)
	var records []ports.CachedAgentModelCatalog
	var err error
	if ok {
		records, err = scopeCache.ListAgentModelCatalogs(ctx)
	} else {
		for _, item := range s.agents {
			rows, listErr := s.cache.ListAgentModelCatalogsByAgent(ctx, string(item.Harness))
			if listErr != nil {
				err = listErr
				break
			}
			records = append(records, rows...)
		}
	}
	if err != nil || ctx.Err() != nil {
		return
	}
	readiness, err := s.readiness.EnsureInstallation(ctx, nil, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return
	}
	installed := make(map[string]struct{}, len(readiness))
	for _, item := range readiness {
		if item.Installation.State == domain.AgentInstallationInstalled {
			installed[item.ID] = struct{}{}
		}
	}
	// Model catalogs belong to installed agents, not projects. Seed one global
	// job for every installed adapter, including on a completely fresh database.
	latest := make(map[string]ports.CachedAgentModelCatalog, len(installed))
	for _, record := range records {
		if record.ProjectID != "" {
			continue
		}
		current, exists := latest[record.AgentID]
		if !exists || record.FetchedAt.After(current.FetchedAt) {
			latest[record.AgentID] = record
		}
	}
	records = records[:0]
	for agentID := range installed {
		record := latest[agentID]
		record.AgentID = agentID
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].AgentID < records[j].AgentID })
	jobs := make(chan ports.CachedAgentModelCatalog, len(records))
	for _, record := range records {
		if _, eligible := installed[record.AgentID]; !eligible {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if force || catalogNeedsRevalidation(record.LastSuccessAt, s.now()) || record.RefreshState != "idle" {
			jobs <- record
		}
	}
	close(jobs)
	var workers sync.WaitGroup
	for range cap(s.discoverySlots) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for record := range jobs {
				if ctx.Err() != nil {
					return
				}
				_, _ = s.RevalidateModels(ctx, record.AgentID, "")
			}
		}()
	}
	workers.Wait()
}

// warmModelCatalogs retains the focused synchronous test seam used by the
// original static-catalog warmer. Production uses the generalized scheduler.
func (s *Service) warmModelCatalogs(ctx context.Context) {
	for _, agentID := range []string{"claude-code", "muse"} {
		record, ok, err := s.cache.GetAgentModelCatalog(ctx, agentID, "")
		if err != nil || !ok {
			continue
		}
		var cached ports.AgentModelCatalog
		if json.Unmarshal([]byte(record.CatalogJSON), &cached) != nil {
			continue
		}
		lastSuccess := record.LastSuccessAt
		if lastSuccess.IsZero() {
			lastSuccess = cached.ValidatedAt
		}
		if !cached.Stale && !catalogNeedsRevalidation(lastSuccess, s.now()) {
			continue
		}
		_, _ = s.RevalidateModels(ctx, agentID, "")
	}
}

func (s *Service) monitorModelCatalogFreshness(ctx context.Context) {
	ticker := time.NewTicker(modelCatalogMonitorInterval)
	defer ticker.Stop()
	last := s.now()
	lastZone, lastOffset := last.Zone()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.now()
			zone, offset := now.Zone()
			if catalogClockDiscontinuity(last, now, lastZone, lastOffset) {
				s.prefetchModelCatalogs(ctx, true)
			}
			last, lastZone, lastOffset = now, zone, offset
		}
	}
}

// Models returns one normalized model catalog. Cached values survive daemon
// restarts; refresh forces a new documented CLI discovery attempt. Discovery
// failures degrade to the last cached catalog or a custom model input.
func (s *Service) Models(ctx context.Context, agentID, _ string, refresh bool) (ports.AgentModelCatalog, error) {
	if s.discoverer == nil {
		return ports.AgentModelCatalog{}, apierr.Internal("MODEL_DISCOVERY_UNAVAILABLE", "Model discovery is unavailable")
	}
	if !refresh {
		if _, ok := s.agent(agentID); !ok {
			return ports.AgentModelCatalog{}, apierr.NotFound("AGENT_NOT_FOUND", "Unknown agent adapter")
		}
		cached, ok, err := s.cachedCatalog(ctx, agentID)
		if err != nil {
			return ports.AgentModelCatalog{}, err
		}
		if ok {
			cached.Catalog = applyCustomModelEntryPolicy(cached.Catalog, s.discoverer.Manual(agentID))
			due := catalogNeedsRevalidation(catalogLastSuccess(cached.Catalog), s.now())
			needsRecovery := cached.RefreshState == "refreshing"
			retriesExhausted := modelCatalogRetriesExhausted(cached)
			cached.Catalog.RefreshRecommended = !retriesExhausted && (due || needsRecovery || cached.RefreshState == "error" || cached.RefreshState == "queued")
			if !retriesExhausted && (due || needsRecovery) && (cached.RetryAt.IsZero() || !s.now().Before(cached.RetryAt)) {
				go func() { _, _ = s.RevalidateModels(s.ctx, agentID, "") }()
			} else if !due || retriesExhausted {
				go s.revalidateChangedInputs(agentID, cached.BinaryVersion)
			}
			return cached.Catalog, nil
		}
	}
	mode := modelLoadCached
	if refresh {
		mode = modelLoadRefresh
	}
	return s.coalesceModelLoad(ctx, agentID, mode)
}

func (s *Service) revalidateChangedInputs(agentID, cachedFingerprint string) {
	if s.ctx.Err() != nil {
		return
	}
	item, ok := s.agent(agentID)
	if !ok {
		return
	}
	var binary string
	if resolver, ok := item.Agent.(ports.AgentBinaryResolver); ok {
		lock := s.resolverMu[agentID]
		lock.Lock()
		resolved, err := resolver.ResolveBinary(s.ctx)
		lock.Unlock()
		if err != nil {
			return
		}
		binary = resolved
	}
	request := ports.AgentModelDiscoveryRequest{AgentID: agentID, Binary: binary}
	if s.discoverer.CatalogFingerprint(s.ctx, request) != cachedFingerprint {
		_, _ = s.RevalidateModels(s.ctx, agentID, "")
	}
}

// RevalidateModels rediscovers a cache-first catalog after the normal read path
// marks it old enough to refresh in the background.
func (s *Service) RevalidateModels(ctx context.Context, agentID, _ string) (ports.AgentModelCatalog, error) {
	return s.coalesceModelLoad(ctx, agentID, modelLoadRevalidate)
}

// InvalidateModelCatalogs marks existing scopes due and schedules cache-first
// revalidation. The last successful choices remain visible throughout.
func (s *Service) InvalidateModelCatalogs(agentID string) {
	if s.cache == nil {
		return
	}
	go func() {
		record, ok, err := s.cache.GetAgentModelCatalog(s.ctx, agentID, "")
		if err != nil || !ok {
			return
		}
		if s.ctx.Err() != nil {
			return
		}
		var catalog ports.AgentModelCatalog
		if json.Unmarshal([]byte(record.CatalogJSON), &catalog) == nil {
			catalog.RefreshRecommended = true
			catalog.RefreshState = "queued"
			catalog.RefreshError = ""
			catalog.LastSuccessAt = nil
			catalog.RetryAt = nil
			_ = s.saveCatalog(s.ctx, catalog, time.Now().UTC().UnixNano(), 0)
		}
		_, _ = s.RevalidateModels(s.ctx, agentID, "")
	}()
}

// InvalidateProjectModelCatalogs is retained as a project-service callback.
// Global agent catalogs are deliberately unaffected by project changes.
func (s *Service) InvalidateProjectModelCatalogs(_ string) {
}

func (s *Service) coalesceModelLoad(
	ctx context.Context,
	agentID string,
	mode modelLoadMode,
) (ports.AgentModelCatalog, error) {
	key := agentID
	s.modelCallMu.Lock()
	if active := s.modelCalls[key]; active != nil {
		s.modelCallMu.Unlock()
		select {
		case <-active.done:
			return active.catalog, active.err
		case <-ctx.Done():
			return ports.AgentModelCatalog{}, ctx.Err()
		}
	}
	wallGeneration := time.Now().UTC().UnixNano()
	if s.modelGeneration[key] < wallGeneration {
		s.modelGeneration[key] = wallGeneration
	} else {
		s.modelGeneration[key]++
	}
	call := &modelCatalogCall{done: make(chan struct{}), generation: s.modelGeneration[key]}
	s.modelCalls[key] = call
	s.modelCallMu.Unlock()

	baseCtx := s.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	go func() {
		select {
		case s.discoverySlots <- struct{}{}:
			defer func() { <-s.discoverySlots }()
		case <-baseCtx.Done():
			call.err = baseCtx.Err()
			s.modelCallMu.Lock()
			delete(s.modelCalls, key)
			close(call.done)
			s.modelCallMu.Unlock()
			return
		}
		loadCtx, cancel := context.WithTimeout(baseCtx, modelCatalogLoadTimeout)
		defer cancel()
		call.catalog, call.err = s.loadModels(loadCtx, agentID, mode, call.generation)
		s.modelCallMu.Lock()
		delete(s.modelCalls, key)
		close(call.done)
		s.modelCallMu.Unlock()
	}()

	select {
	case <-call.done:
		return call.catalog, call.err
	case <-ctx.Done():
		return ports.AgentModelCatalog{}, ctx.Err()
	}
}

func (s *Service) loadModels(ctx context.Context, agentID string, mode modelLoadMode, generation int64) (ports.AgentModelCatalog, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentModelCatalog{}, err
	}
	item, ok := s.agent(agentID)
	if !ok {
		return ports.AgentModelCatalog{}, apierr.NotFound("AGENT_NOT_FOUND", "Unknown agent adapter")
	}
	if s.discoverer == nil {
		return ports.AgentModelCatalog{}, apierr.Internal("MODEL_DISCOVERY_UNAVAILABLE", "Model discovery is unavailable")
	}
	cached, hasCached, err := s.cachedCatalog(ctx, agentID)
	if err != nil {
		return ports.AgentModelCatalog{}, err
	}
	policy := s.discoverer.Manual(agentID)
	if hasCached {
		cached.Catalog = applyCustomModelEntryPolicy(cached.Catalog, policy)
	}
	var binary string
	if resolver, ok := item.Agent.(ports.AgentBinaryResolver); ok {
		lock := s.resolverMu[agentID]
		lock.Lock()
		resolved, err := resolver.ResolveBinary(ctx)
		lock.Unlock()
		if err == nil {
			binary = resolved
		}
	}
	request := ports.AgentModelDiscoveryRequest{
		AgentID: agentID, Binary: binary,
	}
	// Fingerprints the same inputs the discovery run would read, so a change to
	// either the executable or the configuration behind it invalidates the cache.
	version := s.discoverer.CatalogFingerprint(ctx, request)
	inputsChanged := hasCached && cached.BinaryVersion != version
	explicitlyInvalidated := hasCached && (cached.RefreshState == "queued" || cached.RefreshState == "refreshing")
	if hasCached && mode == modelLoadCached && cached.BinaryVersion == version {
		// A command-backed catalog can drift without the binary or its config
		// changing (a provider adds a model), which no fingerprint can see. Ask
		// cache-first clients to revalidate in the background once the catalog is
		// old enough, so staleness resolves itself instead of waiting for someone
		// to press a refresh button.
		cached.Catalog.RefreshRecommended = !modelCatalogRetriesExhausted(cached) && catalogNeedsRevalidation(catalogLastSuccess(cached.Catalog), s.now())
		return cached.Catalog, nil
	}

	if mode != modelLoadRefresh && hasCached && !inputsChanged && !explicitlyInvalidated && modelCatalogRetriesExhausted(cached) {
		cached.Catalog.RefreshRecommended = false
		return cached.Catalog, nil
	}
	if mode != modelLoadRefresh && hasCached && !inputsChanged && !explicitlyInvalidated && !cached.RetryAt.IsZero() && s.now().Before(cached.RetryAt) {
		cached.Catalog.RefreshState = "error"
		cached.Catalog.RefreshError = cached.RefreshError
		cached.Catalog.RetryAt = modelCatalogRetryAt(cached.RetryAt)
		cached.Catalog.RefreshRecommended = true
		return cached.Catalog, nil
	}
	if mode == modelLoadRefresh || inputsChanged || explicitlyInvalidated {
		cached.RetryCount = 0
		cached.RetryAt = time.Time{}
		cached.RefreshError = ""
		cached.Catalog.RetryAt = nil
	}
	// Only an explicit user refresh surfaces a loading state. Automatic daily,
	// wake, and input-change revalidation keeps the last-known-good catalog
	// visible without causing cache subscribers to show a loader.
	if mode == modelLoadRefresh {
		_ = s.persistCatalogState(ctx, cached, hasCached, "refreshing", "", time.Time{}, generation)
	}
	discovered, discoverErr := s.discoverer.Discover(ctx, request)
	discovered = applyCustomModelEntryPolicy(discovered, policy)
	discovered.BinaryVersion = version
	persistCtx := s.ctx
	if persistCtx == nil {
		persistCtx = context.Background()
	}
	if discoverErr != nil {
		if hasCached {
			cached.Catalog.Stale = true
			cached.Catalog.Warning = discoverErr.Error()
			cached.Catalog.RefreshRecommended = true
			if err := s.saveFailedCatalog(persistCtx, cached, cached.Catalog, generation); err != nil {
				cached.Catalog.Warning = appendCacheWarning(cached.Catalog.Warning)
			} else if updated, ok, _ := s.cachedCatalog(persistCtx, agentID); ok {
				return updated.Catalog, nil
			}
			return cached.Catalog, nil
		}
		if len(discovered.Models) > 0 {
			discovered.Stale = true
			discovered.Warning = discoverErr.Error()
			discovered.RefreshRecommended = true
			if err := s.saveFailedCatalog(persistCtx, cached, discovered, generation); err != nil {
				discovered.Warning = appendCacheWarning(discovered.Warning)
			} else if updated, ok, _ := s.cachedCatalog(persistCtx, agentID); ok {
				return updated.Catalog, nil
			}
			return discovered, nil
		}
		fallback := policy
		fallback.BinaryVersion = version
		fallback.Stale = true
		fallback.Warning = discoverErr.Error()
		fallback.RefreshRecommended = true
		if err := s.saveFailedCatalog(persistCtx, decodedCatalog{Catalog: fallback}, fallback, generation); err == nil {
			if updated, found, _ := s.cachedCatalog(persistCtx, agentID); found {
				return updated.Catalog, nil
			}
		}
		return fallback, nil
	}
	now := s.now().UTC()
	discovered.ValidatedAt = now
	discovered.LastSuccessAt = &now
	discovered.InputFingerprint = version
	discovered.Metadata = catalogMetadata(request)
	discovered.RefreshState = "idle"
	discovered.RefreshError = ""
	discovered.RetryAt = nil
	discovered.RefreshRecommended = false
	if err := s.saveCatalog(persistCtx, discovered, generation, 0); err != nil {
		discovered.Warning = appendCacheWarning(discovered.Warning)
	}
	return discovered, nil
}

func applyCustomModelEntryPolicy(catalog, policy ports.AgentModelCatalog) ports.AgentModelCatalog {
	entryMode := policy.CustomModelEntry
	if entryMode == "" {
		if policy.AllowCustom {
			entryMode = ports.CustomModelEntryDirect
		} else {
			entryMode = ports.CustomModelEntryNone
		}
	}
	catalog.CustomModelEntry = entryMode
	catalog.AllowCustom = entryMode == ports.CustomModelEntryDirect
	return catalog
}

func appendCacheWarning(current string) string {
	const next = "Models loaded, but AO could not update the model cache."
	if current == "" {
		return next
	}
	return current + " " + next
}

type decodedCatalog struct {
	Catalog       ports.AgentModelCatalog
	BinaryVersion string
	LastSuccessAt time.Time
	RefreshState  string
	RefreshError  string
	RetryCount    int64
	RetryAt       time.Time
	Generation    int64
}

func (s *Service) cachedCatalog(ctx context.Context, agentID string) (decodedCatalog, bool, error) {
	if s.cache == nil {
		return decodedCatalog{}, false, nil
	}
	record, ok, err := s.cache.GetAgentModelCatalog(ctx, agentID, "")
	if err != nil || !ok {
		return decodedCatalog{}, ok, err
	}
	var catalog ports.AgentModelCatalog
	if err := json.Unmarshal([]byte(record.CatalogJSON), &catalog); err != nil {
		return decodedCatalog{}, false, fmt.Errorf("decode cached model catalog for %s: %w", agentID, err)
	}
	if catalog.Models == nil {
		catalog.Models = []ports.AgentModelInfo{}
	}
	if catalog.LastSuccessAt == nil || catalog.LastSuccessAt.IsZero() {
		lastSuccess := record.LastSuccessAt
		if lastSuccess.IsZero() {
			lastSuccess = catalog.ValidatedAt
		}
		if !lastSuccess.IsZero() {
			catalog.LastSuccessAt = &lastSuccess
		}
	}
	catalog.RefreshState = record.RefreshState
	catalog.RefreshError = record.RefreshError
	catalog.RetryAt = modelCatalogRetryAt(record.RetryAt)
	return decodedCatalog{Catalog: catalog, BinaryVersion: record.BinaryVersion, LastSuccessAt: catalogLastSuccess(catalog), RefreshState: record.RefreshState, RefreshError: record.RefreshError, RetryCount: record.RetryCount, RetryAt: record.RetryAt, Generation: record.Generation}, true, nil
}

func catalogLastSuccess(catalog ports.AgentModelCatalog) time.Time {
	if catalog.LastSuccessAt == nil {
		return time.Time{}
	}
	return *catalog.LastSuccessAt
}

func (s *Service) saveCatalog(ctx context.Context, catalog ports.AgentModelCatalog, generation, retryCount int64) error {
	if s.cache == nil {
		return nil
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode model catalog for %s: %w", catalog.AgentID, err)
	}
	metadata, _ := json.Marshal(catalog.Metadata)
	return s.cache.UpsertAgentModelCatalog(ctx, ports.CachedAgentModelCatalog{
		AgentID:          catalog.AgentID,
		ProjectID:        "",
		BinaryVersion:    catalog.BinaryVersion,
		CatalogJSON:      string(data),
		Source:           catalog.Source,
		FetchedAt:        catalog.FetchedAt,
		MetadataJSON:     string(metadata),
		InputFingerprint: catalog.InputFingerprint,
		LastSuccessAt:    catalogLastSuccess(catalog),
		RefreshState:     catalog.RefreshState,
		RefreshError:     catalog.RefreshError,
		RetryCount:       retryCount,
		RetryAt:          catalogRetryTime(catalog.RetryAt),
		Generation:       generation,
	})
}

func catalogMetadata(request ports.AgentModelDiscoveryRequest) map[string]string {
	metadata := map[string]string{"scope": "global"}
	if request.WorkingDir != "" {
		metadata["scope"] = "project"
	}
	if request.Binary != "" {
		metadata["binary"] = request.Binary
	}
	return metadata
}

func (s *Service) persistCatalogState(ctx context.Context, cached decodedCatalog, hasCached bool, state, message string, retryAt time.Time, generation int64) error {
	if !hasCached {
		return nil
	}
	catalog := cached.Catalog
	catalog.RefreshState = state
	catalog.RefreshError = message
	catalog.RetryAt = modelCatalogRetryAt(retryAt)
	return s.saveCatalog(ctx, catalog, generation, cached.RetryCount)
}

func (s *Service) saveFailedCatalog(ctx context.Context, previous decodedCatalog, catalog ports.AgentModelCatalog, generation int64) error {
	retryCount := previous.RetryCount + 1
	catalog.LastSuccessAt = previous.Catalog.LastSuccessAt
	catalog.RefreshState = "error"
	catalog.RefreshError = catalog.Warning
	catalog.InputFingerprint = catalog.BinaryVersion
	catalog.Metadata = previous.Catalog.Metadata
	catalog.RetryAt = nil
	catalog.RefreshRecommended = retryCount <= int64(modelCatalogMaxRetries)
	var retryDelay time.Duration
	if retryCount <= int64(modelCatalogMaxRetries) {
		retryDelay = modelCatalogRetryDelays[retryCount-1]
		retryAt := s.now().Add(retryDelay).UTC()
		catalog.RetryAt = &retryAt
	}
	if err := s.saveCatalog(ctx, catalog, generation, retryCount); err != nil {
		return err
	}
	if catalog.RetryAt != nil && s.cache != nil {
		retryAt := *catalog.RetryAt
		time.AfterFunc(retryDelay, func() {
			if s.ctx.Err() != nil {
				return
			}
			record, ok, err := s.cache.GetAgentModelCatalog(s.ctx, catalog.AgentID, "")
			if err != nil || !ok || record.Generation != generation || record.RefreshState != "error" || !record.RetryAt.Equal(retryAt) {
				return
			}
			_, _ = s.RevalidateModels(s.ctx, catalog.AgentID, "")
		})
	}
	return nil
}

func modelCatalogRetriesExhausted(cached decodedCatalog) bool {
	return cached.RefreshState == "error" && cached.RetryCount > int64(modelCatalogMaxRetries)
}

func modelCatalogRetryAt(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func catalogRetryTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func (s *Service) agent(agentID string) (agentregistry.HarnessAgent, bool) {
	for _, item := range s.agents {
		if string(item.Harness) == agentID {
			return item, true
		}
	}
	return agentregistry.HarnessAgent{}, false
}

// ResolveAgentBinary resolves one harness through its shipped adapter. This is
// the shared boundary for features that must launch the same executable normal
// session startup recognizes, including managed locations outside PATH.
func (s *Service) ResolveAgentBinary(ctx context.Context, agentID string) (string, error) {
	item, ok := s.agent(agentID)
	if !ok {
		return "", apierr.Invalid("AGENT_UNKNOWN", fmt.Sprintf("unknown agent %q", agentID), nil)
	}
	resolver, ok := item.Agent.(ports.AgentBinaryResolver)
	if !ok {
		return "", fmt.Errorf("agent %s: %w", agentID, ports.ErrAgentBinaryNotFound)
	}
	lock := s.resolverMu[agentID]
	lock.Lock()
	defer lock.Unlock()
	return resolver.ResolveBinary(ctx)
}
