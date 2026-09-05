package domain

import (
	"testing"
)

// ---- DevelopmentPlan status machine ----

func TestDevelopmentPlanStatusValid(t *testing.T) {
	valid := []DevelopmentPlanStatus{PlanStatusDraft, PlanStatusConfirmed, PlanStatusInProgress, PlanStatusCompleted, PlanStatusCancelled}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if DevelopmentPlanStatus("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}

func TestDevelopmentPlanTransitions(t *testing.T) {
	allowed := []struct {
		from, to DevelopmentPlanStatus
	}{
		{PlanStatusDraft, PlanStatusConfirmed},
		{PlanStatusDraft, PlanStatusCancelled},
		{PlanStatusConfirmed, PlanStatusInProgress},
		{PlanStatusConfirmed, PlanStatusCancelled},
		{PlanStatusInProgress, PlanStatusCompleted},
		{PlanStatusInProgress, PlanStatusCancelled},
	}
	for _, tr := range allowed {
		if err := ValidDevelopmentPlanTransition(tr.from, tr.to); err != nil {
			t.Errorf("%s -> %s should be allowed: %v", tr.from, tr.to, err)
		}
	}
}

func TestDevelopmentPlanTerminalBlocksTransitions(t *testing.T) {
	terminal := []DevelopmentPlanStatus{PlanStatusCompleted, PlanStatusCancelled}
	targets := []DevelopmentPlanStatus{PlanStatusDraft, PlanStatusConfirmed, PlanStatusInProgress, PlanStatusCompleted, PlanStatusCancelled}
	for _, from := range terminal {
		for _, to := range targets {
			if err := ValidDevelopmentPlanTransition(from, to); err == nil {
				t.Errorf("terminal %s -> %s should be rejected", from, to)
			}
		}
	}
}

func TestDevelopmentPlanInvalidTransitions(t *testing.T) {
	rejected := []struct {
		from, to DevelopmentPlanStatus
	}{
		{PlanStatusDraft, PlanStatusInProgress},
		{PlanStatusDraft, PlanStatusCompleted},
		{PlanStatusConfirmed, PlanStatusCompleted},
		{PlanStatusConfirmed, PlanStatusDraft},
		{PlanStatusInProgress, PlanStatusDraft},
		{PlanStatusInProgress, PlanStatusConfirmed},
	}
	for _, tr := range rejected {
		if err := ValidDevelopmentPlanTransition(tr.from, tr.to); err == nil {
			t.Errorf("%s -> %s should be rejected", tr.from, tr.to)
		}
	}
}

func TestDevelopmentPlanTerminalFlags(t *testing.T) {
	if !PlanStatusCompleted.IsTerminal() {
		t.Error("completed should be terminal")
	}
	if !PlanStatusCancelled.IsTerminal() {
		t.Error("cancelled should be terminal")
	}
	if PlanStatusDraft.IsTerminal() {
		t.Error("draft should not be terminal")
	}
}

// ---- DevelopmentStage status machine ----

func TestDevelopmentStageStatusValid(t *testing.T) {
	valid := []DevelopmentStageStatus{StageStatusPending, StageStatusInProgress, StageStatusReadyForApproval, StageStatusPassed, StageStatusBlocked, StageStatusCancelled}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if DevelopmentStageStatus("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}

func TestDevelopmentStageTransitions(t *testing.T) {
	allowed := []struct {
		from, to DevelopmentStageStatus
	}{
		{StageStatusPending, StageStatusInProgress},
		{StageStatusPending, StageStatusCancelled},
		{StageStatusInProgress, StageStatusReadyForApproval},
		{StageStatusInProgress, StageStatusBlocked},
		{StageStatusInProgress, StageStatusCancelled},
		{StageStatusBlocked, StageStatusInProgress},
		{StageStatusBlocked, StageStatusCancelled},
		{StageStatusReadyForApproval, StageStatusPassed},
		{StageStatusReadyForApproval, StageStatusInProgress},
		{StageStatusReadyForApproval, StageStatusCancelled},
	}
	for _, tr := range allowed {
		if err := ValidDevelopmentStageTransition(tr.from, tr.to); err != nil {
			t.Errorf("%s -> %s should be allowed: %v", tr.from, tr.to, err)
		}
	}
}

func TestDevelopmentStageTerminalBlocksTransitions(t *testing.T) {
	terminal := []DevelopmentStageStatus{StageStatusPassed, StageStatusCancelled}
	targets := []DevelopmentStageStatus{StageStatusPending, StageStatusInProgress, StageStatusReadyForApproval, StageStatusPassed, StageStatusBlocked, StageStatusCancelled}
	for _, from := range terminal {
		for _, to := range targets {
			if err := ValidDevelopmentStageTransition(from, to); err == nil {
				t.Errorf("terminal %s -> %s should be rejected", from, to)
			}
		}
	}
}

// ---- DevelopmentTask status machine ----

func TestDevelopmentTaskStatusValid(t *testing.T) {
	valid := []DevelopmentTaskStatus{TaskStatusPending, TaskStatusReady, TaskStatusRunning, TaskStatusReview, TaskStatusPassed, TaskStatusBlocked, TaskStatusCancelled}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if DevelopmentTaskStatus("rejected").Valid() {
		t.Error("rejected should be invalid (no REJECTED state)")
	}
	if DevelopmentTaskStatus("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}

func TestDevelopmentTaskNoRejectedState(t *testing.T) {
	// The spec explicitly requires: no REJECTED status for tasks.
	// Task review rejection returns to READY, not a REJECTED state.
	if DevelopmentTaskStatus("rejected").Valid() {
		t.Fatal("Task must not have a REJECTED status")
	}
}

func TestDevelopmentTaskTransitions(t *testing.T) {
	allowed := []struct {
		from, to DevelopmentTaskStatus
	}{
		{TaskStatusPending, TaskStatusReady},
		{TaskStatusPending, TaskStatusCancelled},
		{TaskStatusReady, TaskStatusRunning},
		{TaskStatusReady, TaskStatusBlocked},
		{TaskStatusReady, TaskStatusCancelled},
		{TaskStatusRunning, TaskStatusReview},
		{TaskStatusRunning, TaskStatusReady},
		{TaskStatusRunning, TaskStatusBlocked},
		{TaskStatusRunning, TaskStatusCancelled},
		{TaskStatusReview, TaskStatusPassed},
		{TaskStatusReview, TaskStatusReady}, // rejection returns to READY
		{TaskStatusReview, TaskStatusBlocked},
		{TaskStatusReview, TaskStatusCancelled},
		{TaskStatusBlocked, TaskStatusReady},
		{TaskStatusBlocked, TaskStatusCancelled},
	}
	for _, tr := range allowed {
		if err := ValidDevelopmentTaskTransition(tr.from, tr.to); err != nil {
			t.Errorf("%s -> %s should be allowed: %v", tr.from, tr.to, err)
		}
	}
}

func TestDevelopmentTaskTerminalBlocksTransitions(t *testing.T) {
	terminal := []DevelopmentTaskStatus{TaskStatusPassed, TaskStatusCancelled}
	targets := []DevelopmentTaskStatus{TaskStatusPending, TaskStatusReady, TaskStatusRunning, TaskStatusReview, TaskStatusPassed, TaskStatusBlocked, TaskStatusCancelled}
	for _, from := range terminal {
		for _, to := range targets {
			if err := ValidDevelopmentTaskTransition(from, to); err == nil {
				t.Errorf("terminal %s -> %s should be rejected", from, to)
			}
		}
	}
}

// ---- TaskRun status machine ----

func TestTaskRunStatusValid(t *testing.T) {
	valid := []TaskRunStatus{RunStatusPending, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if TaskRunStatus("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}

func TestTaskRunTransitions(t *testing.T) {
	allowed := []struct {
		from, to TaskRunStatus
	}{
		{RunStatusPending, RunStatusRunning},
		{RunStatusPending, RunStatusCancelled},
		{RunStatusRunning, RunStatusSucceeded},
		{RunStatusRunning, RunStatusFailed},
		{RunStatusRunning, RunStatusCancelled},
	}
	for _, tr := range allowed {
		if err := ValidTaskRunTransition(tr.from, tr.to); err != nil {
			t.Errorf("%s -> %s should be allowed: %v", tr.from, tr.to, err)
		}
	}
}

func TestTaskRunTerminalBlocksTransitions(t *testing.T) {
	terminal := []TaskRunStatus{RunStatusSucceeded, RunStatusFailed, RunStatusCancelled}
	targets := []TaskRunStatus{RunStatusPending, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled}
	for _, from := range terminal {
		for _, to := range targets {
			if err := ValidTaskRunTransition(from, to); err == nil {
				t.Errorf("terminal %s -> %s should be rejected", from, to)
			}
		}
	}
}

// ---- RunReview status machine ----

func TestRunReviewStatusValid(t *testing.T) {
	valid := []RunReviewStatus{RunReviewStatusPending, RunReviewStatusPassed, RunReviewStatusRejected}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if RunReviewStatus("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}

func TestRunReviewSourceValid(t *testing.T) {
	if !RunReviewSourceAI.Valid() {
		t.Error("ai should be valid")
	}
	if !RunReviewSourceHuman.Valid() {
		t.Error("human should be valid")
	}
	if RunReviewSource("bot").Valid() {
		t.Error("bot should be invalid")
	}
}

func TestRunReviewTransitions(t *testing.T) {
	allowed := []struct {
		from, to RunReviewStatus
	}{
		{RunReviewStatusPending, RunReviewStatusPassed},
		{RunReviewStatusPending, RunReviewStatusRejected},
	}
	for _, tr := range allowed {
		if err := ValidRunReviewTransition(tr.from, tr.to); err != nil {
			t.Errorf("%s -> %s should be allowed: %v", tr.from, tr.to, err)
		}
	}
}

func TestRunReviewTerminalBlocksTransitions(t *testing.T) {
	terminal := []RunReviewStatus{RunReviewStatusPassed, RunReviewStatusRejected}
	targets := []RunReviewStatus{RunReviewStatusPending, RunReviewStatusPassed, RunReviewStatusRejected}
	for _, from := range terminal {
		for _, to := range targets {
			if err := ValidRunReviewTransition(from, to); err == nil {
				t.Errorf("terminal %s -> %s should be rejected", from, to)
			}
		}
	}
}

// ---- Invalid status transitions ----

func TestInvalidFromStatusRejected(t *testing.T) {
	if err := ValidDevelopmentPlanTransition("bogus", PlanStatusDraft); err == nil {
		t.Error("invalid from status should be rejected")
	}
	if err := ValidDevelopmentStageTransition("bogus", StageStatusPending); err == nil {
		t.Error("invalid from status should be rejected")
	}
	if err := ValidDevelopmentTaskTransition("bogus", TaskStatusPending); err == nil {
		t.Error("invalid from status should be rejected")
	}
	if err := ValidTaskRunTransition("bogus", RunStatusPending); err == nil {
		t.Error("invalid from status should be rejected")
	}
	if err := ValidRunReviewTransition("bogus", RunReviewStatusPending); err == nil {
		t.Error("invalid from status should be rejected")
	}
}
