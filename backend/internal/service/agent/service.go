package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	modelCatalogLoadTimeout = 30 * time.Second
	// How long a cached catalog is trusted before AO asks a cache-first client to
	// revalidate in the background. Long, because rediscovery runs an agent CLI:
	// this covers drift a fingerprint cannot see, not routine correctness.
	modelCatalogTrustWindow = 6 * time.Hour
	// Suppress repeated successful catalog refreshes across rapid daemon restarts.
	// Failed validations are stale and remain eligible for the next startup.
	modelCatalogStartupGuard = 10 * time.Minute
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

// Service owns normalized harness readiness and the unchanged model catalog.
// Consumers share coordinator checks instead of probing adapters directly.
type Service struct {
	agents        []agentregistry.HarnessAgent
	readiness     *readinessCoordinator
	cache         ports.AgentModelCatalogCache
	discoverer    ports.AgentModelDiscoverer
	projects      ProjectLookup
	sessions      SessionUsageLookup
	resolverMu    map[string]*sync.Mutex
	modelCallMu   sync.Mutex
	modelCalls    map[string]*modelCatalogCall
	codexAccounts *codexAccountManager
	codexSwitches CodexAccountSwitchCoordinator
}

// CodexAccountSwitchCoordinator owns global switch execution and recovery.
type CodexAccountSwitchCoordinator interface {
	CodexAccountSwitchInProgress() bool
	StartCodexAccountSwitch(context.Context, ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error)
	RecoverCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, error)
	GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error)
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
	CodexAccountState      CodexAccountStateStore
	CodexOperationGate     ports.CodexOperationGate
	// Clock overrides time.Now for deterministic account-bootstrap retry tests.
	Clock func() time.Time
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
		svc.codexAccounts = newCodexAccountManager(deps.Context, deps.CodexAccountRoot, deps.CodexPendingRoot, deps.CodexSwitchStagingRoot, deps.CodexGlobalHome, deps.CodexAccounts, deps.CodexAccountState, deps.Logger, deps.CodexOperationGate)
		if deps.Clock != nil {
			svc.codexAccounts.now = deps.Clock
		}
	}
	svc.readiness = newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: agents, Factory: agentregistry.Harnessed, Context: deps.Context, Logger: deps.Logger,
		AuthenticationCheck: svc.structuredCodexAuthentication,
	})
	svc.sessions = deps.Sessions
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
	return &Service{agents: agents, readiness: newReadinessCoordinator(readinessCoordinatorConfig{Agents: agents}), cache: cache, discoverer: discoverer, projects: projects, resolverMu: resolverMu, modelCalls: map[string]*modelCatalogCall{}}
}

// WarmModelCatalogs starts a non-blocking, sequential refresh of the Claude
// Code and Muse scopes that already have durable cache records. Unseen scopes
// remain lazy and are discovered only when their picker is first opened.
func (s *Service) WarmModelCatalogs(ctx context.Context) {
	if s.cache == nil || s.discoverer == nil {
		return
	}
	go s.warmModelCatalogs(ctx)
}

func (s *Service) warmModelCatalogs(ctx context.Context) {
	for _, agentID := range []string{"claude-code", "muse"} {
		records, err := s.cache.ListAgentModelCatalogsByAgent(ctx, agentID)
		if err != nil {
			continue
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return
			}
			var cached ports.AgentModelCatalog
			if err := json.Unmarshal([]byte(record.CatalogJSON), &cached); err != nil {
				continue
			}
			if !cached.Stale && !cached.ValidatedAt.IsZero() && time.Since(cached.ValidatedAt) < modelCatalogStartupGuard {
				continue
			}
			_, _ = s.RevalidateModels(ctx, agentID, record.ProjectID)
		}
	}
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
	discovered = applyCustomModelEntryPolicy(discovered, policy)
	discovered.BinaryVersion = version
	if discoverErr != nil {
		if hasCached && len(cached.Catalog.Models) > len(discovered.Models) {
			cached.Catalog.Stale = true
			cached.Catalog.Warning = discoverErr.Error()
			cached.Catalog.RefreshRecommended = true
			if err := s.saveCatalog(ctx, projectID, cached.Catalog); err != nil {
				cached.Catalog.Warning = appendCacheWarning(cached.Catalog.Warning)
			}
			return cached.Catalog, nil
		}
		if len(discovered.Models) > 0 {
			discovered.Stale = true
			discovered.Warning = discoverErr.Error()
			discovered.RefreshRecommended = true
			if err := s.saveCatalog(ctx, projectID, discovered); err != nil {
				discovered.Warning = appendCacheWarning(discovered.Warning)
			}
			return discovered, nil
		}
		if hasCached {
			cached.Catalog.Stale = true
			cached.Catalog.Warning = discoverErr.Error()
			cached.Catalog.RefreshRecommended = true
			if err := s.saveCatalog(ctx, projectID, cached.Catalog); err != nil {
				cached.Catalog.Warning = appendCacheWarning(cached.Catalog.Warning)
			}
			return cached.Catalog, nil
		}
		if shared, ok := s.latestAgentCatalog(ctx, agentID, projectID); ok {
			shared = applyCustomModelEntryPolicy(shared, policy)
			shared.Stale = true
			shared.Warning = discoverErr.Error()
			shared.RefreshRecommended = true
			return shared, nil
		}
		fallback := policy
		fallback.BinaryVersion = version
		fallback.Stale = true
		fallback.Warning = discoverErr.Error()
		fallback.RefreshRecommended = true
		return fallback, nil
	}
	discovered.ValidatedAt = time.Now().UTC()
	discovered.RefreshRecommended = false
	if err := s.saveCatalog(ctx, projectID, discovered); err != nil {
		discovered.Warning = appendCacheWarning(discovered.Warning)
	}
	return discovered, nil
}

// latestAgentCatalog returns a last-known-good catalog from another project as
// a display-only fallback. Discovery remains project-scoped and this result is
// deliberately not persisted under the requested project key.
func (s *Service) latestAgentCatalog(ctx context.Context, agentID, projectID string) (ports.AgentModelCatalog, bool) {
	if s.cache == nil {
		return ports.AgentModelCatalog{}, false
	}
	records, err := s.cache.ListAgentModelCatalogsByAgent(ctx, agentID)
	if err != nil {
		return ports.AgentModelCatalog{}, false
	}
	var best ports.AgentModelCatalog
	var bestAt time.Time
	for _, record := range records {
		if record.ProjectID == projectID {
			continue
		}
		var candidate ports.AgentModelCatalog
		if err := json.Unmarshal([]byte(record.CatalogJSON), &candidate); err != nil || len(candidate.Models) == 0 {
			continue
		}
		at := record.FetchedAt
		if at.IsZero() {
			at = candidate.FetchedAt
		}
		if best.Models == nil || at.After(bestAt) {
			best = candidate
			bestAt = at
		}
	}
	return best, best.Models != nil
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
