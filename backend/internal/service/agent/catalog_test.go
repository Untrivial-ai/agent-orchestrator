package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/modelcatalog"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeAgent struct {
	err   error
	delay time.Duration
}

type fakeAuthAgent struct {
	fakeAgent
	status    ports.AgentAuthStatus
	authErr   error
	authDelay time.Duration
}

type probeTrackingAgent struct {
	fakeAgent
	onProbe func()
}

type concurrentResolverAgent struct {
	fakeAgent
	active  atomic.Int32
	calls   atomic.Int32
	overlap atomic.Bool
}

type countingResolverAgent struct {
	fakeAgent
	calls atomic.Int32
}

type startupPresenceAgent struct {
	fakeAgent
	normalResolveCalls *atomic.Int32
	presenceCalls      *atomic.Int32
	authCalls          *atomic.Int32
}

type mutableInstallAgent struct {
	fakeAgent
	installed atomic.Bool
}

type mutableAuthAgent struct {
	fakeAgent
	status    *ports.AgentAuthStatus
	authCalls atomic.Int32
}

type blockingResolverAgent struct {
	fakeAgent
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type fakeModelCache struct {
	records map[string]ports.CachedAgentModelCatalog
	puts    int
	putErr  error
}

type fakeProjectLookup struct {
	records map[string]domain.ProjectRecord
	gotID   string
	err     error
}

type fakeSessionUsageLookup struct {
	records []domain.SessionRecord
	err     error
}

func (f fakeSessionUsageLookup) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return f.records, f.err
}

type fakeModelDiscoverer struct {
	version                string
	catalog                ports.AgentModelCatalog
	err                    error
	discoverCalls          atomic.Int32
	lastRequest            ports.AgentModelDiscoveryRequest
	fingerprintRequests    atomic.Int32
	lastFingerprintRequest atomic.Pointer[ports.AgentModelDiscoveryRequest]
	delay                  time.Duration
	active                 atomic.Int32
	overlap                atomic.Bool
}

func (f *fakeModelDiscoverer) Discover(_ context.Context, request ports.AgentModelDiscoveryRequest) (ports.AgentModelCatalog, error) {
	f.discoverCalls.Add(1)
	if f.active.Add(1) != 1 {
		f.overlap.Store(true)
	}
	defer f.active.Add(-1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.lastRequest = request
	catalog := f.catalog
	if catalog.AgentID == "" {
		catalog.AgentID = request.AgentID
	}
	if catalog.Models == nil {
		catalog.Models = []ports.AgentModelInfo{}
	}
	if catalog.FetchedAt.IsZero() {
		catalog.FetchedAt = time.Now().UTC()
	}
	return catalog, f.err
}

func (f *fakeModelDiscoverer) CatalogFingerprint(_ context.Context, request ports.AgentModelDiscoveryRequest) string {
	f.fingerprintRequests.Add(1)
	f.lastFingerprintRequest.Store(&request)
	return f.version
}

func (f *fakeModelDiscoverer) Manual(agentID string) ports.AgentModelCatalog {
	return ports.AgentModelCatalog{
		AgentID:          agentID,
		SelectionMode:    ports.ModelSelectionText,
		Models:           []ports.AgentModelInfo{},
		CustomModelEntry: ports.CustomModelEntryDirect,
		AllowCustom:      true,
		Source:           "manual",
		FetchedAt:        time.Now().UTC(),
	}
}

var testModelDiscoverer ports.AgentModelDiscoverer = modelcatalog.Discoverer{}

func successfulModelDiscoverer() *fakeModelDiscoverer {
	return &fakeModelDiscoverer{version: "v1", catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "model-one", Label: "Model One"}},
		AllowCustom:   true,
		Source:        "cli",
	}}
}

func (f *fakeProjectLookup) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	f.gotID = id
	if f.err != nil {
		return domain.ProjectRecord{}, false, f.err
	}
	record, ok := f.records[id]
	return record, ok, nil
}

func (f *fakeModelCache) GetAgentModelCatalog(_ context.Context, agentID, projectID string) (ports.CachedAgentModelCatalog, bool, error) {
	record, ok := f.records[agentID+"\x00"+projectID]
	return record, ok, nil
}

