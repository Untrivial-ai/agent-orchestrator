package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	agentInstallProbeTimeout = 2 * time.Second
	agentAuthProbeTimeout    = 10 * time.Second
	agentRefreshMinInterval  = 10 * time.Second
	modelCatalogLoadTimeout  = 30 * time.Second
	// How long a cached catalog is trusted before AO asks a cache-first client to
	// revalidate in the background. Long, because rediscovery runs an agent CLI:
	// this covers drift a fingerprint cannot see, not routine correctness.
	modelCatalogTrustWindow = 6 * time.Hour
)

// catalogNeedsRevalidation reports whether a cached catalog is old enough to
// re-check. A zero timestamp comes from a record written before validation was
// tracked, so it counts as due.
func catalogNeedsRevalidation(validatedAt time.Time) bool {
	return validatedAt.IsZero() || time.Since(validatedAt) > modelCatalogTrustWindow
}

type modelLoadMode uint8

const (
	modelLoadCached modelLoadMode = iota
	modelLoadRevalidate
	modelLoadRefresh
)

type modelCatalogCall struct {
	done    chan struct{}
	catalog ports.AgentModelCatalog
	err     error
}

type probeResult struct {
	info       Info
	installed  bool
	authorized bool
}

// ProbeResult describes a fresh readiness probe for one supported agent.
type ProbeResult struct {
	Agent     Info `json:"agent"`
	Supported bool `json:"supported"`
	Installed bool `json:"installed"`
}

// Info is the user-facing identity for an agent adapter.
type Info struct {
	ID         string                `json:"id"`
	Label      string                `json:"label"`
	AuthStatus ports.AgentAuthStatus `json:"authStatus,omitempty" enum:"authorized,unauthorized,unknown" description:"Advisory local auth probe result. authorized means a recent local probe passed; spawn remains the authoritative validation point."`
	UsageCount int                   `json:"usageCount,omitempty" description:"Number of retained sessions currently attributed to this agent."`
	LastUsedAt *time.Time            `json:"lastUsedAt,omitempty" format:"date-time" description:"Creation time of the newest retained session currently attributed to this agent."`
}

// Inventory describes all daemon-supported agents and best-effort local probe
// results. Installed/authorized entries are advisory snapshots and can be stale;
// session spawn is the authoritative validation point for binary availability,
// runtime prerequisites, and model-call readiness.
type Inventory struct {
	Supported  []Info `json:"supported" description:"Agents supported by this daemon build."`
	Installed  []Info `json:"installed" description:"Agents whose binary resolved during the latest best-effort local catalog probe."`
	Authorized []Info `json:"authorized" description:"Compatibility list of installed agents whose local auth probe recently returned authorized. Advisory and stale-prone; spawn may still fail."`
}

type cachedInventory struct {
	Installed  []Info `json:"installed"`
	Authorized []Info `json:"authorized"`
}

// Service reports supported agent adapters and best-effort local readiness
// probes. Catalog readiness is advisory UI metadata, not a spawn precheck.
type Service struct {
	agents         []agentregistry.HarnessAgent
	cache          ports.AgentModelCatalogCache
	inventoryCache ports.AgentInventoryCache
	discoverer     ports.AgentModelDiscoverer
	projects       ProjectLookup
	sessions       SessionUsageLookup
	resolverMu     map[string]*sync.Mutex
	modelCallMu    sync.Mutex
	modelCalls     map[string]*modelCatalogCall

	mu              sync.RWMutex
	inventory       Inventory
	inventoryLoaded bool
	inventoryLoadMu sync.Mutex
	lastRefresh     time.Time
	refreshMu       sync.Mutex
}

// Deps contains optional durable dependencies for the agent catalog service.
type Deps struct {
	Cache          ports.AgentModelCatalogCache
	InventoryCache ports.AgentInventoryCache
	Discoverer     ports.AgentModelDiscoverer
	Projects       ProjectLookup
	Sessions       SessionUsageLookup
}

