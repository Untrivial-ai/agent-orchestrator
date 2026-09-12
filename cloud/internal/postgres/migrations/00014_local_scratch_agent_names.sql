-- +goose Up

UPDATE ao_sessions AS session
SET display_name = CASE session.harness
        WHEN 'claude-code' THEN 'Claude agent'
        WHEN 'codex' THEN 'Codex agent'
        WHEN 'cursor' THEN 'Cursor agent'
        ELSE 'Agent'
    END,
    updated_at = now()
FROM ao_projects AS project
WHERE session.org_id = project.org_id
  AND session.project_id = project.id
  AND session.kind = 'orchestrator'
  AND project.repository_url LIKE 'https://scratch.ao.local/%'
  AND project.config->>'source' = 'scratch'
  AND COALESCE(project.config->>'standalone', 'false') <> 'true'
  AND session.display_name = project.display_name || ' orchestrator';

-- +goose Down

-- Historical display names cannot be reconstructed after a project rename.
SELECT 1;
