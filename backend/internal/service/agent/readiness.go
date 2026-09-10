package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// startupBinaryProbeTimeout bounds the first-render prerequisite check. It
// only resolves executable paths; it never starts an agent CLI or probes
// authentication.
var startupBinaryProbeTimeout = 500 * time.Millisecond

// ProbeResult describes launch-fresh readiness for one supported agent.
type ProbeResult struct {
	Agent     Info `json:"agent"`
	Supported bool `json:"supported"`
	Installed bool `json:"installed"`
}

// Info is the legacy user-facing identity for an agent adapter.
type Info struct {
	ID         string                `json:"id"`
	Label      string                `json:"label"`
	AuthStatus ports.AgentAuthStatus `json:"authStatus,omitempty" enum:"authorized,unauthorized,unknown" description:"Advisory local auth probe result. authorized means a recent local probe passed; spawn remains the authoritative validation point."`
	UsageCount int                   `json:"usageCount,omitempty" description:"Number of retained sessions currently attributed to this agent."`
	LastUsedAt *time.Time            `json:"lastUsedAt,omitempty" format:"date-time" description:"Creation time of the newest retained session currently attributed to this agent."`
}

// Inventory is the compatibility projection consumed by older clients.
type Inventory struct {
	Supported  []Info `json:"supported" description:"Agents supported by this daemon build."`
	Installed  []Info `json:"installed" description:"Agents whose binary resolved during the latest best-effort local catalog probe."`
	Authorized []Info `json:"authorized" description:"Compatibility list of installed agents whose local auth probe recently returned authorized. Advisory and stale-prone; spawn may still fail."`
}

// Readiness is the normalized, daemon-owned readiness response.
type Readiness struct {
	Agents []domain.AgentReadinessSnapshot `json:"agents"`
}

// CachedReadiness returns the current in-memory snapshots without native work.
func (s *Service) CachedReadiness(ctx context.Context) (Readiness, error) {
	if err := ctx.Err(); err != nil {
		return Readiness{}, err
	}
	return s.withReadinessUsage(ctx, s.readiness.Snapshot())
}

// EnsureReadiness waits for the checks required by purpose. Empty agentIDs
// selects all supported harnesses.
func (s *Service) EnsureReadiness(ctx context.Context, agentIDs []string, purpose domain.AgentReadinessPurpose) (Readiness, error) {
	items, err := s.readiness.Ensure(ctx, agentIDs, purpose)
	if err != nil {
		var unsupported unsupportedAgentError
		if errors.As(err, &unsupported) {
			return Readiness{}, apierr.Invalid("UNKNOWN_AGENT_ID", "Unknown agent adapter: "+unsupported.id, map[string]any{"agentId": unsupported.id})
		}
		if !purpose.Valid() {
			return Readiness{}, apierr.Invalid("INVALID_READINESS_PURPOSE", "Purpose must be display or launch", map[string]any{"purpose": purpose})
		}
		return Readiness{}, err
	}
	return s.withReadinessUsage(ctx, items)
}

// EnsureAgentReadiness is the narrow service boundary used by launch paths.
func (s *Service) EnsureAgentReadiness(ctx context.Context, agentID string, purpose domain.AgentReadinessPurpose) (domain.AgentReadinessSnapshot, error) {
	result, err := s.EnsureReadiness(ctx, []string{agentID}, purpose)
	if err != nil {
		return domain.AgentReadinessSnapshot{}, err
	}
	return result.Agents[0], nil
}

// InvalidateAgentInstallation marks an agent's installation observation stale.
func (s *Service) InvalidateAgentInstallation(agentID string) {
	s.readiness.Invalidate(agentID, readinessInvalidateInstallation)
}

// InvalidateAgentAuthentication marks an agent's authentication observation stale.
func (s *Service) InvalidateAgentAuthentication(agentID string) {
	s.readiness.Invalidate(agentID, readinessInvalidateAuthentication)
	if agentID == string(domain.HarnessCodex) && s.codexAccounts != nil {
		if accountID := s.codexAccounts.activeAccountID(); accountID != "" {
			s.codexAccounts.invalidate(accountID)
		}
	}
}

// RecheckAgent schedules a non-blocking display readiness ensure.
func (s *Service) RecheckAgent(agentID string) {
	go func() {
		_, _ = s.readiness.Ensure(s.readiness.ctx, []string{agentID}, domain.AgentReadinessPurposeDisplay)
	}()
}

// WarmReadiness starts the coordinator's bounded asynchronous warm-up.
func (s *Service) WarmReadiness() { s.readiness.Warm() }

