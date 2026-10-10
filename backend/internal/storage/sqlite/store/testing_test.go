package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestTestingRecordsBindingsRetriesAndCDC(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, s, "ao")
	session, err := s.CreateSession(ctx, sampleRecord("ao"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	run := domain.TestRunRecord{ID: "run", ProjectID: "ao", IssueURL: "https://example.test/issue/1", IssueSnapshot: `"quoted issue"`, CommitSHA: "abc", RecipeSnapshot: `{"id":"native"}`, Requester: "maintainer", CreatedAt: now}
	if err = s.CreateTestRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.CreateTestAttempt(ctx, domain.TestAttemptRecord{ID: "attempt", RunID: run.ID, Deadline: now.Add(time.Hour), RecordingGap: "no video", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Number != 1 || attempt.LeaseGeneration != 1 || attempt.Phase != domain.TestAttemptStarting {
		t.Fatal("attempt defaults")
	}
	if err = s.SetTestRunWorker(ctx, run.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	attempt.Target = domain.TestTargetIdentity{ID: "target", LaunchID: "launch", Generation: 1, ElectronPID: 10, ElectronStartedAt: now, DaemonPID: 11, DaemonStartedAt: now, DataDir: "/isolated/data", WindowID: "window"}
	attempt.Phase = domain.TestAttemptActive
	if err = s.UpdateTestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	link := domain.TestToolProfileLink{SessionID: session.ID, AttemptID: attempt.ID, ProfileID: domain.TestToolProfileNativeV1}
	if err = s.BindTestTools(ctx, link); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateTestAttempt(ctx, domain.TestAttemptRecord{ID: "overlap", RunID: run.ID, Deadline: attempt.Deadline, CreatedAt: now}); err == nil {
		t.Fatal("overlapping live attempt admitted")
	}
	attempt.Phase = domain.TestAttemptFinished
	attempt.FinishedAt = &now
	attempt.Outcome = domain.TestOutcomePartial
	attempt.CleanupState = domain.TestCleanupComplete
	if err = s.UpdateTestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err = s.SetTestRunReport(ctx, run.ID, "report-receipt"); err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateTestAttempt(ctx, domain.TestAttemptRecord{ID: "retry", RunID: run.ID, Deadline: attempt.Deadline, CreatedAt: now})
	if err != nil || retry.Number != 2 {
		t.Fatal("retry numbering", err)
	}
	linked := run
	linked.ID = "linked"
	linked.LinkedRunID = run.ID
	linked.CommitSHA = "next"
	if err = s.CreateTestRun(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, ok, err := s.GetTestAttempt(ctx, attempt.ID)
	if err != nil || !ok || saved.Target != attempt.Target || saved.Outcome != attempt.Outcome || saved.RecordingGap != attempt.RecordingGap || saved.FinishedAt == nil {
		t.Fatal("attempt did not survive reopen", err)
	}
	bound, ok, err := s.GetTestToolBinding(ctx, session.ID)
	if err != nil || !ok || bound != link {
		t.Fatal("binding did not survive reopen", err)
	}
	got, ok, err := s.GetTestRun(ctx, run.ID)
	if err != nil || !ok || got.WorkerSessionID != session.ID || got.ReportEvidenceID != "report-receipt" {
		t.Fatal("run not saved", err)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == "testing_updated" {
			count++
			if !json.Valid(event.Payload) {
				t.Fatal("invalid testing CDC")
			}
		}
	}
	if count < 8 {
		t.Fatal("missing testing CDC", count)
	}
}
func TestTestingForeignKeysAndConcurrentAttemptCreation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "ao")
	now := time.Now().UTC()
	run := domain.TestRunRecord{ID: "run", ProjectID: "missing", IssueSnapshot: `"issue"`, RecipeSnapshot: `{}`, CreatedAt: now}
	if err := s.CreateTestRun(ctx, run); err == nil {
		t.Fatal("missing project accepted")
	}
	run.ProjectID = "ao"
	if err := s.CreateTestRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTestTools(ctx, domain.TestToolProfileLink{SessionID: "missing", AttemptID: "missing", ProfileID: domain.TestToolProfileNativeV1}); err == nil {
		t.Fatal("missing binding references accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []domain.TestAttemptID{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateTestAttempt(ctx, domain.TestAttemptRecord{ID: id, RunID: run.ID, Deadline: now.Add(time.Hour), CreatedAt: now})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("multiple live attempts", success)
	}
}

func TestTestingWorkerLegsSurviveReopenAndBindingSwitch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, s, "ao")
	session, err := s.CreateSession(ctx, sampleRecord("ao"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []domain.TestRunID{"base", "head"} {
		if err := s.CreateTestRun(ctx, domain.TestRunRecord{ID: id, ProjectID: "ao", IssueSnapshot: `"PR"`, RecipeSnapshot: `{}`, CommitSHA: string(id), Requester: "tester", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTestAttempt(ctx, domain.TestAttemptRecord{ID: domain.TestAttemptID(id), RunID: id, Deadline: now.Add(time.Hour), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	pair := domain.TestWorkerLegs{SessionID: session.ID, BaseRunID: "base", HeadRunID: "head", TimeoutSeconds: 1800}
	if err := s.CreateTestWorkerLegs(ctx, pair); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTestWorkerLegs(ctx, pair); err == nil {
		t.Fatal("revision pair was overwritten")
	}
	for _, id := range []domain.TestAttemptID{"base", "head"} {
		if err := s.BindTestTools(ctx, domain.TestToolProfileLink{SessionID: session.ID, AttemptID: id, ProfileID: domain.TestToolProfileNativeV1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, found, err := s.GetTestWorkerLegs(ctx, session.ID)
	if err != nil || !found || got != pair {
		t.Fatal("pair lost after reopen", got, err)
	}
	binding, found, err := s.GetTestToolBinding(ctx, session.ID)
	if err != nil || !found || binding.AttemptID != "head" {
		t.Fatal("current head binding lost", binding, err)
	}
	if _, found, err := s.GetTestWorkerLegs(ctx, "ordinary"); err != nil || found {
		t.Fatal("ordinary session has comparison", err)
	}
}
