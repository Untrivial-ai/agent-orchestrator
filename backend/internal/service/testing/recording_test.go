package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type movieDesktop struct {
	*policyDesktop
	result            ports.TestingRecordingResult
	startErr, stopErr error
	escapePath        string
	startGap, stopGap string
	closeErr          error
}

func (d *movieDesktop) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupEvents = append(d.cleanupEvents, "desktop_close")
	return errors.Join(ctx.Err(), d.closeErr)
}

func (d *movieDesktop) StartRecording(_ context.Context, _ domain.TestTargetIdentity, dir string) (ports.TestingRecordingResult, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ports.TestingRecordingResult{}, err
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ports.TestingRecordingResult{}, err
	}
	d.result = ports.TestingRecordingResult{Path: filepath.Join(dir, "window.mov"), MIMEType: "video/quicktime", Width: 2, Height: 2, StartedAt: d.clock.Now(), RecorderPID: 800, StagingCleanup: "pending: recording in progress"}
	d.result.Gap = d.startGap
	if d.startErr != nil {
		d.result.Gap = "owned recorder startup observation failed"
	}
	return d.result, d.startErr
}
func (d *movieDesktop) StopRecording(ctx context.Context, _ domain.TestTargetIdentity) (ports.TestingRecordingResult, error) {
	d.mu.Lock()
	d.cleanupEvents = append(d.cleanupEvents, "recording_stop")
	d.mu.Unlock()
	if ctx.Err() != nil {
		return d.result, ctx.Err()
	}
	d.result.Duration = time.Second
	d.result.StoppedAt = d.result.StartedAt.Add(time.Second)
	d.result.StagingPath = "/native/staging/owned-window.mov"
	d.result.StagingCleanup = "verified absent after final move"
	d.result.Gap = d.stopGap
	if d.stopErr != nil {
		d.result.Gap = "owned recorder did not finalize"
		return d.result, d.stopErr
	}
	if d.escapePath != "" {
		d.result.Path = d.escapePath
	}
	if err := os.WriteFile(d.result.Path, []byte("fake-finalized-movie"), 0600); err != nil {
		return d.result, err
	}
	return d.result, nil
}

func movieFixture(t *testing.T, configure func(*movieDesktop)) (*fixture, *movieDesktop) {
	t.Helper()
	var desktop *movieDesktop
	f := newFixture(t, func(deps *Deps) {
		desktop = &movieDesktop{policyDesktop: &policyDesktop{fakeProviders: deps.Desktop.(*fakeProviders), mode: "foreground"}}
		if configure != nil {
			configure(desktop)
		}
		deps.Desktop = desktop
	})
	return f, desktop
}