func (s *Service) withReadinessUsage(ctx context.Context, snapshots []domain.AgentReadinessSnapshot) (Readiness, error) {
	if s.sessions == nil {
		return Readiness{Agents: snapshots}, nil
	}
	records, err := s.sessions.ListAllSessions(ctx)
	if err != nil {
		return Readiness{}, fmt.Errorf("list sessions for agent usage: %w", err)
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
	for i := range snapshots {
		usage := usageByAgent[snapshots[i].ID]
		snapshots[i].UsageCount = usage.count
		if !usage.lastUsedAt.IsZero() {
			lastUsedAt := usage.lastUsedAt
			snapshots[i].LastUsedAt = &lastUsedAt
		}
	}
	return Readiness{Agents: snapshots}, nil
}

// List is the legacy projection of the cached readiness snapshot. It never
// performs native work.
func (s *Service) List(ctx context.Context) (Inventory, error) {
	readiness, err := s.CachedReadiness(ctx)
	if err != nil {
		return Inventory{}, err
	}
	return projectInventory(readiness.Agents), nil
}

// Refresh is the legacy projection of an explicit forced display refresh.
func (s *Service) Refresh(ctx context.Context) (Inventory, error) {
	items, err := s.readiness.Force(ctx, nil, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return Inventory{}, err
	}
	readiness, err := s.withReadinessUsage(ctx, items)
	if err != nil {
		return Inventory{}, err
	}
	return projectInventory(readiness.Agents), nil
}

// RefreshFresh is retained for the system-check compatibility boundary.
func (s *Service) RefreshFresh(ctx context.Context) (Inventory, error) {
	return s.Refresh(ctx)
}

// FindInstalledBinary returns one installed agent CLI without running any
// agent process or authentication probe. It is used by the desktop's startup
// prerequisite gate.
func (s *Service) FindInstalledBinary(ctx context.Context) (Info, bool) {
	if ctx.Err() != nil {
		return Info{}, false
	}
	waitCtx, cancel := context.WithTimeout(ctx, startupBinaryProbeTimeout)
	defer cancel()
	snapshot, ok := s.readiness.FindInstalled(waitCtx, domain.AgentReadinessPurposeLaunch)
	if !ok {
		return Info{}, false
	}
	return readinessInfo(snapshot), true
}

type sessionUsage struct {
	count      int
	lastUsedAt time.Time
}

// Probe is the legacy projection of a targeted launch ensure. Explicit probes
// always invalidate authentication first so a user checking immediately after
// login cannot receive the cached pre-login observation.
func (s *Service) Probe(ctx context.Context, agentID string) (ProbeResult, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	if _, ok := s.agent(agentID); !ok {
		return ProbeResult{Agent: Info{ID: agentID}, Supported: false, Installed: false}, nil
	}
	s.InvalidateAgentAuthentication(agentID)
	readiness, err := s.EnsureReadiness(ctx, []string{agentID}, domain.AgentReadinessPurposeLaunch)
	if err != nil {
		return ProbeResult{}, err
	}
	snapshot := readiness.Agents[0]
	return ProbeResult{
		Agent: readinessInfo(snapshot), Supported: true,
		// Compatibility clients historically interpreted false as definitely
		// missing. Unknown must remain advisory under the normalized contract.
		Installed: snapshot.Installation.State != domain.AgentInstallationNotInstalled,
	}, nil
}

func projectInventory(snapshots []domain.AgentReadinessSnapshot) Inventory {
	inventory := Inventory{Supported: make([]Info, 0, len(snapshots)), Installed: []Info{}, Authorized: []Info{}}
	for _, snapshot := range snapshots {
		info := readinessInfo(snapshot)
		inventory.Supported = append(inventory.Supported, info)
		if snapshot.Installation.State == domain.AgentInstallationInstalled {
			inventory.Installed = append(inventory.Installed, info)
		}
		if snapshot.Authentication.State == domain.AgentAuthenticationAuthorized || snapshot.Authentication.State == domain.AgentAuthenticationNotApplicable {
			inventory.Authorized = append(inventory.Authorized, info)
		}
	}
	sortInfosByUsage(inventory.Supported)
	sortInfosByUsage(inventory.Installed)
	sortInfosByUsage(inventory.Authorized)
	return inventory
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

func readinessInfo(snapshot domain.AgentReadinessSnapshot) Info {
	status := ports.AgentAuthStatusUnknown
	switch snapshot.Authentication.State {
	case domain.AgentAuthenticationAuthorized, domain.AgentAuthenticationNotApplicable:
		status = ports.AgentAuthStatusAuthorized
	case domain.AgentAuthenticationUnauthorized:
		status = ports.AgentAuthStatusUnauthorized
	}
	return Info{
		ID: snapshot.ID, Label: snapshot.Label, AuthStatus: status,
		UsageCount: snapshot.UsageCount, LastUsedAt: snapshot.LastUsedAt,
	}
}