// ProjectLookup resolves the registered working directory used for model
// discovery. The SQLite store satisfies this narrow read boundary.
type ProjectLookup interface {
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

// SessionUsageLookup provides durable session facts used to rank agent choices.
// The SQLite store satisfies this narrow read boundary.
type SessionUsageLookup interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

// New returns an agent inventory service backed by the daemon's shipped
// adapter registry.
func New() *Service {
	return NewWithDeps(Deps{})
}

// NewWithDeps returns the production service with durable inventory and model-catalog caches.
func NewWithDeps(deps Deps) *Service {
	svc := newService(agentregistry.Harnessed(), deps.Cache, deps.Projects, deps.Discoverer)
	svc.inventoryCache = deps.InventoryCache
	svc.inventoryLoaded = deps.InventoryCache == nil
	svc.sessions = deps.Sessions
	return svc
}

// NewWithAgents returns an inventory service over a caller-provided adapter
// slice. It is used by focused tests.
func NewWithAgents(agents []agentregistry.HarnessAgent) *Service {
	return newService(agents, nil, nil, nil)
}

func newService(agents []agentregistry.HarnessAgent, cache ports.AgentModelCatalogCache, projects ProjectLookup, discoverer ports.AgentModelDiscoverer) *Service {
	resolverMu := make(map[string]*sync.Mutex, len(agents))
	for _, item := range agents {
		resolverMu[string(item.Harness)] = &sync.Mutex{}
	}
	return &Service{agents: agents, cache: cache, discoverer: discoverer, projects: projects, resolverMu: resolverMu, modelCalls: map[string]*modelCatalogCall{}, inventory: Inventory{
		Supported:  supportedInfos(agents),
		Installed:  []Info{},
		Authorized: []Info{},
	}}
}

// List returns the cached agent inventory without running probes. Installed and
// authorized entries come from the latest persisted or in-process Refresh and
// are advisory: they can be stale by the time a user starts a session, and
// session spawn performs the authoritative binary/runtime validation.
func (s *Service) List(ctx context.Context) (Inventory, error) {
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}
	if err := s.loadCachedInventory(ctx); err != nil {
		// Inventory is advisory cache data. A corrupt row or transient SQLite
		// failure must not hide the adapters supported by this daemon build; the
		// background refresh can repair the persisted snapshot.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Inventory{}, err
		}
	}
	s.mu.RLock()
	inventory := cloneInventory(s.inventory)
	s.mu.RUnlock()
	return s.withSessionUsage(ctx, inventory)
}

// Refresh runs the bounded local binary/auth probes, updates the cached
// inventory, and returns the new snapshot. Refreshes are serialized and
// rate-limited so repeated frontend reloads cannot stampede agent CLIs.
func (s *Service) Refresh(ctx context.Context) (Inventory, error) {
	return s.refresh(ctx, false)
}

// RefreshFresh runs the full inventory probe even when a recent Refresh would
// normally return the rate-limited cache. Use it for explicit user recovery
// checks after an install may have changed PATH-visible binaries.
func (s *Service) RefreshFresh(ctx context.Context) (Inventory, error) {
	return s.refresh(ctx, true)
}

func (s *Service) refresh(ctx context.Context, force bool) (Inventory, error) {
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	// A persisted snapshot makes List useful immediately after restart. A corrupt
	// or temporarily unavailable cache must not prevent a fresh system probe from
	// repairing it, so Refresh deliberately continues when this read fails.
	_ = s.loadCachedInventory(ctx)

	s.mu.RLock()
	if !force && !s.lastRefresh.IsZero() && time.Since(s.lastRefresh) < agentRefreshMinInterval {
		cached := cloneInventory(s.inventory)
		s.mu.RUnlock()
		return s.withSessionUsage(ctx, cached)
	}
	s.mu.RUnlock()

	results := make(chan probeResult, len(s.agents))
	var wg sync.WaitGroup
	for _, item := range s.agents {
		if err := ctx.Err(); err != nil {
			return Inventory{}, err
		}
		wg.Add(1)
		go func(item agentregistry.HarnessAgent) {
			defer wg.Done()
			results <- s.probeAgent(ctx, item)
		}(item)
	}
	wg.Wait()
	close(results)

	supported := make([]Info, 0, len(s.agents))
	installed := make([]Info, 0, len(s.agents))
	authorized := make([]Info, 0, len(s.agents))
	for res := range results {
		supported = append(supported, res.info)
		if res.installed {
			installed = append(installed, res.info)
		}
		if res.authorized {
			authorized = append(authorized, res.info)
		}
	}
	sortInfos(supported)
	sortInfos(installed)
	sortInfos(authorized)
	next := Inventory{
		Supported:  supported,
		Installed:  installed,
		Authorized: authorized,
	}
	if s.inventoryCache != nil {
		data, err := json.Marshal(cachedInventory{Installed: next.Installed, Authorized: next.Authorized})
		if err != nil {
			return next, fmt.Errorf("encode agent inventory cache: %w", err)
		}
		if err := s.inventoryCache.UpsertAgentInventoryCache(ctx, string(data), time.Now().UTC()); err != nil {
			return next, err
		}
	}
	s.mu.Lock()
	s.inventory = cloneInventory(next)
	s.inventoryLoaded = true
	s.lastRefresh = time.Now()
	s.mu.Unlock()
	return s.withSessionUsage(ctx, next)
}

