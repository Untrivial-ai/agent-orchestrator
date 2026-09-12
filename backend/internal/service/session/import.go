package session

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// RegisterImport records existing history without starting a provider, creating
// a checkout, or requiring credentials. Workspace ownership starts only when the
// user explicitly resumes the imported session.
func (s *Service) RegisterImport(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	if _, err := s.requireProject(ctx, cfg.ProjectID); err != nil {
		return domain.Session{}, 0, 0, err
	}
	native := cfg.ResumeNativeSession
	if native == nil || native.NativeSessionID == "" || native.TranscriptPath == "" {
		return domain.Session{}, 0, 0, fmt.Errorf("import requires an existing native transcript")
	}
	writer, ok := s.store.(interface {
		CreateSession(context.Context, domain.SessionRecord) (domain.SessionRecord, error)
	})
	if !ok {
		return domain.Session{}, 0, 0, fmt.Errorf("session import storage unavailable")
	}
	now := s.now()
	rec, err := writer.CreateSession(ctx, domain.SessionRecord{
		ProjectID: cfg.ProjectID, Kind: domain.KindWorker, Harness: cfg.Harness,
		DisplayName: cfg.DisplayName, Mode: domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityExited},
		CreatedAt: now, UpdatedAt: now,
		Metadata: domain.SessionMetadata{
			ProviderConversationID: native.NativeSessionID,
			NativeTranscriptPath:   native.TranscriptPath,
			SourceBranch:           native.SourceBranch,
		},
	})
	if err != nil {
		return domain.Session{}, 0, 0, err
	}
	sess, err := s.toSessionWithFacts(rec, nil, nil)
	return sess, 0, 0, err
}

// RegisterImports validates the project once and durably registers the entire
// selection in one storage transaction. No provider or workspace is started.
func (s *Service) RegisterImports(ctx context.Context, configs []ports.SpawnConfig) ([]domain.SessionRecord, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	project := configs[0].ProjectID
	if _, err := s.requireProject(ctx, project); err != nil {
		return nil, err
	}
	writer, ok := s.store.(interface {
		CreateSessions(context.Context, []domain.SessionRecord) ([]domain.SessionRecord, error)
	})
	if !ok {
		return nil, fmt.Errorf("batch import storage unavailable")
	}
	now := s.now()
	records := make([]domain.SessionRecord, 0, len(configs))
	for _, cfg := range configs {
		native := cfg.ResumeNativeSession
		if cfg.ProjectID != project || native == nil || native.NativeSessionID == "" || native.TranscriptPath == "" {
			return nil, fmt.Errorf("import requires a project and existing native transcript")
		}
		records = append(records, domain.SessionRecord{
			ProjectID: project, Kind: domain.KindWorker, Harness: cfg.Harness,
			DisplayName: cfg.DisplayName, Mode: domain.SessionModeChat,
			Activity: domain.Activity{State: domain.ActivityExited}, CreatedAt: now, UpdatedAt: now,
			Metadata: domain.SessionMetadata{ProviderConversationID: native.NativeSessionID, NativeTranscriptPath: native.TranscriptPath, SourceBranch: native.SourceBranch},
		})
	}
	return writer.CreateSessions(ctx, records)
}
