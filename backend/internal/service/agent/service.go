package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	logger          *slog.Logger
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

// ProjectLookup resolves the launch context used by project-scoped model
// discovery.
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
	if deps.Logger != nil {
		svc.logger = deps.Logger
	}
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
	return &Service{agents: agents, readiness: newReadinessCoordinator(readinessCoordinatorConfig{Agents: agents}), cache: cache, discoverer: discoverer, projects: projects, resolverMu: resolverMu, modelCalls: map[string]*modelCatalogCall{}, modelGeneration: map[string]int64{}, discoverySlots: make(chan struct{}, 2), ctx: context.Background(), now: time.Now, logger: slog.Default()}
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
	// Retain the newest row for every agent/project scope and seed one global job
	// for installed adapters that have never been discovered.
	latest := make(map[string]ports.CachedAgentModelCatalog, len(records)+len(installed))
	for _, record := range records {
		key := record.AgentID + "\x00" + record.ProjectID
		current, exists := latest[key]
		if !exists || record.FetchedAt.After(current.FetchedAt) {
			latest[key] = record
		}
	}
	records = records[:0]
	for agentID := range installed {
		key := agentID + "\x00"
		if _, exists := latest[key]; !exists {
			latest[key] = ports.CachedAgentModelCatalog{AgentID: agentID}
		}
	}
	for _, record := range latest {
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].ProjectID == records[j].ProjectID {
			return records[i].AgentID < records[j].AgentID
		}
		return records[i].ProjectID < records[j].ProjectID
	})
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
				_, _ = s.RevalidateModels(ctx, record.AgentID, record.ProjectID)
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
func (s *Service) Models(ctx context.Context, agentID, projectID string, refresh bool) (ports.AgentModelCatalog, error) {
	if s.discoverer == nil {
		return ports.AgentModelCatalog{}, apierr.Internal("MODEL_DISCOVERY_UNAVAILABLE", "Model discovery is unavailable")
	}
	var err error
	projectID, err = s.modelCatalogScope(ctx, projectID)
	if err != nil {
		return ports.AgentModelCatalog{}, err
	}
	if !refresh {
		if _, ok := s.agent(agentID); !ok {
			return ports.AgentModelCatalog{}, apierr.NotFound("AGENT_NOT_FOUND", "Unknown agent adapter")
		}
		cached, ok, err := s.cachedCatalog(ctx, agentID, projectID)
		if err != nil {
			return ports.AgentModelCatalog{}, err
		}
		if ok {
			// Claude provider model IDs are credential-scoped. Check its local
			// discovery inputs before serving a cache hit so switching provider or
			// credentials cannot briefly expose IDs from the previous provider.
			// The check is local; provider discovery remains cache-first.
			if agentID == "claude-code" && s.modelCatalogInputsChanged(ctx, agentID, projectID, cached.BinaryVersion) {
				return s.coalesceModelLoad(ctx, agentID, projectID, modelLoadCached)
			}
			cached.Catalog = applyCustomModelEntryPolicy(cached.Catalog, s.discoverer.Manual(agentID))
			due := catalogNeedsRevalidation(catalogLastSuccess(cached.Catalog), s.now())
			needsRecovery := cached.RefreshState == "refreshing"
			retriesExhausted := modelCatalogRetriesExhausted(cached)
			cached.Catalog.RefreshRecommended = !retriesExhausted && (due || needsRecovery || cached.RefreshState == "error" || cached.RefreshState == "queued")
			if !retriesExhausted && (due || needsRecovery) && (cached.RetryAt.IsZero() || !s.now().Before(cached.RetryAt)) {
				go func() { _, _ = s.RevalidateModels(s.ctx, agentID, projectID) }()
			} else if !due || retriesExhausted {
				go s.revalidateChangedInputs(agentID, projectID, cached.BinaryVersion)
			}
			return cached.Catalog, nil
		}
	}
	mode := modelLoadCached
	if refresh {
		mode = modelLoadRefresh
	}
	return s.coalesceModelLoad(ctx, agentID, projectID, mode)
}

