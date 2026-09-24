-- name: CreateResearchRun :one
INSERT INTO research_runs (
    id, parent_session_id, project_id, prompt, harness, model, effort, mode,
    permissions, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetResearchRun :one
SELECT * FROM research_runs WHERE id = ?;

-- name: GetActiveResearchRunByParent :one
SELECT * FROM research_runs WHERE parent_session_id = ? AND status IN ('queued', 'running') LIMIT 1;

-- name: ListResearchRunsByParent :many
SELECT * FROM research_runs WHERE parent_session_id = ? ORDER BY created_at DESC, id DESC LIMIT 50;

-- name: ListUnfinishedResearchRuns :many
SELECT * FROM research_runs WHERE status IN ('queued', 'running') ORDER BY created_at;

-- name: MarkResearchRunning :execrows
UPDATE research_runs SET status = 'running', started_at = ?
WHERE id = ? AND status = 'queued';

-- name: FinishResearchRun :execrows
UPDATE research_runs SET status = ?, result = ?, error = ?, finished_at = ?
WHERE id = ? AND status IN ('queued', 'running');

-- name: CancelResearchRun :execrows
UPDATE research_runs SET status = 'cancelled', finished_at = ?
WHERE id = ? AND status IN ('queued', 'running');

-- name: InterruptResearchRun :execrows
UPDATE research_runs SET status = 'interrupted', error = ?, finished_at = ?
WHERE id = ? AND status IN ('queued', 'running');