func (f *fakeModelCache) ListAgentModelCatalogsByAgent(_ context.Context, agentID string) ([]ports.CachedAgentModelCatalog, error) {
	records := make([]ports.CachedAgentModelCatalog, 0)
	for _, record := range f.records {
		if record.AgentID == agentID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (f *fakeModelCache) UpsertAgentModelCatalog(_ context.Context, record ports.CachedAgentModelCatalog) error {
	if f.putErr != nil {
		return f.putErr
	}
	if f.records == nil {
		f.records = map[string]ports.CachedAgentModelCatalog{}
	}
	f.records[record.AgentID+"\x00"+record.ProjectID] = record
	f.puts++
	return nil
}

func (f *concurrentResolverAgent) ResolveBinary(ctx context.Context) (string, error) {
	f.calls.Add(1)
	if f.active.Add(1) != 1 {
		f.overlap.Store(true)
	}
	defer f.active.Add(-1)
	select {
	case <-time.After(5 * time.Millisecond):
		return "agent", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *countingResolverAgent) ResolveBinary(ctx context.Context) (string, error) {
	f.calls.Add(1)
	return f.fakeAgent.ResolveBinary(ctx)
}

func (f *mutableInstallAgent) ResolveBinary(context.Context) (string, error) {
	if !f.installed.Load() {
		return "", ports.ErrAgentBinaryNotFound
	}
	return "agent", nil
}

func (f *mutableAuthAgent) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	f.authCalls.Add(1)
	return *f.status, nil
}

func (f *blockingResolverAgent) ResolveBinary(ctx context.Context) (string, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-f.release:
		return "agent", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f fakeAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}

func (f fakeAgent) GetLaunchCommand(ctx context.Context, _ ports.LaunchConfig) ([]string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return []string{"agent"}, nil
}

func (f fakeAgent) ResolveBinary(ctx context.Context) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return "agent", nil
}

func (f probeTrackingAgent) ResolveBinary(ctx context.Context) (string, error) {
	if f.onProbe != nil {
		f.onProbe()
	}
	return f.fakeAgent.ResolveBinary(ctx)
}

func (f fakeAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}

func (f fakeAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error {
	return nil
}

func (f fakeAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}

func (f fakeAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func (f fakeAuthAgent) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if f.authDelay > 0 {
		select {
		case <-time.After(f.authDelay):
		case <-ctx.Done():
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
	}
	return f.status, f.authErr
}

func (f startupPresenceAgent) ResolveBinary(context.Context) (string, error) {
	f.normalResolveCalls.Add(1)
	return "", errors.New("normal resolution must not run during startup")
}

func (f startupPresenceAgent) ResolveBinaryPresence(context.Context) (string, error) {
	f.presenceCalls.Add(1)
	return "/usr/local/bin/muse", nil
}

func (f startupPresenceAgent) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	f.authCalls.Add(1)
	return ports.AgentAuthStatusAuthorized, nil
}

func TestListReturnsInitialSupportedInventoryWithoutProbing(t *testing.T) {
	probed := false
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{
			Harness: domain.AgentHarness("codex"),
			Manifest: adapters.Manifest{
				ID:   "codex",
				Name: "Codex",
			},
			Agent: probeTrackingAgent{onProbe: func() { probed = true }},
		},
	})

	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if probed {
		t.Fatal("List ran a live probe")
	}
	if len(got.Supported) != 1 || got.Supported[0].ID != "codex" {
		t.Fatalf("supported = %#v, want codex", got.Supported)
	}
	if len(got.Installed) != 0 || len(got.Authorized) != 0 {
		t.Fatalf("inventory = %#v, want only supported entries before refresh", got)
	}
	if got.Installed == nil {
		t.Fatal("Installed = nil, want empty slice")
	}
	if got.Authorized == nil {
		t.Fatal("Authorized = nil, want empty slice")
	}
}

func TestFindInstalledBinary_ResolvesWithoutRefreshingInventory(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		harnessAgent("missing", "Missing", ports.ErrAgentBinaryNotFound),
		harnessAgent("codex", "Codex", nil),
	})

	got, ok := svc.FindInstalledBinary(context.Background())
	if !ok {
		t.Fatal("FindInstalledBinary() found no binary, want Codex")
	}
	if got.ID != "codex" || got.Label != "Codex" {
		t.Fatalf("FindInstalledBinary() = %#v, want Codex", got)
	}

	inventory, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(inventory.Installed) != 1 || inventory.Installed[0].ID != "codex" || len(inventory.Authorized) != 0 {
		t.Fatalf("inventory = %#v, want coordinator installation snapshot without auth", inventory)
	}
}

func TestFindInstalledBinaryUsesPresenceResolverWithoutAuthOrNormalResolution(t *testing.T) {
	var normalResolveCalls atomic.Int32
	var presenceCalls atomic.Int32
	var authCalls atomic.Int32
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{
			Harness:  domain.AgentHarness("muse"),
			Manifest: adapters.Manifest{ID: "muse", Name: "Muse"},
			Agent: startupPresenceAgent{
				fakeAgent:          fakeAgent{},
				normalResolveCalls: &normalResolveCalls,
				presenceCalls:      &presenceCalls,
				authCalls:          &authCalls,
			},
		},
	})

	got, ok := svc.FindInstalledBinary(context.Background())
	if !ok || got.ID != "muse" {
		t.Fatalf("FindInstalledBinary() = (%#v, %v), want Muse", got, ok)
	}
	if got := presenceCalls.Load(); got != 1 {
		t.Fatalf("presence resolver calls = %d, want 1", got)
	}
	if got := normalResolveCalls.Load(); got != 0 {
		t.Fatalf("normal resolver calls = %d, want 0", got)
	}
	if got := authCalls.Load(); got != 0 {
		t.Fatalf("auth calls = %d, want 0", got)
	}
}

func TestRefreshUsesNormalResolutionInsteadOfStartupPresenceShortcut(t *testing.T) {
	var normalResolveCalls atomic.Int32
	var presenceCalls atomic.Int32
	var authCalls atomic.Int32
	svc := NewWithAgents([]agentregistry.HarnessAgent{{
		Harness:  domain.AgentHarness("muse"),
		Manifest: adapters.Manifest{ID: "muse", Name: "Muse"},
		Agent: startupPresenceAgent{
			fakeAgent:          fakeAgent{},
			normalResolveCalls: &normalResolveCalls,
			presenceCalls:      &presenceCalls,
			authCalls:          &authCalls,
		},
	}})

	if _, err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := normalResolveCalls.Load(); got != 1 {
		t.Fatalf("normal resolver calls = %d, want 1", got)
	}
	if got := presenceCalls.Load(); got != 0 {
		t.Fatalf("presence resolver calls = %d, want startup-only", got)
	}
	if got := authCalls.Load(); got != 0 {
		t.Fatalf("auth calls = %d, want none after install resolution failure", got)
	}
}

func TestDefaultCatalogDisplaysPrimeAgent(t *testing.T) {
	got, err := New().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range got.Supported {
		if info.ID == "prime-agent" {
			if info.Label != "Prime Agent" {
				t.Fatalf("prime-agent label = %q, want Prime Agent", info.Label)
			}
			return
		}
	}
	t.Fatal("default catalog does not contain prime-agent")
}