type sessionUsage struct {
	count      int
	lastUsedAt time.Time
}

func (s *Service) withSessionUsage(ctx context.Context, inventory Inventory) (Inventory, error) {
	if s.sessions == nil {
		return inventory, nil
	}
	records, err := s.sessions.ListAllSessions(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("list sessions for agent usage: %w", err)
	}
	usageByAgent := make(map[string]sessionUsage)
	for _, record := range records {
		if record.Harness == "" {
			continue
		}
		usage := usageByAgent[string(record.Harness)]
		usage.count++
		if record.CreatedAt.After(usage.lastUsedAt) {
			usage.lastUsedAt = record.CreatedAt
		}
		usageByAgent[string(record.Harness)] = usage
	}
	decorate := func(infos []Info) {
		for i := range infos {
			usage := usageByAgent[infos[i].ID]
			infos[i].UsageCount = usage.count
			if !usage.lastUsedAt.IsZero() {
				lastUsedAt := usage.lastUsedAt
				infos[i].LastUsedAt = &lastUsedAt
			}
		}
		sortInfosByUsage(infos)
	}
	decorate(inventory.Supported)
	decorate(inventory.Installed)
	decorate(inventory.Authorized)
	return inventory, nil
}

func (s *Service) loadCachedInventory(ctx context.Context) error {
	s.mu.RLock()
	loaded := s.inventoryLoaded
	s.mu.RUnlock()
	if loaded || s.inventoryCache == nil {
		return nil
	}

	s.inventoryLoadMu.Lock()
	defer s.inventoryLoadMu.Unlock()
	s.mu.RLock()
	loaded = s.inventoryLoaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}

	data, _, ok, err := s.inventoryCache.GetAgentInventoryCache(ctx)
	if err != nil {
		return err
	}
	next := Inventory{Supported: supportedInfos(s.agents), Installed: []Info{}, Authorized: []Info{}}
	if ok {
		var cached cachedInventory
		if err := json.Unmarshal([]byte(data), &cached); err != nil {
			return fmt.Errorf("decode agent inventory cache: %w", err)
		}
		next = reconcileCachedInventory(next.Supported, cached)
	}
	s.mu.Lock()
	if !s.inventoryLoaded {
		s.inventory = cloneInventory(next)
		s.inventoryLoaded = true
	}
	s.mu.Unlock()
	return nil
}

func reconcileCachedInventory(supported []Info, cached cachedInventory) Inventory {
	byID := make(map[string]Info, len(supported))
	for _, item := range supported {
		byID[item.ID] = item
	}
	filter := func(items []Info) []Info {
		out := make([]Info, 0, len(items))
		for _, item := range items {
			current, ok := byID[item.ID]
			if !ok {
				continue
			}
			current.AuthStatus = item.AuthStatus
			out = append(out, current)
		}
		sortInfos(out)
		return out
	}
	return Inventory{Supported: cloneInfos(supported), Installed: filter(cached.Installed), Authorized: filter(cached.Authorized)}
}

// Probe runs a fresh bounded binary/auth probe for one agent, bypassing the
// catalog refresh rate limit. It is intended for user-initiated preflight paths
// where a cached negative catalog result may be stale.
func (s *Service) Probe(ctx context.Context, agentID string) (ProbeResult, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	for _, item := range s.agents {
		info := Info{ID: string(item.Harness), Label: item.Manifest.Name}
		if info.Label == "" {
			info.Label = info.ID
		}
		if info.ID != agentID {
			continue
		}
		res := s.probeAgent(ctx, item)
		return ProbeResult{
			Agent:     res.info,
			Supported: true,
			Installed: res.installed,
		}, nil
	}
	return ProbeResult{Agent: Info{ID: agentID}, Supported: false, Installed: false}, nil
}

// Models returns one normalized model catalog. Cached values survive daemon
// restarts; refresh forces a new documented CLI discovery attempt. Discovery
// failures degrade to the last cached catalog or a custom model input.
func (s *Service) Models(ctx context.Context, agentID, projectID string, refresh bool) (ports.AgentModelCatalog, error) {
	mode := modelLoadCached
	if refresh {
		mode = modelLoadRefresh
	}
	return s.coalesceModelLoad(ctx, agentID, projectID, mode)
}