func (s *Service) revalidateChangedInputs(agentID, projectID, cachedFingerprint string) {
	if s.ctx.Err() != nil {
		return
	}
	if s.modelCatalogInputsChanged(s.ctx, agentID, projectID, cachedFingerprint) {
		_, _ = s.RevalidateModels(s.ctx, agentID, projectID)
	}
}

func (s *Service) modelCatalogInputsChanged(ctx context.Context, agentID, projectID, cachedFingerprint string) bool {
	item, ok := s.agent(agentID)
	if !ok {
		return false
	}
	var binary string
	if resolver, ok := item.Agent.(ports.AgentBinaryResolver); ok {
		lock := s.resolverMu[agentID]
		lock.Lock()
		resolved, err := resolver.ResolveBinary(ctx)
		lock.Unlock()
		if err != nil {
			return false
		}
		binary = resolved
	}
	request, err := s.modelDiscoveryRequest(ctx, agentID, projectID, binary)
	if err != nil {
		return false
	}
	return s.discoverer.CatalogFingerprint(ctx, request) != cachedFingerprint
}

func (s *Service) modelCatalogScope(ctx context.Context, projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" || s.projects == nil {
		return "", nil
	}
	_, ok, err := s.projects.GetProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("resolve model catalog project %s: %w", projectID, err)
	}
	if !ok {
		return "", nil
	}
	return projectID, nil
}

func (s *Service) modelDiscoveryRequest(ctx context.Context, agentID, projectID, binary string) (ports.AgentModelDiscoveryRequest, error) {
	request := ports.AgentModelDiscoveryRequest{AgentID: agentID, Binary: binary}
	if strings.TrimSpace(projectID) == "" || s.projects == nil {
		return request, nil
	}
	project, ok, err := s.projects.GetProject(ctx, projectID)
	if err != nil {
		return ports.AgentModelDiscoveryRequest{}, fmt.Errorf("resolve model discovery project %s: %w", projectID, err)
	}
	if !ok {
		return request, nil
	}
	request.WorkingDir = project.Path
	if len(project.Config.Env) > 0 {
		request.Env = make(map[string]string, len(project.Config.Env))
		for key, value := range project.Config.Env {
			request.Env[key] = value
		}
	}
	return request, nil
}

// RevalidateModels rediscovers a cache-first catalog after the normal read path
// marks it old enough to refresh in the background.
func (s *Service) RevalidateModels(ctx context.Context, agentID, projectID string) (ports.AgentModelCatalog, error) {
	var err error
	projectID, err = s.modelCatalogScope(ctx, projectID)
	if err != nil {
		return ports.AgentModelCatalog{}, err
	}
	return s.coalesceModelLoad(ctx, agentID, projectID, modelLoadRevalidate)
}

// InvalidateModelCatalogs marks existing scopes due and schedules cache-first
// revalidation. The last successful choices remain visible throughout.
func (s *Service) InvalidateModelCatalogs(agentID string) {
	if s.cache == nil {
		return
	}
	go func() {
		records, err := s.cache.ListAgentModelCatalogsByAgent(s.ctx, agentID)
		if err != nil {
			return
		}
		for _, record := range records {
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
				_ = s.saveCatalog(s.ctx, record.ProjectID, catalog, time.Now().UTC().UnixNano(), 0)
			}
			_, _ = s.RevalidateModels(s.ctx, agentID, record.ProjectID)
		}
	}()
}