func TestRefreshReportsInstalledAgentsAndIgnoresDetectorErrors(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		harnessAgent("codex", "Codex", nil),
		harnessAgent("missing", "Missing", ports.ErrAgentBinaryNotFound),
		harnessAgent("broken", "Broken", errors.New("unexpected detector failure")),
	})

	got, err := svc.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(got.Supported) != 3 {
		t.Fatalf("supported = %#v, want 3 agents", got.Supported)
	}
	if len(got.Installed) != 1 || got.Installed[0].ID != "codex" {
		t.Fatalf("installed = %#v, want only codex", got.Installed)
	}
}

func TestRefreshReportsAuthorizedInstalledAgents(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		harnessAuthAgent("codex", "Codex", ports.AgentAuthStatusAuthorized, nil),
		harnessAuthAgent("claude-code", "Claude Code", ports.AgentAuthStatusUnauthorized, nil),
		harnessAgent("opencode", "OpenCode", nil),
		harnessAuthAgent("broken-auth", "Broken Auth", ports.AgentAuthStatusAuthorized, errors.New("probe failed")),
	})

	got, err := svc.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(got.Supported) != 4 || len(got.Installed) != 4 {
		t.Fatalf("inventory = %#v, want supported=4 installed=4", got)
	}
	if len(got.Authorized) != 1 || got.Authorized[0].ID != "codex" {
		t.Fatalf("authorized = %#v, want only codex", got.Authorized)
	}

	byID := map[string]Info{}
	for _, info := range got.Installed {
		byID[info.ID] = info
	}
	if byID["codex"].AuthStatus != ports.AgentAuthStatusAuthorized {
		t.Fatalf("codex authStatus = %q", byID["codex"].AuthStatus)
	}
	if byID["claude-code"].AuthStatus != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("claude-code authStatus = %q", byID["claude-code"].AuthStatus)
	}
	if byID["opencode"].AuthStatus != ports.AgentAuthStatusUnknown {
		t.Fatalf("opencode authStatus = %q", byID["opencode"].AuthStatus)
	}
	if byID["broken-auth"].AuthStatus != ports.AgentAuthStatusUnknown {
		t.Fatalf("broken-auth authStatus = %q", byID["broken-auth"].AuthStatus)
	}
}

func TestRefreshDoesNotWaitForSlowAgentProbe(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		harnessAgent("codex", "Codex", nil),
		{
			Harness: domain.AgentHarness("slow"),
			Manifest: adapters.Manifest{
				ID:   "slow",
				Name: "Slow",
			},
			Agent: fakeAgent{delay: time.Minute},
		},
	})
	svc.readiness.installTimeout = 20 * time.Millisecond

	start := time.Now()
	got, err := svc.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("List took %s, want bounded by slow probe timeout", elapsed)
	}
	if len(got.Supported) != 2 {
		t.Fatalf("supported = %#v, want both agents", got.Supported)
	}
	if len(got.Installed) != 1 || got.Installed[0].ID != "codex" {
		t.Fatalf("installed = %#v, want only codex", got.Installed)
	}
}

func TestListRanksAgentsByRetainedSessionUsage(t *testing.T) {
	older := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		harnessAgent("claude-code", "Claude Code", nil),
		harnessAgent("codex", "Codex", nil),
		harnessAgent("goose", "Goose", nil),
	})
	svc.sessions = fakeSessionUsageLookup{records: []domain.SessionRecord{
		{Harness: domain.AgentHarness("claude-code"), CreatedAt: newer},
		{Harness: domain.AgentHarness("codex"), CreatedAt: older},
		{Harness: domain.AgentHarness("codex"), CreatedAt: newer},
	}}

	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ids := []string{got.Supported[0].ID, got.Supported[1].ID, got.Supported[2].ID}; !reflect.DeepEqual(ids, []string{"codex", "claude-code", "goose"}) {
		t.Fatalf("supported order = %v, want frequency then unused fallback", ids)
	}
	if got.Supported[0].UsageCount != 2 || got.Supported[0].LastUsedAt == nil || !got.Supported[0].LastUsedAt.Equal(newer) {
		t.Fatalf("codex usage = %#v, want count 2 and latest use %s", got.Supported[0], newer)
	}
	if got.Supported[2].UsageCount != 0 || got.Supported[2].LastUsedAt != nil {
		t.Fatalf("unused agent usage = %#v, want empty usage metadata", got.Supported[2])
	}
}

func TestListReturnsSessionUsageReadFailure(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{harnessAgent("codex", "Codex", nil)})
	svc.sessions = fakeSessionUsageLookup{err: errors.New("database unavailable")}

	_, err := svc.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list sessions for agent usage") {
		t.Fatalf("List error = %v, want session usage context", err)
	}
}

func TestRefreshUsesSeparateTimeoutForAuthProbe(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{
			Harness: domain.AgentHarness("claude-code"),
			Manifest: adapters.Manifest{
				ID:   "claude-code",
				Name: "Claude Code",
			},
			Agent: fakeAuthAgent{
				fakeAgent: fakeAgent{},
				status:    ports.AgentAuthStatusAuthorized,
				authDelay: 75 * time.Millisecond,
			},
		},
	})
	svc.readiness.installTimeout = 20 * time.Millisecond
	svc.readiness.authTimeout = 200 * time.Millisecond

	got, err := svc.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(got.Authorized) != 1 || got.Authorized[0].ID != "claude-code" {
		t.Fatalf("authorized = %#v, want claude-code", got.Authorized)
	}
}

func TestRefreshForcesFreshReadinessChecks(t *testing.T) {
	probes := 0
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{
			Harness: domain.AgentHarness("codex"),
			Manifest: adapters.Manifest{
				ID:   "codex",
				Name: "Codex",
			},
			Agent: probeTrackingAgent{onProbe: func() { probes++ }},
		},
	})

	if _, err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if _, err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if probes != 2 {
		t.Fatalf("probes = %d, want 2", probes)
	}
}

