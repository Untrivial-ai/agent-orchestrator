package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"path/filepath"
)

// FindImportedSessions reads at most one durable marker for each bounded source
// identity. Profile-root predicates remain inside SQL, ahead of the row bound.
func (s *Store) FindImportedSessions(ctx context.Context, identities []ports.ImportIdentity) ([]domain.SessionRecord, error) {
	if len(identities) > 100 {
		return nil, fmt.Errorf("import lookup exceeds page limit")
	}
	type source struct {
		Provider  domain.AgentHarness `json:"provider"`
		Native    string              `json:"native"`
		Prefix    string              `json:"prefix"`
		Alternate string              `json:"alternate"`
		Slashes   int                 `json:"slashes"`
	}
	targets := make([]source, 0, len(identities))
	for _, id := range identities {
		v := source{Provider: id.Provider, Native: id.NativeSessionID}
		switch id.Provider {
		case domain.HarnessClaudeCode:
			v.Prefix = filepath.Join(id.ConfigDir, "projects") + string(filepath.Separator)
			v.Alternate = v.Prefix
			v.Slashes = 1
		case domain.HarnessCodex:
			v.Prefix = filepath.Join(id.ConfigDir, "sessions") + string(filepath.Separator)
			v.Alternate = filepath.Join(id.ConfigDir, "archived_sessions") + string(filepath.Separator)
			v.Slashes = 3
		default:
			continue
		}
		targets = append(targets, v)
	}
	if len(targets) == 0 {
		return []domain.SessionRecord{}, nil
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return nil, err
	}
	rows, err := s.readDB.QueryContext(ctx, `WITH targets AS (SELECT key,json_extract(value,'$.provider') provider,json_extract(value,'$.native') native,json_extract(value,'$.prefix') prefix,json_extract(value,'$.alternate') alternate,json_extract(value,'$.slashes') slashes FROM json_each(?)), matches AS (
 SELECT s.id,s.project_id,s.harness,s.provider_conversation_id,s.agent_session_id,s.native_transcript_path,row_number() OVER(PARTITION BY t.key ORDER BY s.created_at,s.id) position
 FROM targets t JOIN sessions s ON s.harness=t.provider AND (s.provider_conversation_id=t.native OR s.agent_session_id=t.native)
 WHERE s.is_terminated=0 AND (
 (substr(s.native_transcript_path,1,length(t.prefix))=t.prefix AND length(substr(s.native_transcript_path,length(t.prefix)+1))-length(replace(substr(s.native_transcript_path,length(t.prefix)+1),?,''))=t.slashes)
 OR (substr(s.native_transcript_path,1,length(t.alternate))=t.alternate AND length(substr(s.native_transcript_path,length(t.alternate)+1))-length(replace(substr(s.native_transcript_path,length(t.alternate)+1),?,''))=t.slashes)))
 SELECT id,project_id,harness,provider_conversation_id,agent_session_id,native_transcript_path FROM matches WHERE position=1`, string(raw), string(filepath.Separator), string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.SessionRecord, 0, len(targets))
	for rows.Next() {
		var r domain.SessionRecord
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Harness, &r.Metadata.ProviderConversationID, &r.Metadata.AgentSessionID, &r.Metadata.NativeTranscriptPath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
