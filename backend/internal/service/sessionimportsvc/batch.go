package sessionimportsvc

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

// Selection identifies one provider conversation selected for import.
type Selection struct {
	Provider        string `json:"provider"`
	NativeSessionID string `json:"nativeSessionId"`
}

// ImportResult reports the durable outcome for one selection.
type ImportResult struct {
	Provider        string `json:"provider"`
	NativeSessionID string `json:"nativeSessionId"`
	SessionID       string `json:"sessionId,omitempty"`
	AlreadyImported bool   `json:"alreadyImported"`
	Error           string `json:"error,omitempty"`
}

// ImportBatch keeps bulk work in one daemon request instead of a renderer
// network/paint round trip for every saved conversation. Cancellation stops
// pending work before commit; completed imports remain idempotently retryable.
func (s *Service) ImportBatch(ctx context.Context, project domain.ProjectID, selections []Selection) []ImportResult {
	if err := s.imports.Acquire(ctx, 1); err != nil {
		return nil
	}
	defer s.imports.Release(1)
	opts, setupErr := s.projectOptions(ctx, project)
	var available []sessionimport.ImportableSession
	if setupErr == nil {
		for _, selection := range selections {
			opts.Providers = append(opts.Providers, domain.AgentHarness(selection.Provider))
		}
		available, setupErr = s.disco.Discover(ctx, opts)
	}
	targets := map[string]sessionimport.ImportableSession{}
	for _, target := range available {
		targets[sessionimport.NativeKey(target.Provider, target.NativeSessionID)] = target
	}
	existing := map[string]domain.SessionRecord{}
	records, err := s.store.ListAllSessions(ctx)
	if setupErr == nil {
		setupErr = err
	}
	for _, record := range records {
		if record.IsTerminated {
			continue
		}
		for _, id := range []string{record.Metadata.ProviderConversationID, record.Metadata.AgentSessionID} {
			if id != "" {
				existing[sessionimport.NativeKey(record.Harness, id)] = record
			}
		}
	}
	// Validate before the transaction; duplicates share the same inserted row.
	created := map[string]domain.SessionRecord{}
	var batchErr error
	bulk, canBatch := s.sessions.(interface {
		RegisterImports(context.Context, []ports.SpawnConfig) ([]domain.SessionRecord, error)
	})
	if setupErr == nil && canBatch {
		var configs []ports.SpawnConfig
		seen := map[string]bool{}
		for _, selection := range selections {
			key := sessionimport.NativeKey(domain.AgentHarness(selection.Provider), selection.NativeSessionID)
			target, ok := targets[key]
			if seen[key] || existing[key].ID != "" || !ok || !opts.IncludeCWD(target.CWD) || target.LastActivity.Before(opts.Since) || target.TokenCount < MinimumTokens {
				continue
			}
			seen[key] = true
			configs = append(configs, importConfig(target, project))
		}
		var records []domain.SessionRecord
		records, batchErr = bulk.RegisterImports(ctx, configs)
		for _, record := range records {
			created[sessionimport.NativeKey(record.Harness, record.Metadata.ProviderConversationID)] = record
		}
	}
	out := make([]ImportResult, 0, len(selections))
	for _, selection := range selections {
		if ctx.Err() != nil {
			break
		}
		result := ImportResult{Provider: selection.Provider, NativeSessionID: selection.NativeSessionID}
		key := sessionimport.NativeKey(domain.AgentHarness(selection.Provider), selection.NativeSessionID)
		switch {
		case setupErr != nil:
			result.Error = setupErr.Error()
		case existing[key].ID != "":
			record := existing[key]
			if record.ProjectID != project {
				result.Error = ErrImportProjectUnresolved.Error()
			} else {
				result.SessionID = string(record.ID)
				result.AlreadyImported = true
			}
		default:
			target, ok := targets[key]
			if !ok || !opts.IncludeCWD(target.CWD) || target.LastActivity.Before(opts.Since) || target.TokenCount < MinimumTokens {
				result.Error = ErrImportSessionNotFound.Error()
			} else if canBatch {
				if batchErr != nil {
					result.Error = batchErr.Error()
				} else {
					record := created[key]
					result.SessionID = string(record.ID)
					existing[key] = record
				}
			} else {
				session, err := s.registerTarget(ctx, target, project)
				if err != nil {
					result.Error = err.Error()
				} else {
					result.SessionID = string(session.ID)
					existing[key] = session.SessionRecord
				}
			}
		}
		out = append(out, result)
	}
	return out
}