func TestRefreshFreshDetectsManualInstallWithoutInvalidation(t *testing.T) {
	agent := &mutableInstallAgent{}
	svc := NewWithAgents([]agentregistry.HarnessAgent{{
		Harness: domain.AgentHarness("codex"),
		Manifest: adapters.Manifest{
			ID:   "codex",
			Name: "Codex",
		},
		Agent: agent,
	}})

	initial, err := svc.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(initial.Installed) != 0 {
		t.Fatalf("initial Installed = %#v, want empty", initial.Installed)
	}

	agent.installed.Store(true)
	fresh, err := svc.RefreshFresh(context.Background())
	if err != nil {
		t.Fatalf("RefreshFresh: %v", err)
	}
	if len(fresh.Installed) != 1 || fresh.Installed[0].ID != "codex" {
		t.Fatalf("fresh Installed = %#v, want codex", fresh.Installed)
	}
}

func TestProbeBypassesRefreshRateLimitForOneAgent(t *testing.T) {
	probes := 0
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{
			Harness: domain.AgentHarness("codex"),
			Manifest: adapters.Manifest{
				ID:   "codex",
				Name: "Codex",
			},
			Agent: probeTrackingAgent{fakeAgent: fakeAgent{}, onProbe: func() { probes++ }},
		},
		harnessAgent("missing", "Missing", ports.ErrAgentBinaryNotFound),
	})

	if _, err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, err := svc.Probe(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !got.Supported || !got.Installed || got.Agent.ID != "codex" {
		t.Fatalf("Probe = %#v, want supported installed codex", got)
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want launch probe to reuse a launch-fresh snapshot", probes)
	}
	listed, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List after Probe: %v", err)
	}
	if len(listed.Installed) != 1 || listed.Installed[0].ID != "codex" {
		t.Fatalf("installed after Probe = %#v, want codex", listed.Installed)
	}
}

func TestProbeRechecksCachedUnauthorizedAuthentication(t *testing.T) {
	status := ports.AgentAuthStatusUnauthorized
	agent := &mutableAuthAgent{status: &status}
	svc := NewWithAgents([]agentregistry.HarnessAgent{{
		Harness:  domain.AgentHarness("codex"),
		Manifest: adapters.Manifest{ID: "codex", Name: "Codex"},
		Agent:    agent,
	}})

	initial, err := svc.EnsureAgentReadiness(context.Background(), "codex", domain.AgentReadinessPurposeLaunch)
	if err != nil {
		t.Fatalf("EnsureAgentReadiness: %v", err)
	}
	if initial.Authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("initial authentication = %q, want unauthorized", initial.Authentication.State)
	}

	status = ports.AgentAuthStatusAuthorized
	got, err := svc.Probe(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.Agent.AuthStatus != ports.AgentAuthStatusAuthorized {
		t.Fatalf("Probe authStatus = %q, want authorized", got.Agent.AuthStatus)
	}
	if calls := agent.authCalls.Load(); calls != 2 {
		t.Fatalf("authentication checks = %d, want fresh check after cached unauthorized", calls)
	}
}

func TestProbeReportsUnsupportedAndMissingAgent(t *testing.T) {
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		harnessAgent("missing", "Missing", ports.ErrAgentBinaryNotFound),
	})

	missing, err := svc.Probe(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Probe missing: %v", err)
	}
	if !missing.Supported || missing.Installed {
		t.Fatalf("Probe missing = %#v, want supported but not installed", missing)
	}

	unsupported, err := svc.Probe(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("Probe unknown: %v", err)
	}
	if unsupported.Supported || unsupported.Installed || unsupported.Agent.ID != "unknown" {
		t.Fatalf("Probe unknown = %#v, want unsupported unknown", unsupported)
	}
}

