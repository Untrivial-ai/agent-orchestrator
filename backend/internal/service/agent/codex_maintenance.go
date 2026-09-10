package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// RefreshCodexInstallation verifies fresh readiness and refreshes every retained
// project catalog through new provider processes. Existing session controllers
// are not restarted. loadModels keeps useful cached data with stale/error facts.
func (s *Service) RefreshCodexInstallation(ctx context.Context) error {
	s.codexCatalogMu.Lock()
	defer s.codexCatalogMu.Unlock()
	s.InvalidateAgentInstallation("codex")
	s.InvalidateAgentAuthentication("codex")
	items, readinessErr := s.readiness.Force(ctx, []string{"codex"}, domain.AgentReadinessPurposeLaunch)
	if readinessErr == nil && (len(items) == 0 || items[0].EffectiveReadiness != domain.AgentReadinessReady) {
		readinessErr = fmt.Errorf("selected Codex account readiness is not verified; check authentication separately")
	}
	projects := map[string]bool{"": true}
	if s.cache != nil {
		records, err := s.cache.ListAgentModelCatalogsByAgent(ctx, "codex")
		if err != nil {
			return errors.Join(readinessErr, err)
		}
		for _, record := range records {
			projects[record.ProjectID] = true
		}
	}
	var failures []error
	failures = append(failures, readinessErr)
	for projectID := range projects {
		// The catalog gate prevents older readers overwriting verified results.
		catalog, err := s.loadModelsUnlocked(ctx, "codex", projectID, modelLoadRefresh)
		if err == nil && (catalog.Stale || len(catalog.Models) == 0) {
			err = fmt.Errorf("model catalog refresh: %s", catalog.Warning)
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
