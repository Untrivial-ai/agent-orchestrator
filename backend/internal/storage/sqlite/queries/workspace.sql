-- name: DeleteWorkspaceReposByProject :exec
DELETE FROM workspace_repos WHERE project_id = ?;

-- name: UpsertWorkspaceRepo :exec
INSERT INTO workspace_repos (project_id, name, relative_path, repo_origin_url, default_branch, registered_at, git_status)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (project_id, name) DO UPDATE SET
    relative_path = excluded.relative_path,
    repo_origin_url = excluded.repo_origin_url,
    default_branch = excluded.default_branch,
    registered_at = excluded.registered_at,
    git_status = excluded.git_status;

-- name: ListWorkspaceRepos :many
SELECT project_id, name, relative_path, repo_origin_url, default_branch, registered_at, git_status
FROM workspace_repos
WHERE project_id = ?
ORDER BY name;

-- name: UpsertSessionWorktree :exec
INSERT INTO session_worktrees (session_id, repo_name, branch, base_sha, base_ref, worktree_path, preserved_ref, state)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id, repo_name) DO UPDATE SET
    branch = excluded.branch,
    base_sha = excluded.base_sha,
    base_ref = excluded.base_ref,
    worktree_path = excluded.worktree_path,
    preserved_ref = excluded.preserved_ref,
    state = excluded.state;

-- name: GetSessionWorktree :one
SELECT session_id, repo_name, branch, base_sha, worktree_path, preserved_ref, state, base_ref
FROM session_worktrees
WHERE session_id = ? AND repo_name = ?;

-- name: ListSessionWorktrees :many
SELECT session_id, repo_name, branch, base_sha, worktree_path, preserved_ref, state, base_ref
FROM session_worktrees
WHERE session_id = ?
ORDER BY CASE WHEN repo_name = '__root__' THEN 0 ELSE 1 END, repo_name;

-- name: DeleteSessionWorktrees :exec
DELETE FROM session_worktrees WHERE session_id = ?;