func TestModelsCachesDiscoveredCatalogByProject(t *testing.T) {
	cache := &fakeModelCache{}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("codex", "Codex", nil),
	}, cache, nil, successfulModelDiscoverer())

	first, err := svc.Models(context.Background(), "codex", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.SelectionMode != ports.ModelSelectionCatalog || len(first.Models) == 0 || !first.AllowCustom {
		t.Fatalf("first catalog = %#v", first)
	}
	if cache.puts != 1 {
		t.Fatalf("cache puts = %d, want 1", cache.puts)
	}

	second, err := svc.Models(context.Background(), "codex", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if cache.puts != 1 {
		t.Fatalf("cache puts after hit = %d, want 1", cache.puts)
	}
	if second.Source != first.Source || len(second.Models) != len(first.Models) {
		t.Fatalf("cached catalog = %#v, want %#v", second, first)
	}
}

func TestModelsReusesCacheWhileBinaryVersionMatches(t *testing.T) {
	cache := &fakeModelCache{}
	agent := &countingResolverAgent{}
	discoverer := &fakeModelDiscoverer{version: "v1", catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "model-one"}},
		AllowCustom:   true,
		Source:        "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{{
		Harness:  domain.AgentHarness("codex"),
		Manifest: adapters.Manifest{ID: "codex", Name: "Codex"},
		Agent:    agent,
	}}, cache, nil, discoverer)

	_, err := svc.Models(context.Background(), "codex", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	record := cache.records["codex\x00proj-1"]
	var old ports.AgentModelCatalog
	if err := json.Unmarshal([]byte(record.CatalogJSON), &old); err != nil {
		t.Fatal(err)
	}
	old.FetchedAt = time.Now().Add(-30 * 24 * time.Hour)
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	record.CatalogJSON = string(data)
	cache.records["codex\x00proj-1"] = record

	resolveCalls := agent.calls.Load()
	cached, err := svc.Models(context.Background(), "codex", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls.Load() != resolveCalls+1 {
		t.Fatalf("cache hit resolve calls=%d want=%d", agent.calls.Load(), resolveCalls+1)
	}
	if discoverer.discoverCalls.Load() != 1 {
		t.Fatalf("discovery calls=%d, want cached result", discoverer.discoverCalls.Load())
	}
	if !cached.FetchedAt.Equal(old.FetchedAt) {
		t.Fatalf("FetchedAt=%s, want unchanged cached timestamp %s", cached.FetchedAt, old.FetchedAt)
	}
}

func TestModelsRediscoversWhenBinaryVersionChanges(t *testing.T) {
	cache := &fakeModelCache{}
	discoverer := &fakeModelDiscoverer{version: "v1", catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "model-one"}},
		AllowCustom:   true,
		Source:        "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{{
		Harness:  domain.AgentHarness("codex"),
		Manifest: adapters.Manifest{ID: "codex", Name: "Codex"},
		Agent:    &countingResolverAgent{},
	}}, cache, nil, discoverer)

	first, err := svc.Models(context.Background(), "codex", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	discoverer.version = "v2"
	discoverer.catalog.Models = []ports.AgentModelInfo{{ID: "model-two"}}
	discoverer.catalog.FetchedAt = time.Time{}
	got, err := svc.Models(context.Background(), "codex", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if discoverer.discoverCalls.Load() != 2 || len(got.Models) != 1 || got.Models[0].ID != "model-two" {
		t.Fatalf("catalog=%#v calls=%d, want rediscovery for v2", got, discoverer.discoverCalls.Load())
	}
	if got.BinaryVersion != "v2" || !got.FetchedAt.After(first.FetchedAt) {
		t.Fatalf("catalog=%#v, want refreshed v2 catalog", got)
	}
}

func TestModelsLeaderCancellationDoesNotCancelCoalescedLoad(t *testing.T) {
	agent := &blockingResolverAgent{started: make(chan struct{}), release: make(chan struct{})}
	svc := newService([]agentregistry.HarnessAgent{{
		Harness:  domain.AgentHarness("codex"),
		Manifest: adapters.Manifest{ID: "codex", Name: "Codex"},
		Agent:    agent,
	}}, nil, nil, successfulModelDiscoverer())

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := svc.Models(leaderCtx, "codex", "proj-1", true)
		leaderErr <- err
	}()
	<-agent.started

	waiterResult := make(chan ports.AgentModelCatalog, 1)
	waiterErr := make(chan error, 1)
	go func() {
		catalog, err := svc.Models(context.Background(), "codex", "proj-1", true)
		waiterResult <- catalog
		waiterErr <- err
	}()
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context canceled", err)
	}
	close(agent.release)
	if err := <-waiterErr; err != nil {
		t.Fatalf("coalesced waiter: %v", err)
	}
	if got := <-waiterResult; got.AgentID != "codex" || len(got.Models) == 0 {
		t.Fatalf("coalesced waiter catalog = %#v", got)
	}
}

func TestModelsResolvesProjectWorkingDirectory(t *testing.T) {
	projects := &fakeProjectLookup{records: map[string]domain.ProjectRecord{
		"proj-1": {ID: "proj-1", Path: "/work/project"},
	}}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("codex", "Codex", nil),
	}, nil, projects, testModelDiscoverer)

	if _, err := svc.Models(context.Background(), "codex", "proj-1", false); err != nil {
		t.Fatal(err)
	}
	if projects.gotID != "proj-1" {
		t.Fatalf("project lookup id = %q, want proj-1", projects.gotID)
	}
}

func TestModelsPassesProjectEnvironmentToDiscovery(t *testing.T) {
	projects := &fakeProjectLookup{records: map[string]domain.ProjectRecord{
		"proj-1": {
			ID:   "proj-1",
			Path: "/work/project",
			Config: domain.ProjectConfig{Env: map[string]string{
				"OPENCODE_CONFIG": "/work/project/opencode.json",
			}},
		},
	}}
	discoverer := &fakeModelDiscoverer{catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "project/model"}},
		Source:        "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("opencode", "OpenCode", nil),
	}, nil, projects, discoverer)

	got, err := svc.Models(context.Background(), "opencode", "proj-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || discoverer.lastRequest.WorkingDir != "/work/project" ||
		discoverer.lastRequest.Env["OPENCODE_CONFIG"] != "/work/project/opencode.json" {
		t.Fatalf("catalog=%#v request=%#v, want project-scoped discovery", got, discoverer.lastRequest)
	}
}

func TestModelsRejectsUnknownProjectScope(t *testing.T) {
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("codex", "Codex", nil),
	}, nil, &fakeProjectLookup{records: map[string]domain.ProjectRecord{}}, testModelDiscoverer)

	if _, err := svc.Models(context.Background(), "codex", "missing", false); err == nil {
		t.Fatal("Models: want unknown-project error")
	}
}