// RevalidateModels rediscovers a cache-first catalog after the normal read path
// marks it old enough to refresh in the background.
func (s *Service) RevalidateModels(ctx context.Context, agentID, projectID string) (ports.AgentModelCatalog, error) {
	return s.coalesceModelLoad(ctx, agentID, projectID, modelLoadRevalidate)
}

func (s *Service) coalesceModelLoad(
	ctx context.Context,
	agentID, projectID string,
	mode modelLoadMode,
) (ports.AgentModelCatalog, error) {
	key := agentID + "\x00" + projectID + "\x00" + strconv.Itoa(int(mode))
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
	call := &modelCatalogCall{done: make(chan struct{})}
	s.modelCalls[key] = call
	s.modelCallMu.Unlock()

	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelCatalogLoadTimeout)
	go func() {
		defer cancel()
		call.catalog, call.err = s.loadModels(loadCtx, agentID, projectID, mode)
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

func (s *Service) loadModels(ctx context.Context, agentID, projectID string, mode modelLoadMode) (ports.AgentModelCatalog, error) {
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
	discovery, err := s.projectDiscoveryContext(ctx, projectID)
	if err != nil {
		return ports.AgentModelCatalog{}, err
	}
	cached, hasCached, err := s.cachedCatalog(ctx, agentID, projectID)
	if err != nil {
		return ports.AgentModelCatalog{}, err
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
		AgentID: agentID, Binary: binary, WorkingDir: discovery.workingDir, Env: discovery.env,
	}
	// Fingerprints the same inputs the discovery run would read, so a change to
	// either the executable or the configuration behind it invalidates the cache.
	version := s.discoverer.CatalogFingerprint(ctx, request)
	if hasCached && mode == modelLoadCached && cached.BinaryVersion == version {
		// A command-backed catalog can drift without the binary or its config
		// changing (a provider adds a model), which no fingerprint can see. Ask
		// cache-first clients to revalidate in the background once the catalog is
		// old enough, so staleness resolves itself instead of waiting for someone
		// to press a refresh button.
		cached.Catalog.RefreshRecommended = catalogNeedsRevalidation(cached.Catalog.ValidatedAt)
		return cached.Catalog, nil
	}

	discovered, discoverErr := s.discoverer.Discover(ctx, request)
	discovered.BinaryVersion = version
	discovered.ValidatedAt = time.Now().UTC()
	discovered.RefreshRecommended = false
	if discoverErr != nil {
		if hasCached && len(cached.Catalog.Models) > len(discovered.Models) {
			cached.Catalog.Stale = true
			cached.Catalog.Warning = discoverErr.Error()
			cached.Catalog.ValidatedAt = time.Now().UTC()
			cached.Catalog.RefreshRecommended = false
			if err := s.saveCatalog(ctx, projectID, cached.Catalog); err != nil {
				cached.Catalog.Warning = appendCacheWarning(cached.Catalog.Warning)
			}
			return cached.Catalog, nil
		}
		if len(discovered.Models) > 0 {
			discovered.Stale = true
			discovered.Warning = discoverErr.Error()
			if err := s.saveCatalog(ctx, projectID, discovered); err != nil {
				discovered.Warning = appendCacheWarning(discovered.Warning)
			}
			return discovered, nil
		}
		if hasCached {
			cached.Catalog.Stale = true
			cached.Catalog.Warning = discoverErr.Error()
			cached.Catalog.ValidatedAt = time.Now().UTC()
			cached.Catalog.RefreshRecommended = false
			if err := s.saveCatalog(ctx, projectID, cached.Catalog); err != nil {
				cached.Catalog.Warning = appendCacheWarning(cached.Catalog.Warning)
			}
			return cached.Catalog, nil
		}
		fallback := s.discoverer.Manual(agentID)
		fallback.BinaryVersion = version
		fallback.ValidatedAt = time.Now().UTC()
		fallback.Stale = true
		fallback.Warning = discoverErr.Error()
		return fallback, nil
	}
	if err := s.saveCatalog(ctx, projectID, discovered); err != nil {
		discovered.Warning = appendCacheWarning(discovered.Warning)
	}
	return discovered, nil
}

func appendCacheWarning(current string) string {
	const next = "Models loaded, but AO could not update the model cache."
	if current == "" {
		return next
	}
	return current + " " + next
}

type projectDiscovery struct {
	workingDir string
	env        map[string]string
}

func (s *Service) projectDiscoveryContext(ctx context.Context, projectID string) (projectDiscovery, error) {
	if projectID == "" || s.projects == nil {
		return projectDiscovery{}, nil
	}
	project, ok, err := s.projects.GetProject(ctx, projectID)
	if err != nil {
		return projectDiscovery{}, apierr.Internal("PROJECT_LOAD_FAILED", "Failed to load project")
	}
	if !ok || !project.ArchivedAt.IsZero() {
		return projectDiscovery{}, apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	}
	return projectDiscovery{workingDir: project.Path, env: project.Config.Env}, nil
}

type decodedCatalog struct {
	Catalog       ports.AgentModelCatalog
	BinaryVersion string
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
	return decodedCatalog{Catalog: catalog, BinaryVersion: record.BinaryVersion}, true, nil
}

func (s *Service) saveCatalog(ctx context.Context, projectID string, catalog ports.AgentModelCatalog) error {
	if s.cache == nil {
		return nil
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode model catalog for %s: %w", catalog.AgentID, err)
	}
	return s.cache.UpsertAgentModelCatalog(ctx, ports.CachedAgentModelCatalog{
		AgentID:       catalog.AgentID,
		ProjectID:     projectID,
		BinaryVersion: catalog.BinaryVersion,
		CatalogJSON:   string(data),
		Source:        catalog.Source,
		FetchedAt:     catalog.FetchedAt,
	})
}

func (s *Service) agent(agentID string) (agentregistry.HarnessAgent, bool) {
	for _, item := range s.agents {
		if string(item.Harness) == agentID {
			return item, true
		}
	}
	return agentregistry.HarnessAgent{}, false
}

func supportedInfos(agents []agentregistry.HarnessAgent) []Info {
	supported := make([]Info, 0, len(agents))
	for _, item := range agents {
		info := Info{ID: string(item.Harness), Label: item.Manifest.Name}
		if info.Label == "" {
			info.Label = info.ID
		}
		supported = append(supported, info)
	}
	sortInfos(supported)
	return supported
}

func cloneInventory(in Inventory) Inventory {
	return Inventory{
		Supported:  cloneInfos(in.Supported),
		Installed:  cloneInfos(in.Installed),
		Authorized: cloneInfos(in.Authorized),
	}
}

func cloneInfos(in []Info) []Info {
	out := make([]Info, len(in))
	copy(out, in)
	return out
}

func (s *Service) probeAgent(ctx context.Context, item agentregistry.HarnessAgent) probeResult {
	info := Info{ID: string(item.Harness), Label: item.Manifest.Name}
	if info.Label == "" {
		info.Label = info.ID
	}
	probeCtx, cancel := context.WithTimeout(ctx, agentInstallProbeTimeout)
	defer cancel()
	resolver, ok := item.Agent.(ports.AgentBinaryResolver)
	if !ok {
		return probeResult{info: info}
	}
	lock := s.resolverMu[info.ID]
	lock.Lock()
	defer lock.Unlock()
	if _, err := resolver.ResolveBinary(probeCtx); err != nil {
		return probeResult{info: info}
	}
	authCtx, authCancel := context.WithTimeout(ctx, agentAuthProbeTimeout)
	defer authCancel()
	info.AuthStatus = authStatus(authCtx, item.Agent)
	return probeResult{info: info, installed: true, authorized: info.AuthStatus == ports.AgentAuthStatusAuthorized}
}

func authStatus(ctx context.Context, a ports.Agent) ports.AgentAuthStatus {
	checker, ok := a.(ports.AgentAuthChecker)
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	status, err := checker.AuthStatus(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ports.AgentAuthStatusUnknown
		}
		return ports.AgentAuthStatusUnknown
	}
	switch status {
	case ports.AgentAuthStatusAuthorized, ports.AgentAuthStatusUnauthorized:
		return status
	default:
		return ports.AgentAuthStatusUnknown
	}
}

func sortInfos(infos []Info) {
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ID < infos[j].ID
	})
}

func sortInfosByUsage(infos []Info) {
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].UsageCount != infos[j].UsageCount {
			return infos[i].UsageCount > infos[j].UsageCount
		}
		var left, right time.Time
		if infos[i].LastUsedAt != nil {
			left = *infos[i].LastUsedAt
		}
		if infos[j].LastUsedAt != nil {
			right = *infos[j].LastUsedAt
		}
		if !left.Equal(right) {
			return left.After(right)
		}
		if infos[i].Label != infos[j].Label {
			return infos[i].Label < infos[j].Label
		}
		return infos[i].ID < infos[j].ID
	})
}