// InvalidateProjectModelCatalogs marks every cached agent catalog for a changed
// project due without disturbing device-global scopes.
func (s *Service) InvalidateProjectModelCatalogs(projectID string) {
	if s.cache == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	go func() {
		scopeCache, ok := s.cache.(ports.AgentModelCatalogScopeCache)
		if !ok {
			return
		}
		records, err := scopeCache.ListAgentModelCatalogs(s.ctx)
		if err != nil {
			return
		}
		for _, record := range records {
			if record.ProjectID != projectID || s.ctx.Err() != nil {
				continue
			}
			var catalog ports.AgentModelCatalog
			if json.Unmarshal([]byte(record.CatalogJSON), &catalog) != nil {
				continue
			}
			catalog.RefreshRecommended = true
			catalog.RefreshState = "queued"
			catalog.RefreshError = ""
			catalog.LastSuccessAt = nil
			catalog.RetryAt = nil
			_ = s.saveCatalog(s.ctx, projectID, catalog, time.Now().UTC().UnixNano(), 0)
			_, _ = s.RevalidateModels(s.ctx, record.AgentID, projectID)
		}
	}()
}

func (s *Service) coalesceModelLoad(
	ctx context.Context,
	agentID, projectID string,
	mode modelLoadMode,
) (ports.AgentModelCatalog, error) {
	key := agentID + "\x00" + projectID
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
		call.catalog, call.err = s.loadModels(loadCtx, agentID, projectID, mode, call.generation)
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

func (s *Service) loadModels(ctx context.Context, agentID, projectID string, mode modelLoadMode, generation int64) (ports.AgentModelCatalog, error) {
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
	cached, hasCached, err := s.cachedCatalog(ctx, agentID, projectID)
	if err != nil {
		return ports.AgentModelCatalog{}, err
	}
	cached.ProjectID = projectID
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
	request, err := s.modelDiscoveryRequest(ctx, agentID, projectID, binary)
	if err != nil {
		return ports.AgentModelCatalog{}, err
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
	if errors.Is(discoverErr, ports.ErrAgentModelDiscoverySignInRequired) {
		return s.keepCatalogUntilSignIn(persistCtx, projectID, item.Manifest.Name, cached, hasCached, policy, version, generation), nil
	}
	if discoverErr != nil {
		// Provider model IDs are credential-scoped. Reuse a cached catalog only
		// when it was produced from the same discovery inputs; otherwise a revoked
		// key or provider switch could leave invalid IDs in the picker.
		cacheMatchesInputs := hasCached && cached.BinaryVersion == version
		if cacheMatchesInputs && len(cached.Catalog.Models) > 0 {
			cached.Catalog.Stale = true
			cached.Catalog.Warning = discoverErr.Error()
			cached.Catalog.RefreshRecommended = true
			if err := s.saveFailedCatalog(persistCtx, cached, cached.Catalog, generation); err != nil {
				cached.Catalog.Warning = appendCacheWarning(cached.Catalog.Warning)
			} else if updated, ok, _ := s.cachedCatalog(persistCtx, agentID, projectID); ok {
				return updated.Catalog, nil
			}
			return cached.Catalog, nil
		}
		if len(discovered.Models) > 0 {
			discovered.Stale = true
			discovered.Warning = discoverErr.Error()
			discovered.RefreshRecommended = true
			previous := cached
			if !cacheMatchesInputs {
				previous.Catalog.LastSuccessAt = nil
				previous.Catalog.Metadata = catalogMetadata(request)
			}
			if err := s.saveFailedCatalog(persistCtx, previous, discovered, generation); err != nil {
				discovered.Warning = appendCacheWarning(discovered.Warning)
			} else if updated, ok, _ := s.cachedCatalog(persistCtx, agentID, projectID); ok {
				return updated.Catalog, nil
			}
			return discovered, nil
		}
		fallback := policy
		fallback.BinaryVersion = version
		fallback.Stale = true
		fallback.Warning = discoverErr.Error()
		fallback.RefreshRecommended = true
		if err := s.saveFailedCatalog(persistCtx, decodedCatalog{Catalog: fallback, ProjectID: projectID}, fallback, generation); err == nil {
			if updated, found, _ := s.cachedCatalog(persistCtx, agentID, projectID); found {
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
	if err := s.saveCatalog(persistCtx, projectID, discovered, generation, 0); err != nil {
		discovered.Warning = appendCacheWarning(discovered.Warning)
	}
	return discovered, nil
}

// keepCatalogUntilSignIn handles discovery skipped because the agent is
// clearly signed out. That is not a failure: no retry budget is spent and no
// retry timer is set. A cached catalog keeps its models but loses any earlier
// failure marker, and a first load stores an idle placeholder. Both carry the
// sign-in warning, so cache-first reads show it too. The record's validation
// times are cleared so it is due for revalidation regardless of when it last
// loaded: a sign-in made outside AO (for example from a terminal) is picked up
// by the next picker read or daemon start, not only by AO's own auth probe.
// The refresh state stays idle because the picker shows a spinner for queued.
func (s *Service) keepCatalogUntilSignIn(ctx context.Context, projectID, agentName string, cached decodedCatalog, hasCached bool, policy ports.AgentModelCatalog, version string, generation int64) ports.AgentModelCatalog {
	catalog := cached.Catalog
	if !hasCached {
		catalog = policy
		catalog.BinaryVersion = version
		catalog.InputFingerprint = version
	}
	catalog.ValidatedAt = time.Time{}
	catalog.LastSuccessAt = nil
	catalog.Stale = false
	catalog.Warning = agentName + " is not signed in; sign in to load its models"
	catalog.RefreshState = "idle"
	catalog.RefreshError = ""
	catalog.RetryAt = nil
	catalog.RefreshRecommended = false
	if err := s.saveCatalog(ctx, projectID, catalog, generation, 0); err != nil {
		catalog.Warning = appendCacheWarning(catalog.Warning)
	}
	return catalog
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
	ProjectID     string
	BinaryVersion string
	LastSuccessAt time.Time
	RefreshState  string
	RefreshError  string
	RetryCount    int64
	RetryAt       time.Time
	Generation    int64
}

func (s *Service) cachedCatalog(ctx context.Context, agentID, projectID string) (decodedCatalog, bool, error) {
	if s.cache == nil {
		return decodedCatalog{}, false, nil
	}
	record, ok, err := s.cache.GetAgentModelCatalog(ctx, agentID, projectID)
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
	return decodedCatalog{Catalog: catalog, ProjectID: record.ProjectID, BinaryVersion: record.BinaryVersion, LastSuccessAt: catalogLastSuccess(catalog), RefreshState: record.RefreshState, RefreshError: record.RefreshError, RetryCount: record.RetryCount, RetryAt: record.RetryAt, Generation: record.Generation}, true, nil
}

func catalogLastSuccess(catalog ports.AgentModelCatalog) time.Time {
	if catalog.LastSuccessAt == nil {
		return time.Time{}
	}
	return *catalog.LastSuccessAt
}

func (s *Service) saveCatalog(ctx context.Context, projectID string, catalog ports.AgentModelCatalog, generation, retryCount int64) error {
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
		ProjectID:        projectID,
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
	return s.saveCatalog(ctx, cached.ProjectID, catalog, generation, cached.RetryCount)
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
	if err := s.saveCatalog(ctx, previous.ProjectID, catalog, generation, retryCount); err != nil {
		return err
	}
	if catalog.RetryAt != nil && s.cache != nil {
		retryAt := *catalog.RetryAt
		time.AfterFunc(retryDelay, func() {
			if s.ctx.Err() != nil {
				return
			}
			record, ok, err := s.cache.GetAgentModelCatalog(s.ctx, catalog.AgentID, previous.ProjectID)
			if err != nil || !ok || record.Generation != generation || record.RefreshState != "error" || !record.RetryAt.Equal(retryAt) {
				return
			}
			_, _ = s.RevalidateModels(s.ctx, catalog.AgentID, previous.ProjectID)
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