func TestModelsUsesCapabilityAwareFallbackWhenDiscoveryCannotRun(t *testing.T) {
	for _, tc := range []struct {
		agent          string
		wantEntryMode  ports.CustomModelEntryMode
		wantSelection  ports.ModelSelectionMode
		wantAllowInput bool
	}{
		{agent: "qwen", wantEntryMode: ports.CustomModelEntryDirect, wantSelection: ports.ModelSelectionText, wantAllowInput: true},
		{agent: "opencode", wantEntryMode: ports.CustomModelEntryDirect, wantSelection: ports.ModelSelectionText, wantAllowInput: true},
		{agent: "grok", wantEntryMode: ports.CustomModelEntryDirect, wantSelection: ports.ModelSelectionText, wantAllowInput: true},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			emptyHome := t.TempDir()
			t.Setenv("HOME", emptyHome)
			t.Setenv("XDG_CONFIG_HOME", emptyHome)
			svc := NewWithAgents([]agentregistry.HarnessAgent{
				harnessAgent(tc.agent, tc.agent, ports.ErrAgentBinaryNotFound),
			})
			svc.discoverer = testModelDiscoverer
			got, err := svc.Models(context.Background(), tc.agent, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if got.SelectionMode != tc.wantSelection || got.CustomModelEntry != tc.wantEntryMode || got.AllowCustom != tc.wantAllowInput || got.Source != "manual" || len(got.Models) != 0 {
				t.Fatalf("catalog = %#v, want capability-aware fallback", got)
			}
			if tc.agent != "qwen" && (!got.Stale || got.Warning == "") {
				t.Fatalf("catalog = %#v, want discovery warning on manual fallback", got)
			}
		})
	}
}

func TestModelsNormalizesCustomEntryPolicyInOldCache(t *testing.T) {
	for _, tc := range []struct {
		agent          string
		wantEntryMode  ports.CustomModelEntryMode
		wantAllowInput bool
	}{
		{agent: "codex", wantEntryMode: ports.CustomModelEntryDirect, wantAllowInput: true},
		{agent: "opencode", wantEntryMode: ports.CustomModelEntryDirect, wantAllowInput: true},
		{agent: "grok", wantEntryMode: ports.CustomModelEntryDirect, wantAllowInput: true},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			oldCatalog := map[string]any{
				"agentId": tc.agent, "selectionMode": "catalog", "models": []map[string]any{{"id": "cached-model", "label": "Cached model"}},
				"allowCustom": true, "source": "cli", "fetchedAt": time.Now().UTC(), "stale": false,
			}
			data, err := json.Marshal(oldCatalog)
			if err != nil {
				t.Fatal(err)
			}
			cache := &fakeModelCache{records: map[string]ports.CachedAgentModelCatalog{
				tc.agent + "\x00": {AgentID: tc.agent, CatalogJSON: string(data)},
			}}
			svc := newService([]agentregistry.HarnessAgent{harnessAgent(tc.agent, tc.agent, nil)}, cache, nil, testModelDiscoverer)

			got, err := svc.Models(context.Background(), tc.agent, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if got.CustomModelEntry != tc.wantEntryMode || got.AllowCustom != tc.wantAllowInput {
				t.Fatalf("catalog = %#v, want entry mode %q allowCustom=%v", got, tc.wantEntryMode, tc.wantAllowInput)
			}
		})
	}
}

func TestModelsSerializeBinaryResolutionPerAdapter(t *testing.T) {
	agent := &concurrentResolverAgent{}
	svc := newService([]agentregistry.HarnessAgent{{
		Harness:  domain.AgentHarness("codex"),
		Manifest: adapters.Manifest{ID: "codex", Name: "Codex"},
		Agent:    agent,
	}}, nil, nil, testModelDiscoverer)

	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Models(context.Background(), "codex", "", true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if agent.overlap.Load() {
		t.Fatal("ResolveBinary calls overlapped for the same adapter")
	}
	if got := agent.calls.Load(); got != 1 {
		t.Fatalf("ResolveBinary calls = %d, want one coalesced model load", got)
	}
}

func TestModelsReturnsDiscoveredCatalogWhenCacheWriteFails(t *testing.T) {
	cache := &fakeModelCache{putErr: errors.New("database unavailable")}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("codex", "Codex", nil),
	}, cache, nil, successfulModelDiscoverer())

	got, err := svc.Models(context.Background(), "codex", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) == 0 || got.SelectionMode != ports.ModelSelectionCatalog {
		t.Fatalf("catalog = %#v, want discovered models", got)
	}
	if !strings.Contains(got.Warning, "could not update the model cache") {
		t.Fatalf("warning = %q, want cache warning", got.Warning)
	}
}

func TestModelsKeepsFullerCacheWhenRefreshReturnsPartialCatalog(t *testing.T) {
	lastSuccessfulValidation := time.Now().Add(-modelCatalogTrustWindow - time.Hour)
	cached := ports.AgentModelCatalog{
		AgentID:       "opencode",
		SelectionMode: ports.ModelSelectionCatalog,
		Models: []ports.AgentModelInfo{
			{ID: "configured/model", Label: "Configured"},
			{ID: "cli/one", Label: "CLI one"},
			{ID: "cli/two", Label: "CLI two"},
		},
		AllowCustom: true,
		Source:      "cli",
		FetchedAt:   lastSuccessfulValidation,
		ValidatedAt: lastSuccessfulValidation,
	}
	data, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	cache := &fakeModelCache{records: map[string]ports.CachedAgentModelCatalog{
		"opencode\x00": {AgentID: "opencode", CatalogJSON: string(data)},
	}}
	discoverer := &fakeModelDiscoverer{
		catalog: ports.AgentModelCatalog{
			SelectionMode: ports.ModelSelectionCatalog,
			Models:        []ports.AgentModelInfo{{ID: "configured/model"}},
			AllowCustom:   true,
			Source:        "cli",
		},
		err: errors.New("transient model discovery failure"),
	}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("opencode", "OpenCode", nil),
	}, cache, nil, discoverer)

	got, err := svc.Models(context.Background(), "opencode", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 3 || !got.Stale || got.Warning == "" {
		t.Fatalf("catalog = %#v, want fuller stale cache", got)
	}
	if !got.ValidatedAt.Equal(lastSuccessfulValidation) {
		t.Fatalf("validatedAt = %s, want last successful validation %s", got.ValidatedAt, lastSuccessfulValidation)
	}
	if !got.RefreshRecommended {
		t.Fatal("failed refresh did not remain eligible for automatic retry")
	}
}

