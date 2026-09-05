package domain

import (
	"errors"
	"time"
)

// ---- ID types ----

type (
	DevelopmentPlanID  string
	DevelopmentStageID string
	DevelopmentTaskID  string
	AgentRoleID        string
	TaskRunID          string
	RunReviewID        string
)

// ---- DevelopmentPlan ----

type DevelopmentPlanStatus string

const (
	PlanStatusDraft      DevelopmentPlanStatus = "draft"
	PlanStatusConfirmed  DevelopmentPlanStatus = "confirmed"
	PlanStatusInProgress DevelopmentPlanStatus = "in_progress"
	PlanStatusCompleted  DevelopmentPlanStatus = "completed"
	PlanStatusCancelled  DevelopmentPlanStatus = "cancelled"
)

var validPlanTransitions = map[DevelopmentPlanStatus][]DevelopmentPlanStatus{
	PlanStatusDraft:      {PlanStatusConfirmed, PlanStatusCancelled},
	PlanStatusConfirmed:  {PlanStatusInProgress, PlanStatusCancelled},
	PlanStatusInProgress: {PlanStatusCompleted, PlanStatusCancelled},
}

func (s DevelopmentPlanStatus) IsTerminal() bool {
	return s == PlanStatusCompleted || s == PlanStatusCancelled
}

func (s DevelopmentPlanStatus) Valid() bool {
	switch s {
	case PlanStatusDraft, PlanStatusConfirmed, PlanStatusInProgress, PlanStatusCompleted, PlanStatusCancelled:
		return true
	default:
		return false
	}
}

