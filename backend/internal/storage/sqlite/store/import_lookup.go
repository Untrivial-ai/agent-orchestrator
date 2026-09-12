package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/importidentity"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// FindImportedSessions reads candidates only for the page's native identities.
// Keyset batches bound memory even when native IDs collide across many roots.
// Root aliases are resolved on those bounded rows, including missing histories.
func (s *Store) FindImportedSessions(ctx context.Context, identities []ports.ImportIdentity) ([]domain.SessionRecord, error) {
	if len(identities) > 100 {
		return nil, fmt.Errorf("import lookup exceeds page limit")
	}
	out := make([]domain.SessionRecord, 0, len(identities))
	if len(identities) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(identities)
	if err != nil {
		return nil, err
	}
	found := make([]bool, len(identities))
	after := ""
	for len(out) < len(identities) {
		rows, err := s.readDB.QueryContext(ctx, `WITH targets AS (SELECT json_extract(value,'$.Provider') provider,json_extract(value,'$.NativeSessionID') native FROM json_each(?))
 SELECT DISTINCT s.id,s.project_id,s.harness,s.provider_conversation_id,s.agent_session_id,s.native_transcript_path
 FROM targets t JOIN sessions s ON s.harness=t.provider AND (s.provider_conversation_id=t.native OR s.agent_session_id=t.native)
 WHERE s.is_terminated=0 AND s.id>? ORDER BY s.id LIMIT 128`, string(raw), after)
		if err != nil {
			return nil, err
		}
		batch := make([]domain.SessionRecord, 0, 128)
		for rows.Next() {
			var r domain.SessionRecord
			if err = rows.Scan(&r.ID, &r.ProjectID, &r.Harness, &r.Metadata.ProviderConversationID, &r.Metadata.AgentSessionID, &r.Metadata.NativeTranscriptPath); err != nil {
				_ = rows.Close()
				return nil, err
			}
			batch = append(batch, r)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for n, id := range identities {
				if found[n] || r.Harness != id.Provider || (r.Metadata.ProviderConversationID != id.NativeSessionID && r.Metadata.AgentSessionID != id.NativeSessionID) {
					continue
				}
				if importidentity.Matches(id.Provider, id.ConfigDir, r.Metadata.NativeTranscriptPath) {
					found[n] = true
					out = append(out, r)
				}
			}
		}
		after = string(batch[len(batch)-1].ID)
		if len(batch) < 128 {
			break
		}
	}
	return out, nil
}