func TestModelsUsesNewestAgentWideCacheWhenCurrentProjectDiscoveryFails(t *testing.T) {
	older := cachedModelRecord(t, "cursor", "project-a", time.Now().Add(-2*time.Hour), false)
	newer := cachedModelRecord(t, "cursor", "project-b", time.Now().Add(-time.Hour), false)
	var newerCatalog ports.AgentModelCatalog
	if err := json.Unmarshal([]byte(newer.CatalogJSON), &newerCatalog); err != nil {
		t.Fatal(err)
	}
	newerCatalog.Models = []ports.AgentModelInfo{{ID: "cursor/latest", Label: "Latest"}}
	newerData, err := json.Marshal(newerCatalog)
	if err != nil {
		t.Fatal(err)
	}
	newer.CatalogJSON = string(newerData)

	cache := &fakeModelCache{records: map[string]ports.CachedAgentModelCatalog{
		"cursor\x00project-a": older,
		"cursor\x00project-b": newer,
	}}
	discoverer := &fakeModelDiscoverer{err: errors.New("cursor model discovery timed out after 20s")}
	projects := &fakeProjectLookup{records: map[string]domain.ProjectRecord{
		"project-c": {ID: "project-c", Path: "/work/project-c"},
	}}
	svc := newService([]agentregistry.HarnessAgent{harnessAgent("cursor", "Cursor", nil)}, cache, projects, discoverer)

	got, err := svc.Models(context.Background(), "cursor", "project-c", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "cursor/latest" || !got.Stale {
		t.Fatalf("catalog = %#v, want newest agent-wide cache marked stale", got)
	}
	if !strings.Contains(got.Warning, "timed out") {
		t.Fatalf("warning = %q, want discovery failure", got.Warning)
	}
	if _, exists := cache.records["cursor\x00project-c"]; exists {
		t.Fatal("agent-wide fallback must not be persisted as project-specific discovery")
	}
}

func harnessAgent(id, label string, err error) agentregistry.HarnessAgent {
	return agentregistry.HarnessAgent{
		Harness: domain.AgentHarness(id),
		Manifest: adapters.Manifest{
			ID:   id,
			Name: label,
		},
		Agent: fakeAgent{err: err},
	}
}

func harnessAuthAgent(id, label string, status ports.AgentAuthStatus, err error) agentregistry.HarnessAgent {
	return agentregistry.HarnessAgent{
		Harness: domain.AgentHarness(id),
		Manifest: adapters.Manifest{
			ID:   id,
			Name: label,
		},
		Agent: fakeAuthAgent{fakeAgent: fakeAgent{}, status: status, authErr: err},
	}
}

func TestResolveAgentBinaryUsesRequestedAdapter(t *testing.T) {
	t.Parallel()

	svc := NewWithAgents([]agentregistry.HarnessAgent{harnessAgent("codex", "Codex", nil)})

	path, err := svc.ResolveAgentBinary(context.Background(), "codex")
	if err != nil {
		t.Fatalf("ResolveAgentBinary(codex): %v", err)
	}
	if path != "agent" {
		t.Fatalf("ResolveAgentBinary(codex) = %q, want adapter-resolved path", path)
	}
}

func TestModelsFingerprintsTheSameInputsDiscoveryReads(t *testing.T) {
	projects := &fakeProjectLookup{records: map[string]domain.ProjectRecord{
		"proj-1": {
			ID:     "proj-1",
			Path:   "/work/project",
			Config: domain.ProjectConfig{Env: map[string]string{"ANTHROPIC_MODEL": "opus"}},
		},
	}}
	discoverer := &fakeModelDiscoverer{version: "v1", catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "opus"}},
		Source:        "official-aliases",
	}}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("claude-code", "Claude Code", nil),
	}, &fakeModelCache{}, projects, discoverer)

	if _, err := svc.Models(context.Background(), "claude-code", "proj-1", false); err != nil {
		t.Fatal(err)
	}
	// The cache decision runs before discovery, so it must be able to see the
	// configuration discovery would read. Fingerprinting a narrower input than
	// Discover consumes is what let a settings edit go unnoticed.
	fingerprinted := discoverer.lastFingerprintRequest.Load()
	if fingerprinted == nil {
		t.Fatal("catalog fingerprint was never requested")
	}
	if !reflect.DeepEqual(*fingerprinted, discoverer.lastRequest) {
		t.Fatalf("fingerprint request = %#v, want the discovery request %#v", *fingerprinted, discoverer.lastRequest)
	}
	if fingerprinted.WorkingDir != "/work/project" || fingerprinted.Env["ANTHROPIC_MODEL"] != "opus" {
		t.Fatalf("fingerprint request = %#v, want the project working dir and env", *fingerprinted)
	}
}

func TestModelsAsksClientsToRevalidateAnAgedCatalog(t *testing.T) {
	cache := &fakeModelCache{}
	discoverer := &fakeModelDiscoverer{version: "v1", catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "model-one"}},
		Source:        "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("opencode", "OpenCode", nil),
	}, cache, nil, discoverer)

	fresh, err := svc.Models(context.Background(), "opencode", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	// Just discovered: nothing to revalidate, so the client must not be nudged.
	if fresh.RefreshRecommended {
		t.Fatal("a freshly discovered catalog asked for revalidation")
	}

	record := cache.records["opencode\x00proj-1"]
	var aged ports.AgentModelCatalog
	if err := json.Unmarshal([]byte(record.CatalogJSON), &aged); err != nil {
		t.Fatal(err)
	}
	aged.ValidatedAt = time.Now().Add(-modelCatalogTrustWindow - time.Hour)
	data, err := json.Marshal(aged)
	if err != nil {
		t.Fatal(err)
	}
	record.CatalogJSON = string(data)
	cache.records["opencode\x00proj-1"] = record

	// A CLI-backed catalog can drift with no change to the binary or its config,
	// so an aged cache hit is what replaces the manual "Refresh models" button.
	stale, err := svc.Models(context.Background(), "opencode", "proj-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.RefreshRecommended {
		t.Fatalf("catalog validated %s ago did not ask for revalidation", time.Since(aged.ValidatedAt))
	}
	if discoverer.discoverCalls.Load() != 1 {
		t.Fatalf("discovery calls = %d, want the cached catalog served immediately", discoverer.discoverCalls.Load())
	}
}

