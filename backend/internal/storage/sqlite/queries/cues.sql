-- User-managed reusable quick actions (Cues) scoped to a project. The
-- (project_id, name) UNIQUE constraint enforces per-project name uniqueness;
-- duplicates surface as domain.ErrCueNameExists in the store.

-- name: InsertCue :one
INSERT INTO cues (
    id, project_id, name, description, type, command, prompt, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: SelectCueByID :one
SELECT *
FROM cues
WHERE id = ?;

-- name: SelectCuesByProject :many
SELECT *
FROM cues
WHERE project_id = ?
ORDER BY name;

-- name: UpdateCue :one
UPDATE cues
SET name = ?, description = ?, type = ?, command = ?, prompt = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: DeleteCueByID :execrows
DELETE FROM cues
WHERE id = ?;