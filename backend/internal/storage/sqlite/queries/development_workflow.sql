-- Development Workflow queries for sqlc (Phase 2.1)
-- These queries document the intended SQL surface. The store implementation
-- uses raw SQL directly because sqlc is not available on this machine.
-- Run `sqlc generate` when the tool is available to produce gen/ code.

-- ---- development_plans ----

-- name: CreateDevelopmentPlan :exec
INSERT INTO development_plans (id, project_id, title, objective, requirements, implementation_summary, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDevelopmentPlan :one
SELECT id, project_id, title, objective, requirements, implementation_summary, status, created_at, confirmed_at, completed_at
FROM development_plans WHERE id = ? LIMIT 1;

-- name: ListDevelopmentPlansByProject :many
SELECT id, project_id, title, objective, requirements, implementation_summary, status, created_at, confirmed_at, completed_at
FROM development_plans WHERE project_id = ? ORDER BY created_at DESC;

-- name: UpdateDevelopmentPlanStatus :exec
UPDATE development_plans SET status = ?, confirmed_at = COALESCE(confirmed_at, ?), completed_at = COALESCE(completed_at, ?) WHERE id = ?;

-- name: UpdateDevelopmentPlanContent :exec
UPDATE development_plans SET title = ?, objective = ?, requirements = ?, implementation_summary = ? WHERE id = ?;

-- ---- development_stages ----

-- name: CreateDevelopmentStage :exec
INSERT INTO development_stages (id, plan_id, sequence, title, description, acceptance_criteria, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDevelopmentStage :one
SELECT id, plan_id, sequence, title, description, acceptance_criteria, status, created_at, started_at, completed_at
FROM development_stages WHERE id = ? LIMIT 1;

-- name: ListDevelopmentStagesByPlan :many
SELECT id, plan_id, sequence, title, description, acceptance_criteria, status, created_at, started_at, completed_at
FROM development_stages WHERE plan_id = ? ORDER BY sequence;

-- name: UpdateDevelopmentStageStatus :exec
UPDATE development_stages SET status = ?, started_at = COALESCE(started_at, ?), completed_at = COALESCE(completed_at, ?) WHERE id = ?;

-- ---- development_tasks ----

-- name: CreateDevelopmentTask :exec
INSERT INTO development_tasks (id, stage_id, sequence, title, description, task_type, acceptance_criteria, status, agent_role_id, provider_id, provider_model_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDevelopmentTask :one
SELECT id, stage_id, sequence, title, description, task_type, acceptance_criteria, status, agent_role_id, provider_id, provider_model_id, created_at, started_at, completed_at
FROM development_tasks WHERE id = ? LIMIT 1;

-- name: ListDevelopmentTasksByStage :many
SELECT id, stage_id, sequence, title, description, task_type, acceptance_criteria, status, agent_role_id, provider_id, provider_model_id, created_at, started_at, completed_at
FROM development_tasks WHERE stage_id = ? ORDER BY sequence;

-- name: UpdateDevelopmentTaskStatus :exec
UPDATE development_tasks SET status = ?, started_at = COALESCE(started_at, ?), completed_at = COALESCE(completed_at, ?) WHERE id = ?;

-- name: UpdateDevelopmentTaskAssignment :exec
UPDATE development_tasks SET agent_role_id = ?, provider_id = ?, provider_model_id = ? WHERE id = ?;

-- ---- agent_roles ----

-- name: CreateAgentRole :exec
INSERT INTO agent_roles (id, name, display_name, description, system_prompt, default_provider_id, default_provider_model_id, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentRole :one
SELECT id, name, display_name, description, system_prompt, default_provider_id, default_provider_model_id, enabled, created_at, updated_at
FROM agent_roles WHERE id = ? LIMIT 1;

-- name: ListAgentRoles :many
SELECT id, name, display_name, description, system_prompt, default_provider_id, default_provider_model_id, enabled, created_at, updated_at
FROM agent_roles ORDER BY name;

-- name: UpdateAgentRole :exec
UPDATE agent_roles SET display_name = ?, description = ?, system_prompt = ?, default_provider_id = ?, default_provider_model_id = ?, updated_at = ? WHERE id = ?;

-- name: SetAgentRoleEnabled :exec
UPDATE agent_roles SET enabled = ?, updated_at = ? WHERE id = ?;

-- ---- task_runs ----

-- name: CreateTaskRun :exec
INSERT INTO task_runs (id, task_id, attempt, session_id, agent_role_id, provider_id, provider_model_id, provider_display_name, provider_model_name, executor_type, status, result_summary, error_message, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTaskRun :one
SELECT id, task_id, attempt, session_id, agent_role_id, provider_id, provider_model_id, provider_display_name, provider_model_name, executor_type, status, result_summary, error_message, created_at, started_at, finished_at
FROM task_runs WHERE id = ? LIMIT 1;

-- name: ListTaskRunsByTask :many
SELECT id, task_id, attempt, session_id, agent_role_id, provider_id, provider_model_id, provider_display_name, provider_model_name, executor_type, status, result_summary, error_message, created_at, started_at, finished_at
FROM task_runs WHERE task_id = ? ORDER BY attempt;

-- name: UpdateTaskRunStatus :exec
UPDATE task_runs SET status = ?, result_summary = COALESCE(result_summary, ?), error_message = COALESCE(error_message, ?), started_at = COALESCE(started_at, ?), finished_at = COALESCE(finished_at, ?) WHERE id = ?;

-- name: BindTaskRunSession :exec
UPDATE task_runs SET session_id = ? WHERE id = ? AND session_id = '';

-- ---- run_reviews ----

-- name: CreateRunReview :exec
INSERT INTO run_reviews (id, run_id, source, status, summary, issues, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetRunReview :one
SELECT id, run_id, source, status, summary, issues, created_at, completed_at
FROM run_reviews WHERE id = ? LIMIT 1;

-- name: ListRunReviewsByRun :many
SELECT id, run_id, source, status, summary, issues, created_at, completed_at
FROM run_reviews WHERE run_id = ? ORDER BY created_at;

-- name: UpdateRunReviewStatus :exec
UPDATE run_reviews SET status = ?, completed_at = ? WHERE id = ?;
