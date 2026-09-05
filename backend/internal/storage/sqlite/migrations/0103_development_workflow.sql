-- +goose Up
-- +goose StatementBegin

CREATE TABLE development_plans (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    title TEXT NOT NULL,
    objective TEXT NOT NULL DEFAULT '',
    requirements TEXT NOT NULL DEFAULT '',
    implementation_summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('draft','confirmed','in_progress','completed','cancelled')),
    created_at DATETIME NOT NULL,
    confirmed_at DATETIME NULL,
    completed_at DATETIME NULL
);

CREATE INDEX idx_development_plans_project ON development_plans(project_id);

CREATE TABLE development_stages (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL REFERENCES development_plans(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending','in_progress','ready_for_approval','passed','blocked','cancelled')),
    created_at DATETIME NOT NULL,
    started_at DATETIME NULL,
    completed_at DATETIME NULL,
    UNIQUE(plan_id, sequence)
);

CREATE INDEX idx_development_stages_plan ON development_stages(plan_id);

CREATE TABLE development_tasks (
    id TEXT PRIMARY KEY,
    stage_id TEXT NOT NULL REFERENCES development_stages(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    task_type TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending','ready','running','review','passed','blocked','cancelled')),
    agent_role_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    provider_model_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    started_at DATETIME NULL,
    completed_at DATETIME NULL,
    UNIQUE(stage_id, sequence)
);

CREATE INDEX idx_development_tasks_stage ON development_tasks(stage_id);

CREATE TABLE agent_roles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    default_provider_id TEXT NOT NULL DEFAULT '',
    default_provider_model_id TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE task_runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES development_tasks(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    agent_role_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    provider_model_id TEXT NOT NULL DEFAULT '',
    provider_display_name TEXT NOT NULL DEFAULT '',
    provider_model_name TEXT NOT NULL DEFAULT '',
    executor_type TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed','cancelled')),
    result_summary TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    UNIQUE(task_id, attempt)
);

CREATE INDEX idx_task_runs_task ON task_runs(task_id);
CREATE INDEX idx_task_runs_session ON task_runs(session_id);

CREATE TABLE run_reviews (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('ai','human')),
    status TEXT NOT NULL CHECK (status IN ('pending','passed','rejected')),
    summary TEXT NOT NULL DEFAULT '',
    issues TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    completed_at DATETIME NULL
);

CREATE INDEX idx_run_reviews_run ON run_reviews(run_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE run_reviews;
DROP TABLE task_runs;
DROP TABLE agent_roles;
DROP TABLE development_tasks;
DROP TABLE development_stages;
DROP TABLE development_plans;
-- +goose StatementEnd