func TestRecordingFinishCancelAndDeadlineSaveEvidenceBeforeTargetStop(t *testing.T) {
	for _, finish := range []string{"finish", "cancel", "deadline", "partial_start"} {
		t.Run(finish, func(t *testing.T) {
			f, desktop := movieFixture(t, func(d *movieDesktop) {
				if finish == "partial_start" {
					d.startErr = errors.New("startup birth observation failed")
				}
			})
			switch finish {
			case "finish":
				if _, err := f.call("report", "submit_report", domain.TestSubmitReportRequest{Outcome: domain.TestOutcomePartial, Markdown: "Observed target."}); err != nil {
					t.Fatal(err)
				}
			case "deadline":
				f.clock.advance(time.Minute)
			default:
				if _, err := f.svc.Cancel(context.Background(), f.start.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			f.wait(t)
			if !reflect.DeepEqual(f.provider.cleanupEvents, []string{"recording_stop", "desktop_release", "target_stop"}) {
				t.Fatal("recording cleanup order", f.provider.cleanupEvents)
			}
			rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
			if err != nil || rec.RecordingGap != "" || rec.CleanupState != domain.TestCleanupComplete {
				t.Fatal("successful recording retained a gap", rec, err)
			}
			if err := os.Remove(desktop.result.Path); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(f.dir, "testing", string(f.run.ID), string(rec.ID))
			receipts, err := f.svc.ListEvidence(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			foundVideo, foundMetadata := false, false
			for _, receipt := range receipts {
				switch receipt.Kind {
				case "recording":
					data, err := os.ReadFile(filepath.Join(dir, receipt.RelativePath))
					if err != nil || string(data) != "fake-finalized-movie" || receipt.MIMEType != "video/quicktime" || !strings.HasSuffix(receipt.RelativePath, ".mov") {
						t.Fatal("recording did not survive target/source stop", err)
					}
					foundVideo = true
				case "recording_metadata":
					foundMetadata = true
				}
			}
			if !foundVideo || !foundMetadata {
				t.Fatal("recording receipts missing", receipts)
			}
			journal, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			foundResult := false
			for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
				var record domain.TestActionRecord
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record.WindowID != rec.Target.WindowID || record.LaunchID != rec.Target.LaunchID {
					t.Fatal("recording journal lost target identity", record)
				}
				if record.Tool != "stop_recording" || record.State != "completed" {
					continue
				}
				var result ports.TestingRecordingResult
				if err := json.Unmarshal(record.Recording, &result); err != nil || !reflect.DeepEqual(result, desktop.result) {
					t.Fatal("full recording/staging result missing from journal", result, err)
				}
				foundResult = true
			}
			if !foundResult || bytes.Contains(journal, []byte(f.worker.binding.Capability)) {
				t.Fatal("recording journal missing or capability leaked")
			}
		})
	}
}

func TestCloseMidAttemptFinalizesRecordingStopsTargetAndClosesDesktop(t *testing.T) {
	for _, failure := range []string{"", "recording_deadline", "desktop_close"} {
		t.Run(failure, func(t *testing.T) {
			f, _ := movieFixture(t, func(d *movieDesktop) {
				if failure == "recording_deadline" {
					d.stopErr = context.DeadlineExceeded
				}
				if failure == "desktop_close" {
					d.closeErr = errors.New("owned driver stop failed")
				}
			})
			entered := make(chan struct{})
			f.provider.screenshotHook = func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			toolDone := make(chan error, 1)
			go func() {
				_, err := f.call("inflight", "screenshot", map[string]any{})
				toolDone <- err
			}()
			<-entered
			closed := make(chan error, 1)
			go func() { closed <- f.svc.Close() }()
			select {
			case err := <-closed:
				if (err != nil) != (failure != "") {
					t.Fatal("shutdown error", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown did not finish within the test bound")
			}
			if err := <-toolDone; err == nil {
				t.Fatal("shutdown admitted the in-flight tool")
			}
			wantEvents := []string{"recording_stop", "desktop_release", "target_stop", "desktop_close"}
			if !reflect.DeepEqual(f.provider.cleanupEvents, wantEvents) {
				t.Fatal("shutdown cleanup order", f.provider.cleanupEvents)
			}
			rec, ok, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
			wantState := domain.TestCleanupComplete
			if failure != "" {
				wantState = domain.TestCleanupFailed
			}
			if err != nil || !ok || rec.Phase != domain.TestAttemptFinished || rec.Outcome != domain.TestOutcomeCancelled || rec.CancelledAt == nil || rec.CleanupState != wantState {
				t.Fatal("shutdown did not persist terminal cleanup", rec, err)
			}
			wantGap := ""
			if failure == "recording_deadline" {
				wantGap = context.DeadlineExceeded.Error()
			}
			if rec.RecordingGap != wantGap {
				t.Fatal("shutdown recording gap", rec.RecordingGap)
			}
			receipts, err := f.svc.ListEvidence(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			kinds := map[string]bool{}
			dir := filepath.Join(f.dir, "testing", string(f.run.ID), string(rec.ID))
			for _, receipt := range receipts {
				kinds[receipt.Kind] = true
				if receipt.Kind == "recording_metadata" {
					data, err := os.ReadFile(filepath.Join(dir, receipt.RelativePath))
					var result ports.TestingRecordingResult
					if err != nil || json.Unmarshal(data, &result) != nil || result.Gap != wantGap {
						t.Fatal("shutdown recording metadata receipt missing", err)
					}
				}
			}
			for _, kind := range []string{"journal", "recording_metadata", "final_logs", "cleanup"} {
				if !kinds[kind] {
					t.Fatal("shutdown evidence missing", kind)
				}
			}
			if kinds["recording"] != (failure != "recording_deadline") {
				t.Fatal("shutdown recording receipt", receipts)
			}
			if _, err := f.call("after-close", "screenshot", map[string]any{}); code(err) != "TEST_WORKER_NOT_RUNNING" {
				t.Fatal("shutdown tool refusal was not explicit", err)
			}
		})
	}
}

func TestExpiredCleanupContextStillSavesRecordingGapAndStopsTarget(t *testing.T) {
	f, _ := movieFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Simulate cleanup already running when the supervisor begins Close.
	f.svc.mu.Lock()
	st := f.svc.attempts[f.start.AttemptID]
	now := f.clock.Now()
	st.record.Phase = domain.TestAttemptFinished
	st.record.Outcome = domain.TestOutcomeCancelled
	st.record.CancelledAt, st.record.FinishedAt = &now, &now
	st.cancel()
	st.timer.Stop()
	st.cleanupRunning = true
	<-st.gate // the cancelled in-flight operation has not released its gate
	if err := f.store.UpdateTestAttempt(context.Background(), st.record); err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Unlock()
	go f.svc.cleanup(ctx, st, st.cleanupDone)
	if err := f.svc.Close(); !errors.Is(err, context.Canceled) {
		t.Fatal("expired cleanup failure hidden", err)
	}
	st.gate <- struct{}{}
	rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.CleanupState != domain.TestCleanupFailed || !strings.Contains(rec.RecordingGap, context.Canceled.Error()) || f.provider.stops != 1 {
		t.Fatal("expired cleanup skipped gap persistence or target stop", rec, err)
	}
	receipts, err := f.svc.ListEvidence(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if receipt.Kind == "recording_metadata" {
			return
		}
	}
	t.Fatal("expired cleanup lost final recording metadata")
}

func TestRecordingFailureSetsActionableGapAndStillStopsTarget(t *testing.T) {
	for _, failure := range []string{"provider_stop", "recording", "recording_metadata", "foreign_path"} {
		t.Run(failure, func(t *testing.T) {
			f, desktop := movieFixture(t, nil)
			switch failure {
			case "provider_stop":
				desktop.stopErr = errors.New("stop failed")
			case "foreign_path":
				desktop.escapePath = filepath.Join(t.TempDir(), "foreign.mov")
			default:
				f.evidence.failKind = failure
			}
			_, _ = f.svc.Cancel(context.Background(), f.start.AttemptID)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := f.svc.WaitCleanup(ctx, f.start.AttemptID); err == nil {
				t.Fatal("recording failure reported successful cleanup")
			}
			rec, _, err := f.store.GetTestAttempt(ctx, f.start.AttemptID)
			wantCause := ""
			switch failure {
			case "provider_stop":
				wantCause = "stop failed"
			case "foreign_path":
				wantCause = "outside attempt evidence storage"
			case "recording":
				wantCause = "Cannot save finalized recording: disk full"
			case "recording_metadata":
				wantCause = "Cannot save recording metadata: disk full"
			}
			if err != nil || !strings.Contains(rec.RecordingGap, wantCause) || rec.CleanupState != domain.TestCleanupFailed || f.provider.stops != 1 {
				t.Fatal("recording failure misclassified gap or skipped target stop", rec, err)
			}
			journal, err := os.ReadFile(filepath.Join(f.dir, "testing", string(f.run.ID), string(rec.ID), "actions.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			foundFailure := false
			for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
				var record domain.TestActionRecord
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record.Tool == "stop_recording" && record.State == "failed" {
					foundFailure = record.Detail != "" && record.RecordingGap == rec.RecordingGap && strings.Contains(record.Detail, wantCause)
				}
			}
			if !foundFailure {
				t.Fatal("recording failure reason missing from journal")
			}
		})
	}
}

type exhaustedMovieCopy struct{ ports.TestingEvidenceStore }

func (e exhaustedMovieCopy) Write(ctx context.Context, id domain.TestAttemptID, artifact ports.TestingEvidenceArtifact, data io.Reader) (domain.TestEvidenceReceipt, error) {
	if artifact.Kind == "recording" {
		<-ctx.Done()
		return domain.TestEvidenceReceipt{}, ctx.Err()
	}
	return e.TestingEvidenceStore.Write(ctx, id, artifact, data)
}

func TestRecordingCopyBudgetDoesNotConsumeTerminalDiagnosticsBudget(t *testing.T) {
	f, _ := movieFixture(t, nil)
	f.svc.deps.Evidence = exhaustedMovieCopy{TestingEvidenceStore: f.evidence}
	_, _ = f.svc.Cancel(context.Background(), f.start.AttemptID)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := f.svc.WaitCleanup(ctx, f.start.AttemptID); err == nil {
		t.Fatal("copy timeout reported success")
	}
	rec, _, err := f.store.GetTestAttempt(ctx, f.start.AttemptID)
	if err != nil || !strings.Contains(rec.RecordingGap, "context deadline exceeded") || f.provider.stops != 1 {
		t.Fatal("copy failure lost gap or target cleanup", rec, err)
	}
	receipts, err := f.svc.ListEvidence(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := false
	for _, receipt := range receipts {
		if receipt.Kind == "recording" {
			t.Fatal("timed-out copy published receipt")
		}
		if receipt.Kind == "recording_metadata" {
			metadata = true
		}
	}
	journal, err := os.ReadFile(filepath.Join(f.dir, "testing", string(f.run.ID), string(rec.ID), "actions.jsonl"))
	if err != nil || !metadata {
		t.Fatal("copy budget swallowed diagnostics", err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
		var record domain.TestActionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Tool == "stop_recording" && record.State == "failed" && strings.Contains(record.Detail, "context deadline exceeded") && record.RecordingGap == rec.RecordingGap {
			return
		}
	}
	t.Fatal("copy timeout lost completion journal")
}

func TestRecordingStartErrorRetainsReasonUntilSuccessfulStop(t *testing.T) {
	f, desktop := movieFixture(t, func(d *movieDesktop) {
		d.startErr = errors.New("cannot observe recorder process birth")
	})
	rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.RecordingGap != desktop.startErr.Error() {
		t.Fatal("StartRecording error reason was not retained", rec.RecordingGap, err)
	}
	_, _ = f.svc.Cancel(context.Background(), f.start.AttemptID)
	f.wait(t)
	rec, _, err = f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.RecordingGap != "" {
		t.Fatal("successful finalization retained startup gap", rec.RecordingGap, err)
	}
}

func TestRecordingSuccessfulProviderCannotDeclareGap(t *testing.T) {
	f, _ := movieFixture(t, func(d *movieDesktop) {
		d.startGap = "legacy gap without a start error"
		d.stopGap = "legacy gap without a stop error"
	})
	rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.RecordingGap != "" {
		t.Fatal("successful StartRecording declared a gap", rec.RecordingGap, err)
	}
	_, _ = f.svc.Cancel(context.Background(), f.start.AttemptID)
	f.wait(t)
	rec, _, err = f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.RecordingGap != "" {
		t.Fatal("successful StopRecording declared a gap", rec.RecordingGap, err)
	}
	receipts, err := f.svc.ListEvidence(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if receipt.Kind == "recording" {
			return
		}
	}
	t.Fatal("successful recording was not saved as video evidence")
}