func ValidDevelopmentPlanTransition(from, to DevelopmentPlanStatus) error {
	if !from.Valid() || !to.Valid() {
		return errors.New("invalid plan status")
	}
	targets, ok := validPlanTransitions[from]
	if !ok {
		return errors.New("plan status is terminal")
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return errors.New("invalid plan status transition")
}

type DevelopmentPlan struct {
	ID                    DevelopmentPlanID     `json:"id"`
	ProjectID             ProjectID             `json:"projectId"`
	Title                 string                `json:"title"`
	Objective             string                `json:"objective"`
	Requirements          string                `json:"requirements"`
	ImplementationSummary string                `json:"implementationSummary"`
	Status                DevelopmentPlanStatus `json:"status"`
	CreatedAt             time.Time             `json:"createdAt"`
	ConfirmedAt           *time.Time            `json:"confirmedAt,omitempty"`
	CompletedAt           *time.Time            `json:"completedAt,omitempty"`
}

// ---- DevelopmentStage ----

type DevelopmentStageStatus string

const (
	StageStatusPending          DevelopmentStageStatus = "pending"
	StageStatusInProgress       DevelopmentStageStatus = "in_progress"
	StageStatusReadyForApproval DevelopmentStageStatus = "ready_for_approval"
	StageStatusPassed           DevelopmentStageStatus = "passed"
	StageStatusBlocked          DevelopmentStageStatus = "blocked"
	StageStatusCancelled        DevelopmentStageStatus = "cancelled"
)

var validStageTransitions = map[DevelopmentStageStatus][]DevelopmentStageStatus{
	StageStatusPending:          {StageStatusInProgress, StageStatusCancelled},
	StageStatusInProgress:       {StageStatusReadyForApproval, StageStatusBlocked, StageStatusCancelled},
	StageStatusBlocked:          {StageStatusInProgress, StageStatusCancelled},
	StageStatusReadyForApproval: {StageStatusPassed, StageStatusInProgress, StageStatusCancelled},
}

func (s DevelopmentStageStatus) IsTerminal() bool {
	return s == StageStatusPassed || s == StageStatusCancelled
}

func (s DevelopmentStageStatus) Valid() bool {
	switch s {
	case StageStatusPending, StageStatusInProgress, StageStatusReadyForApproval, StageStatusPassed, StageStatusBlocked, StageStatusCancelled:
		return true
	default:
		return false
	}
}

func ValidDevelopmentStageTransition(from, to DevelopmentStageStatus) error {
	if !from.Valid() || !to.Valid() {
		return errors.New("invalid stage status")
	}
	targets, ok := validStageTransitions[from]
	if !ok {
		return errors.New("stage status is terminal")
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return errors.New("invalid stage status transition")
}

type DevelopmentStage struct {
	ID                DevelopmentStageID     `json:"id"`
	PlanID            DevelopmentPlanID      `json:"planId"`
	Sequence          int                    `json:"sequence"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	AcceptanceCriteria string                `json:"acceptanceCriteria"`
	Status            DevelopmentStageStatus `json:"status"`
	CreatedAt         time.Time              `json:"createdAt"`
	StartedAt         *time.Time             `json:"startedAt,omitempty"`
	CompletedAt       *time.Time             `json:"completedAt,omitempty"`
}

// ---- DevelopmentTask ----

type DevelopmentTaskStatus string

const (
	TaskStatusPending DevelopmentTaskStatus = "pending"
	TaskStatusReady   DevelopmentTaskStatus = "ready"
	TaskStatusRunning DevelopmentTaskStatus = "running"
	TaskStatusReview  DevelopmentTaskStatus = "review"
	TaskStatusPassed  DevelopmentTaskStatus = "passed"
	TaskStatusBlocked DevelopmentTaskStatus = "blocked"
	TaskStatusCancelled DevelopmentTaskStatus = "cancelled"
)

var validTaskTransitions = map[DevelopmentTaskStatus][]DevelopmentTaskStatus{
	TaskStatusPending: {TaskStatusReady, TaskStatusCancelled},
	TaskStatusReady:   {TaskStatusRunning, TaskStatusBlocked, TaskStatusCancelled},
	TaskStatusRunning: {TaskStatusReview, TaskStatusReady, TaskStatusBlocked, TaskStatusCancelled},
	TaskStatusReview:  {TaskStatusPassed, TaskStatusReady, TaskStatusBlocked, TaskStatusCancelled},
	TaskStatusBlocked: {TaskStatusReady, TaskStatusCancelled},
}

func (s DevelopmentTaskStatus) IsTerminal() bool {
	return s == TaskStatusPassed || s == TaskStatusCancelled
}

func (s DevelopmentTaskStatus) Valid() bool {
	switch s {
	case TaskStatusPending, TaskStatusReady, TaskStatusRunning, TaskStatusReview, TaskStatusPassed, TaskStatusBlocked, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

func ValidDevelopmentTaskTransition(from, to DevelopmentTaskStatus) error {
	if !from.Valid() || !to.Valid() {
		return errors.New("invalid task status")
	}
	targets, ok := validTaskTransitions[from]
	if !ok {
		return errors.New("task status is terminal")
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return errors.New("invalid task status transition")
}

type DevelopmentTask struct {
	ID                 DevelopmentTaskID      `json:"id"`
	StageID            DevelopmentStageID     `json:"stageId"`
	Sequence           int                    `json:"sequence"`
	Title              string                 `json:"title"`
	Description        string                 `json:"description"`
	TaskType           string                 `json:"taskType"`
	AcceptanceCriteria string                 `json:"acceptanceCriteria"`
	Status             DevelopmentTaskStatus  `json:"status"`
	AgentRoleID        AgentRoleID            `json:"agentRoleId,omitempty"`
	ProviderID         ProviderID             `json:"providerId,omitempty"`
	ProviderModelID    ProviderModelID        `json:"providerModelId,omitempty"`
	CreatedAt          time.Time              `json:"createdAt"`
	StartedAt          *time.Time             `json:"startedAt,omitempty"`
	CompletedAt        *time.Time             `json:"completedAt,omitempty"`
}

// ---- AgentRole ----

type AgentRole struct {
	ID                    string        `json:"id"`
	Name                  string        `json:"name"`
	DisplayName           string        `json:"displayName"`
	Description           string        `json:"description"`
	SystemPrompt          string        `json:"systemPrompt"`
	DefaultProviderID     ProviderID    `json:"defaultProviderId,omitempty"`
	DefaultProviderModelID ProviderModelID `json:"defaultProviderModelId,omitempty"`
	Enabled               bool          `json:"enabled"`
	CreatedAt             time.Time     `json:"createdAt"`
	UpdatedAt             time.Time     `json:"updatedAt"`
}

// ---- TaskRun ----

type TaskRunStatus string

const (
	RunStatusPending   TaskRunStatus = "pending"
	RunStatusRunning   TaskRunStatus = "running"
	RunStatusSucceeded TaskRunStatus = "succeeded"
	RunStatusFailed    TaskRunStatus = "failed"
	RunStatusCancelled TaskRunStatus = "cancelled"
)

var validRunTransitions = map[TaskRunStatus][]TaskRunStatus{
	RunStatusPending: {RunStatusRunning, RunStatusCancelled},
	RunStatusRunning: {RunStatusSucceeded, RunStatusFailed, RunStatusCancelled},
}

func (s TaskRunStatus) IsTerminal() bool {
	return s == RunStatusSucceeded || s == RunStatusFailed || s == RunStatusCancelled
}

func (s TaskRunStatus) Valid() bool {
	switch s {
	case RunStatusPending, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled:
		return true
	default:
		return false
	}
}

func ValidTaskRunTransition(from, to TaskRunStatus) error {
	if !from.Valid() || !to.Valid() {
		return errors.New("invalid run status")
	}
	targets, ok := validRunTransitions[from]
	if !ok {
		return errors.New("run status is terminal")
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return errors.New("invalid run status transition")
}

type TaskRun struct {
	ID                 TaskRunID        `json:"id"`
	TaskID             DevelopmentTaskID `json:"taskId"`
	Attempt            int              `json:"attempt"`
	SessionID          SessionID        `json:"sessionId,omitempty"`
	AgentRoleID        AgentRoleID      `json:"agentRoleId,omitempty"`
	ProviderID         ProviderID       `json:"providerId,omitempty"`
	ProviderModelID    ProviderModelID  `json:"providerModelId,omitempty"`
	ProviderDisplayName string          `json:"providerDisplayName,omitempty"`
	ProviderModelName  string           `json:"providerModelName,omitempty"`
	ExecutorType       string           `json:"executorType,omitempty"`
	Status             TaskRunStatus    `json:"status"`
	ResultSummary      string           `json:"resultSummary,omitempty"`
	ErrorMessage       string           `json:"errorMessage,omitempty"`
	CreatedAt          time.Time        `json:"createdAt"`
	StartedAt          *time.Time       `json:"startedAt,omitempty"`
	FinishedAt         *time.Time       `json:"finishedAt,omitempty"`
}

// ---- RunReview ----

type RunReviewSource string

const (
	RunReviewSourceAI    RunReviewSource = "ai"
	RunReviewSourceHuman RunReviewSource = "human"
)

func (s RunReviewSource) Valid() bool {
	return s == RunReviewSourceAI || s == RunReviewSourceHuman
}

type RunReviewStatus string

const (
	RunReviewStatusPending  RunReviewStatus = "pending"
	RunReviewStatusPassed   RunReviewStatus = "passed"
	RunReviewStatusRejected RunReviewStatus = "rejected"
)

var validReviewTransitions = map[RunReviewStatus][]RunReviewStatus{
	RunReviewStatusPending: {RunReviewStatusPassed, RunReviewStatusRejected},
}

func (s RunReviewStatus) IsTerminal() bool {
	return s == RunReviewStatusPassed || s == RunReviewStatusRejected
}

func (s RunReviewStatus) Valid() bool {
	switch s {
	case RunReviewStatusPending, RunReviewStatusPassed, RunReviewStatusRejected:
		return true
	default:
		return false
	}
}

func ValidRunReviewTransition(from, to RunReviewStatus) error {
	if !from.Valid() || !to.Valid() {
		return errors.New("invalid review status")
	}
	targets, ok := validReviewTransitions[from]
	if !ok {
		return errors.New("review status is terminal")
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return errors.New("invalid review status transition")
}

type RunReview struct {
	ID          RunReviewID     `json:"id"`
	RunID       TaskRunID       `json:"runId"`
	Source      RunReviewSource `json:"source"`
	Status      RunReviewStatus `json:"status"`
	Summary     string          `json:"summary"`
	Issues      string          `json:"issues"`
	CreatedAt   time.Time       `json:"createdAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
}
