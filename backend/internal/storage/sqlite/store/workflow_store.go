package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ---- development_plans ----

func (s *Store) CreateDevelopmentPlan(ctx context.Context, p domain.DevelopmentPlan) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO development_plans
(id,project_id,title,objective,requirements,implementation_summary,status,created_at)
VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, p.ProjectID, p.Title, p.Objective, p.Requirements, p.ImplementationSummary, p.Status, p.CreatedAt)
	return err
}

func scanDevelopmentPlan(row *sql.Row) (domain.DevelopmentPlan, error) {
	var p domain.DevelopmentPlan
	err := row.Scan(&p.ID, &p.ProjectID, &p.Title, &p.Objective, &p.Requirements,
		&p.ImplementationSummary, &p.Status, &p.CreatedAt, &p.ConfirmedAt, &p.CompletedAt)
	return p, err
}

func scanDevelopmentPlanRows(rows *sql.Rows) ([]domain.DevelopmentPlan, error) {
	defer rows.Close()
	out := make([]domain.DevelopmentPlan, 0)
	for rows.Next() {
		var p domain.DevelopmentPlan
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Title, &p.Objective, &p.Requirements,
			&p.ImplementationSummary, &p.Status, &p.CreatedAt, &p.ConfirmedAt, &p.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetDevelopmentPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, bool, error) {
	p, err := scanDevelopmentPlan(s.readDB.QueryRowContext(ctx,
		`SELECT id,project_id,title,objective,requirements,implementation_summary,status,created_at,confirmed_at,completed_at
FROM development_plans WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DevelopmentPlan{}, false, nil
	}
	return p, true, err
}

func (s *Store) ListDevelopmentPlansByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.DevelopmentPlan, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id,project_id,title,objective,requirements,implementation_summary,status,created_at,confirmed_at,completed_at
FROM development_plans WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	return scanDevelopmentPlanRows(rows)
}

func (s *Store) UpdateDevelopmentPlanStatus(ctx context.Context, id domain.DevelopmentPlanID, status domain.DevelopmentPlanStatus, confirmedAt, completedAt *time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE development_plans SET status=?, confirmed_at=COALESCE(confirmed_at,?), completed_at=COALESCE(completed_at,?) WHERE id=?`,
		status, confirmedAt, completedAt, id)
	return err
}

func (s *Store) UpdateDevelopmentPlanContent(ctx context.Context, id domain.DevelopmentPlanID, title, objective, requirements, implSummary string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE development_plans SET title=?,objective=?,requirements=?,implementation_summary=? WHERE id=?`,
		title, objective, requirements, implSummary, id)
	return err
}

// ---- development_stages ----

func (s *Store) CreateDevelopmentStage(ctx context.Context, st domain.DevelopmentStage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO development_stages
(id,plan_id,sequence,title,description,acceptance_criteria,status,created_at)
VALUES(?,?,?,?,?,?,?,?)`,
		st.ID, st.PlanID, st.Sequence, st.Title, st.Description, st.AcceptanceCriteria, st.Status, st.CreatedAt)
	return err
}

func scanDevelopmentStage(row *sql.Row) (domain.DevelopmentStage, error) {
	var st domain.DevelopmentStage
	err := row.Scan(&st.ID, &st.PlanID, &st.Sequence, &st.Title, &st.Description,
		&st.AcceptanceCriteria, &st.Status, &st.CreatedAt, &st.StartedAt, &st.CompletedAt)
	return st, err
}

func (s *Store) GetDevelopmentStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, bool, error) {
	st, err := scanDevelopmentStage(s.readDB.QueryRowContext(ctx,
		`SELECT id,plan_id,sequence,title,description,acceptance_criteria,status,created_at,started_at,completed_at
FROM development_stages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DevelopmentStage{}, false, nil
	}
	return st, true, err
}

func (s *Store) ListDevelopmentStagesByPlan(ctx context.Context, planID domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id,plan_id,sequence,title,description,acceptance_criteria,status,created_at,started_at,completed_at
FROM development_stages WHERE plan_id=? ORDER BY sequence`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DevelopmentStage, 0)
	for rows.Next() {
		var st domain.DevelopmentStage
		if err := rows.Scan(&st.ID, &st.PlanID, &st.Sequence, &st.Title, &st.Description,
			&st.AcceptanceCriteria, &st.Status, &st.CreatedAt, &st.StartedAt, &st.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDevelopmentStageStatus(ctx context.Context, id domain.DevelopmentStageID, status domain.DevelopmentStageStatus, startedAt, completedAt *time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE development_stages SET status=?,started_at=COALESCE(started_at,?),completed_at=COALESCE(completed_at,?) WHERE id=?`,
		status, startedAt, completedAt, id)
	return err
}

// ---- development_tasks ----

func (s *Store) CreateDevelopmentTask(ctx context.Context, t domain.DevelopmentTask) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO development_tasks
(id,stage_id,sequence,title,description,task_type,acceptance_criteria,status,agent_role_id,provider_id,provider_model_id,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.StageID, t.Sequence, t.Title, t.Description, t.TaskType, t.AcceptanceCriteria,
		t.Status, t.AgentRoleID, t.ProviderID, t.ProviderModelID, t.CreatedAt)
	return err
}

func scanDevelopmentTask(row *sql.Row) (domain.DevelopmentTask, error) {
	var t domain.DevelopmentTask
	err := row.Scan(&t.ID, &t.StageID, &t.Sequence, &t.Title, &t.Description, &t.TaskType,
		&t.AcceptanceCriteria, &t.Status, &t.AgentRoleID, &t.ProviderID, &t.ProviderModelID,
		&t.CreatedAt, &t.StartedAt, &t.CompletedAt)
	return t, err
}

func (s *Store) GetDevelopmentTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, bool, error) {
	t, err := scanDevelopmentTask(s.readDB.QueryRowContext(ctx,
		`SELECT id,stage_id,sequence,title,description,task_type,acceptance_criteria,status,agent_role_id,provider_id,provider_model_id,created_at,started_at,completed_at
FROM development_tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DevelopmentTask{}, false, nil
	}
	return t, true, err
}

func (s *Store) ListDevelopmentTasksByStage(ctx context.Context, stageID domain.DevelopmentStageID) ([]domain.DevelopmentTask, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id,stage_id,sequence,title,description,task_type,acceptance_criteria,status,agent_role_id,provider_id,provider_model_id,created_at,started_at,completed_at
FROM development_tasks WHERE stage_id=? ORDER BY sequence`, stageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DevelopmentTask, 0)
	for rows.Next() {
		var t domain.DevelopmentTask
		if err := rows.Scan(&t.ID, &t.StageID, &t.Sequence, &t.Title, &t.Description, &t.TaskType,
			&t.AcceptanceCriteria, &t.Status, &t.AgentRoleID, &t.ProviderID, &t.ProviderModelID,
			&t.CreatedAt, &t.StartedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDevelopmentTaskStatus(ctx context.Context, id domain.DevelopmentTaskID, status domain.DevelopmentTaskStatus, startedAt, completedAt *time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE development_tasks SET status=?,started_at=COALESCE(started_at,?),completed_at=COALESCE(completed_at,?) WHERE id=?`,
		status, startedAt, completedAt, id)
	return err
}

func (s *Store) UpdateDevelopmentTaskAssignment(ctx context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE development_tasks SET agent_role_id=?,provider_id=?,provider_model_id=? WHERE id=?`,
		roleID, providerID, modelID, id)
	return err
}

// ---- agent_roles ----

const agentRoleColumns = `id,name,display_name,description,system_prompt,default_provider_id,default_provider_model_id,enabled,created_at,updated_at`

func scanAgentRole(row *sql.Row) (domain.AgentRole, error) {
	var r domain.AgentRole
	err := row.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Description, &r.SystemPrompt,
		&r.DefaultProviderID, &r.DefaultProviderModelID, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) CreateAgentRole(ctx context.Context, r domain.AgentRole) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO agent_roles
(`+agentRoleColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Name, r.DisplayName, r.Description, r.SystemPrompt,
		r.DefaultProviderID, r.DefaultProviderModelID, r.Enabled, r.CreatedAt, r.UpdatedAt)
	return err
}

func (s *Store) GetAgentRole(ctx context.Context, id domain.AgentRoleID) (domain.AgentRole, bool, error) {
	r, err := scanAgentRole(s.readDB.QueryRowContext(ctx,
		`SELECT `+agentRoleColumns+` FROM agent_roles WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRole{}, false, nil
	}
	return r, true, err
}

func (s *Store) ListAgentRoles(ctx context.Context) ([]domain.AgentRole, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+agentRoleColumns+` FROM agent_roles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.AgentRole, 0)
	for rows.Next() {
		var r domain.AgentRole
		if err := rows.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Description, &r.SystemPrompt,
			&r.DefaultProviderID, &r.DefaultProviderModelID, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAgentRole(ctx context.Context, r domain.AgentRole) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE agent_roles SET display_name=?,description=?,system_prompt=?,default_provider_id=?,default_provider_model_id=?,updated_at=? WHERE id=?`,
		r.DisplayName, r.Description, r.SystemPrompt, r.DefaultProviderID, r.DefaultProviderModelID, r.UpdatedAt, r.ID)
	return err
}

func (s *Store) SetAgentRoleEnabled(ctx context.Context, id domain.AgentRoleID, enabled bool, updatedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `UPDATE agent_roles SET enabled=?,updated_at=? WHERE id=?`, enabled, updatedAt, id)
	return err
}

// ---- task_runs ----

func (s *Store) CreateTaskRun(ctx context.Context, r domain.TaskRun) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO task_runs
(id,task_id,attempt,session_id,agent_role_id,provider_id,provider_model_id,provider_display_name,provider_model_name,executor_type,status,result_summary,error_message,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.TaskID, r.Attempt, r.SessionID, r.AgentRoleID, r.ProviderID, r.ProviderModelID,
		r.ProviderDisplayName, r.ProviderModelName, r.ExecutorType, r.Status,
		r.ResultSummary, r.ErrorMessage, r.CreatedAt)
	return err
}

func scanTaskRun(row *sql.Row) (domain.TaskRun, error) {
	var r domain.TaskRun
	err := row.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.SessionID, &r.AgentRoleID,
		&r.ProviderID, &r.ProviderModelID, &r.ProviderDisplayName, &r.ProviderModelName,
		&r.ExecutorType, &r.Status, &r.ResultSummary, &r.ErrorMessage,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	return r, err
}

func (s *Store) GetTaskRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, bool, error) {
	r, err := scanTaskRun(s.readDB.QueryRowContext(ctx,
		`SELECT id,task_id,attempt,session_id,agent_role_id,provider_id,provider_model_id,provider_display_name,provider_model_name,executor_type,status,result_summary,error_message,created_at,started_at,finished_at
FROM task_runs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskRun{}, false, nil
	}
	return r, true, err
}

func (s *Store) ListTaskRunsByTask(ctx context.Context, taskID domain.DevelopmentTaskID) ([]domain.TaskRun, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id,task_id,attempt,session_id,agent_role_id,provider_id,provider_model_id,provider_display_name,provider_model_name,executor_type,status,result_summary,error_message,created_at,started_at,finished_at
FROM task_runs WHERE task_id=? ORDER BY attempt`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.TaskRun, 0)
	for rows.Next() {
		var r domain.TaskRun
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.SessionID, &r.AgentRoleID,
			&r.ProviderID, &r.ProviderModelID, &r.ProviderDisplayName, &r.ProviderModelName,
			&r.ExecutorType, &r.Status, &r.ResultSummary, &r.ErrorMessage,
			&r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTaskRunStatus(ctx context.Context, id domain.TaskRunID, status domain.TaskRunStatus, resultSummary, errorMessage string, startedAt, finishedAt *time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE task_runs SET status=?,result_summary=CASE WHEN ?='' THEN result_summary ELSE ? END,error_message=CASE WHEN ?='' THEN error_message ELSE ? END,started_at=COALESCE(started_at,?),finished_at=COALESCE(finished_at,?) WHERE id=?`,
		status, resultSummary, resultSummary, errorMessage, errorMessage, startedAt, finishedAt, id)
	return err
}

func (s *Store) BindTaskRunSession(ctx context.Context, id domain.TaskRunID, sessionID domain.SessionID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE task_runs SET session_id=? WHERE id=? AND session_id=''`, sessionID, id)
	return err
}

// ListTaskRunsByStatus returns all task runs with the given status.
// Used by Reconcile to find PENDING/RUNNING runs that may need attention.
func (s *Store) ListTaskRunsByStatus(ctx context.Context, status domain.TaskRunStatus) ([]domain.TaskRun, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id,task_id,attempt,session_id,agent_role_id,provider_id,provider_model_id,provider_display_name,provider_model_name,executor_type,status,result_summary,error_message,created_at,started_at,finished_at
FROM task_runs WHERE status=? ORDER BY created_at`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.TaskRun, 0)
	for rows.Next() {
		var r domain.TaskRun
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.SessionID, &r.AgentRoleID,
			&r.ProviderID, &r.ProviderModelID, &r.ProviderDisplayName, &r.ProviderModelName,
			&r.ExecutorType, &r.Status, &r.ResultSummary, &r.ErrorMessage,
			&r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateTaskRunSnapshot sets provider/session metadata on an existing run.
// Used by StartRun to bind session info after spawn.
// Only updates non-empty fields; caller should set fields selectively.
func (s *Store) UpdateTaskRunSnapshot(ctx context.Context, id domain.TaskRunID, sessionID domain.SessionID, providerID domain.ProviderID, providerModelID domain.ProviderModelID, providerDisplayName, providerModelName, executorType string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE task_runs SET session_id=?,provider_id=?,provider_model_id=?,provider_display_name=?,provider_model_name=?,executor_type=? WHERE id=?`,
		sessionID, providerID, providerModelID, providerDisplayName, providerModelName, executorType, id)
	return err
}

// ---- run_reviews ----

func (s *Store) CreateRunReview(ctx context.Context, r domain.RunReview) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO run_reviews
(id,run_id,source,status,summary,issues,created_at)
VALUES(?,?,?,?,?,?,?)`,
		r.ID, r.RunID, r.Source, r.Status, r.Summary, r.Issues, r.CreatedAt)
	return err
}

func scanRunReview(row *sql.Row) (domain.RunReview, error) {
	var r domain.RunReview
	err := row.Scan(&r.ID, &r.RunID, &r.Source, &r.Status, &r.Summary, &r.Issues, &r.CreatedAt, &r.CompletedAt)
	return r, err
}

func (s *Store) GetRunReview(ctx context.Context, id domain.RunReviewID) (domain.RunReview, bool, error) {
	r, err := scanRunReview(s.readDB.QueryRowContext(ctx,
		`SELECT id,run_id,source,status,summary,issues,created_at,completed_at FROM run_reviews WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunReview{}, false, nil
	}
	return r, true, err
}

func (s *Store) ListRunReviewsByRun(ctx context.Context, runID domain.TaskRunID) ([]domain.RunReview, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id,run_id,source,status,summary,issues,created_at,completed_at FROM run_reviews WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.RunReview, 0)
	for rows.Next() {
		var r domain.RunReview
		if err := rows.Scan(&r.ID, &r.RunID, &r.Source, &r.Status, &r.Summary, &r.Issues, &r.CreatedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRunReviewStatus(ctx context.Context, id domain.RunReviewID, status domain.RunReviewStatus, completedAt *time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx, `UPDATE run_reviews SET status=?,completed_at=? WHERE id=?`, status, completedAt, id)
	return err
}
