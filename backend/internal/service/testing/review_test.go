package testing

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCloseCancelsAttemptSavesRecordingAndStopsProviders(t *testing.T) {
	f, _ := movieFixture(t, nil)
	if err := f.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.provider.cleanupEvents, []string{"recording_stop", "desktop_release", "target_stop", "desktop_close"}) {
		t.Fatal("shutdown skipped or reordered cleanup", f.provider.cleanupEvents)
	}
	rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.Phase != domain.TestAttemptFinished || rec.Outcome != domain.TestOutcomeCancelled || rec.CancelledAt == nil || rec.CleanupState != domain.TestCleanupComplete {
		t.Fatal("shutdown did not persist cancelled cleanup", rec, err)
	}
	if _, err := f.call("closed", "screenshot", domain.TestScreenshotRequest{}); code(err) != "TEST_WORKER_NOT_RUNNING" {
		t.Fatal("shutdown retained a capability", err)
	}
	if err := f.svc.Close(); err != nil || len(f.provider.cleanupEvents) != 4 {
		t.Fatal("second close repeated cleanup", err)
	}
	restarted := New(f.deps)
	defer restarted.Close()
	if _, err := restarted.IssueCapability(context.Background(), f.start.WorkerSessionID); code(err) != "TEST_ATTEMPT_INACTIVE" {
		t.Fatal("supervisor restart revived the shutdown attempt", err)
	}
	receipts, err := restarted.ListEvidence(context.Background(), rec.ID)
	if err != nil || len(receipts) < 3 {
		t.Fatal("shutdown evidence was lost", err)
	}
}

func TestCancelRetriesFailedCleanupWithoutRepeatingInput(t *testing.T) {
	f := newFixture(t, func(deps *Deps) {
		deps.Desktop = &onceReleasedDesktop{policyDesktop: &policyDesktop{fakeProviders: deps.Desktop.(*fakeProviders), mode: "foreground", gap: "no recorder"}}
	})
	f.provider.stopFail = true
	_, _ = f.svc.Cancel(context.Background(), f.start.AttemptID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.svc.WaitCleanup(ctx, f.start.AttemptID); err == nil {
		t.Fatal("initial failed cleanup hidden")
	}
	f.provider.stopFail = false
	if _, err := f.svc.Cancel(ctx, f.start.AttemptID); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	rec, _, err := f.store.GetTestAttempt(ctx, f.start.AttemptID)
	if err != nil || rec.CleanupState != domain.TestCleanupComplete || f.provider.stops != 2 || f.provider.shots != 0 || f.provider.clicks != 0 {
		t.Fatal("second stop did not retry cleanup only", rec, err)
	}
}

type onceReleasedDesktop struct {
	*policyDesktop
	released bool
}

func (d *onceReleasedDesktop) Release(context.Context, domain.TestTargetIdentity) error {
	if d.released {
		return errors.New("target desktop session already released")
	}
	d.released = true
	return nil
}

func TestCloseCancelsInFlightToolBeforeStoppingTarget(t *testing.T) {
	f := newFixture(t)
	entered, exited := make(chan struct{}), make(chan error, 1)
	f.provider.screenshotHook = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		_, err := f.call("inflight", "screenshot", domain.TestScreenshotRequest{})
		exited <- err
	}()
	<-entered
	if err := f.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-exited; err == nil || f.provider.stops != 1 {
		t.Fatal("shutdown did not cancel and join the in-flight tool", err)
	}
}

func TestInputRefusalIsDistinctFromUnverifiedDelivery(t *testing.T) {
	for _, tool := range []string{"click", "type", "key"} {
		for _, refused := range []bool{true, false} {
			t.Run(tool+"/"+map[bool]string{true: "refused", false: "uncertain"}[refused], func(t *testing.T) {
				f := newFixture(t)
				shot, err := f.call("shot", "screenshot", domain.TestScreenshotRequest{})
				if err != nil {
					t.Fatal(err)
				}
				f.provider.inputErr = errors.New("provider input failed")
				wantCode, wantState := "TEST_INPUT_FAILED", `"state":"failed"`
				if refused {
					f.provider.inputErr = ports.ErrTestingInputRefused
					wantCode, wantState = "TEST_INPUT_REFUSED", `"state":"refused"`
				}
				id := shot.Screenshot.Frame.ScreenshotID
				input := map[string]any{"click": domain.TestClickRequest{ScreenshotID: id}, "type": domain.TestTypeRequest{ScreenshotID: id, Text: "hello"}, "key": domain.TestKeyRequest{ScreenshotID: id, Keys: []string{"Enter"}}}[tool]
				if _, err := f.call("input", tool, input); code(err) != wantCode {
					t.Fatal("input failure class was lost", err)
				}
				journal, err := os.ReadFile(filepath.Join(f.dir, "testing", string(f.run.ID), string(f.start.AttemptID), "actions.jsonl"))
				if err != nil || !bytes.Contains(journal, []byte(wantState)) {
					t.Fatal("input failure journal missing", err)
				}
				if refused && (!bytes.Contains(journal, []byte("nothing was sent")) || bytes.Contains(journal, []byte("partial action may have been delivered"))) {
					t.Fatal("refusal claimed uncertain delivery")
				}
			})
		}
	}
}