func TestRevalidateModelsRediscoversAnAgedCatalog(t *testing.T) {
	cache := &fakeModelCache{}
	discoverer := &fakeModelDiscoverer{version: "v1", catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog,
		Models:        []ports.AgentModelInfo{{ID: "model-one"}},
		Source:        "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("opencode", "OpenCode", nil),
	}, cache, nil, discoverer)

	if _, err := svc.Models(context.Background(), "opencode", "proj-1", false); err != nil {
		t.Fatal(err)
	}
	discoverer.catalog.Models = []ports.AgentModelInfo{{ID: "model-two"}}
	discoverer.catalog.FetchedAt = time.Time{}

	got, err := svc.RevalidateModels(context.Background(), "opencode", "proj-1")
	if err != nil {
		t.Fatal(err)
	}
	if discoverer.discoverCalls.Load() != 2 {
		t.Fatalf("discovery calls = %d, want initial load plus revalidation", discoverer.discoverCalls.Load())
	}
	if len(got.Models) != 1 || got.Models[0].ID != "model-two" {
		t.Fatalf("revalidated catalog = %#v, want model-two", got)
	}
	if got.RefreshRecommended {
		t.Fatal("revalidated catalog still recommends refresh")
	}
}

func cachedModelRecord(t *testing.T, agentID, projectID string, validatedAt time.Time, stale bool) ports.CachedAgentModelCatalog {
	t.Helper()
	catalog := ports.AgentModelCatalog{
		AgentID: agentID, SelectionMode: ports.ModelSelectionCatalog,
		Models: []ports.AgentModelInfo{{ID: "cached-model"}}, AllowCustom: true,
		Source: "cli", FetchedAt: validatedAt, ValidatedAt: validatedAt, Stale: stale,
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return ports.CachedAgentModelCatalog{AgentID: agentID, ProjectID: projectID, CatalogJSON: string(data), FetchedAt: validatedAt}
}

func TestWarmModelCatalogsRevalidatesOnlyCachedClaudeAndMuseScopesSequentially(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	cache := &fakeModelCache{records: map[string]ports.CachedAgentModelCatalog{
		"claude-code\x00project-a": cachedModelRecord(t, "claude-code", "project-a", old, false),
		"muse\x00project-b":        cachedModelRecord(t, "muse", "project-b", old, false),
		"codex\x00project-c":       cachedModelRecord(t, "codex", "project-c", old, false),
	}}
	discoverer := &fakeModelDiscoverer{delay: 10 * time.Millisecond, catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog, Models: []ports.AgentModelInfo{{ID: "fresh-model"}}, Source: "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{
		harnessAgent("claude-code", "Claude Code", nil),
		harnessAgent("muse", "Muse", nil),
		harnessAgent("codex", "Codex", nil),
	}, cache, nil, discoverer)

	svc.warmModelCatalogs(context.Background())
	if got := discoverer.discoverCalls.Load(); got != 2 {
		t.Fatalf("discovery calls = %d, want cached Claude and Muse scopes only", got)
	}
	if discoverer.overlap.Load() {
		t.Fatal("startup model discoveries overlapped")
	}
}

func TestWarmModelCatalogsSuppressesOnlyRecentSuccessfulValidation(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	cache := &fakeModelCache{records: map[string]ports.CachedAgentModelCatalog{
		"claude-code\x00fresh": cachedModelRecord(t, "claude-code", "fresh", recent, false),
		"claude-code\x00stale": cachedModelRecord(t, "claude-code", "stale", recent, true),
	}}
	discoverer := &fakeModelDiscoverer{catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog, Models: []ports.AgentModelInfo{{ID: "fresh-model"}}, Source: "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{harnessAgent("claude-code", "Claude Code", nil)}, cache, nil, discoverer)

	svc.warmModelCatalogs(context.Background())
	if got := discoverer.discoverCalls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want only stale scope revalidated", got)
	}
}

func TestWarmModelCatalogsStartsAsynchronously(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	cache := &fakeModelCache{records: map[string]ports.CachedAgentModelCatalog{
		"muse\x00project-a": cachedModelRecord(t, "muse", "project-a", old, false),
	}}
	discoverer := &fakeModelDiscoverer{delay: 100 * time.Millisecond, catalog: ports.AgentModelCatalog{
		SelectionMode: ports.ModelSelectionCatalog, Models: []ports.AgentModelInfo{{ID: "fresh-model"}}, Source: "cli",
	}}
	svc := newService([]agentregistry.HarnessAgent{harnessAgent("muse", "Muse", nil)}, cache, nil, discoverer)

	started := time.Now()
	svc.WarmModelCatalogs(context.Background())
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("WarmModelCatalogs blocked for %s", elapsed)
	}
	deadline := time.Now().Add(time.Second)
	for discoverer.discoverCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if discoverer.discoverCalls.Load() == 0 {
		t.Fatal("background warm did not start")
	}
	for discoverer.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if discoverer.active.Load() != 0 {
		t.Fatal("background warm did not finish")
	}
}
