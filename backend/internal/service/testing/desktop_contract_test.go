package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestFullFrameAndDeliveryPolicyReachInput(t *testing.T) {
	f := newFixture(t, func(deps *Deps) {
		deps.Desktop = &policyDesktop{fakeProviders: deps.Desktop.(*fakeProviders), mode: "foreground", gap: "declared window recording gap"}
	})
	for _, tool := range []string{"click", "type", "key"} {
		shot, err := f.call("shot-"+tool, "screenshot", domain.TestScreenshotRequest{})
		if err != nil {
			t.Fatal(err)
		}
		frame := shot.Screenshot.Frame
		var input any
		switch tool {
		case "click":
			input = domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 1, Y: 1}
		case "type":
			input = domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, Text: "hello"}
		case "key":
			input = domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"Enter"}}
		}
		if _, err := f.call("input-"+tool, tool, input); err != nil {
			t.Fatal(err)
		}
		got := f.provider.inputFrames[len(f.provider.inputFrames)-1]
		if frame.CaptureHandle == "" || frame.Scale != 2 || !reflect.DeepEqual(got, frame) {
			t.Fatalf("%s lost the adapter frame: %+v", tool, got)
		}
	}
	dir := filepath.Join(f.dir, "testing", string(f.run.ID), string(f.start.AttemptID))
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil || bytes.Contains(data, []byte("private-capture-receipt")) {
			t.Fatal("private capture receipt reached evidence", file.Name(), err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record domain.TestActionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Tool != "click" && record.Tool != "type" && record.Tool != "key" {
			continue
		}
		if record.ConfiguredDeliveryMode != "foreground" || record.DeliveryMode != "foreground" {
			t.Fatalf("input policy missing from %s journal: %+v", record.State, record)
		}
		counts[record.Tool]++
	}
	for _, tool := range []string{"click", "type", "key"} {
		if counts[tool] != 2 {
			t.Fatal("input missing journal pair", tool, counts)
		}
	}
	var recipe Recipe
	if err := json.Unmarshal([]byte(f.run.RecipeSnapshot), &recipe); err != nil || recipe.DeliveryMode != "foreground" {
		t.Fatal("recipe did not retain delivery policy", err)
	}
}

type inputPolicyDesktop struct {
	*policyDesktop
	inputMode string
}

func (d *inputPolicyDesktop) InputDeliveryMode(string) string { return d.inputMode }

func TestForegroundPolicyRejectsConfigurationAndInputFallback(t *testing.T) {
	f := newFixture(t)
	var recipe Recipe
	if err := json.Unmarshal([]byte(f.run.RecipeSnapshot), &recipe); err != nil || recipe.DeliveryMode != "foreground" {
		t.Fatal("default recipe did not require foreground", recipe, err)
	}
	for _, mode := range []string{"", "background"} {
		f.svc.deps.Desktop = &policyDesktop{fakeProviders: f.provider, mode: mode}
		if _, err := f.svc.StartAttempt(context.Background(), f.run.ID, StartAttemptInput{WorkerPrompt: "Investigate", Timeout: time.Minute}); code(err) != "TESTING_PROVIDER_NOT_CONFIGURED" || len(f.provider.launchSpecs) != 1 {
			t.Fatal("unsupported policy launched a target", mode, err)
		}
	}
	desktop := &inputPolicyDesktop{policyDesktop: &policyDesktop{fakeProviders: f.provider, mode: "foreground"}}
	f.svc.deps.Desktop = desktop
	for i, mode := range []string{"", "background"} {
		desktop.inputMode = mode
		shot, err := f.call(fmt.Sprintf("policy-shot-%d", i), "screenshot", domain.TestScreenshotRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.call(fmt.Sprintf("policy-click-%d", i), "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID, X: 1, Y: 1}); code(err) != "TEST_INPUT_REFUSED" || len(f.provider.inputFrames) != 0 {
			t.Fatal("unsupported input mode was dispatched", mode, err)
		}
	}
	journal, err := os.ReadFile(filepath.Join(f.dir, "testing", string(f.run.ID), string(f.start.AttemptID), "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	refused := 0
	for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
		var record domain.TestActionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Tool == "click" && record.State == "refused" {
			if record.ConfiguredDeliveryMode != "foreground" || record.DeliveryMode == "foreground" {
				t.Fatal("refused journal lost the configured or actual policy", record)
			}
			refused++
		}
	}
	if refused != 2 {
		t.Fatal("missing refused input receipts", refused)
	}
}

type originalScreenshotDesktop struct {
	*fakeProviders
	original domain.TestScreenshot
	corrupt  func(*domain.TestScreenshot)
}

func (d *originalScreenshotDesktop) Screenshot(ctx context.Context, target domain.TestTargetIdentity) (domain.TestScreenshot, error) {
	shot, err := d.fakeProviders.Screenshot(ctx, target)
	if err != nil {
		return shot, err
	}
	var full bytes.Buffer
	if err := png.Encode(&full, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		return shot, err
	}
	d.original = shot
	d.original.Data = full.Bytes()
	d.original.Frame.Width, d.original.Frame.Height = 4, 4
	if d.corrupt != nil {
		d.corrupt(&d.original)
	}
	shot.Original = &d.original
	return shot, nil
}

func TestScreenshotSavesOriginalEvidenceAndReturnsPreviewForInput(t *testing.T) {
	var desktop *originalScreenshotDesktop
	f := newFixture(t, func(deps *Deps) {
		desktop = &originalScreenshotDesktop{fakeProviders: deps.Desktop.(*fakeProviders)}
		deps.Desktop = desktop
	})
	result, err := f.call("preview", "screenshot", domain.TestScreenshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	shot := result.Screenshot
	geometry, err := png.DecodeConfig(bytes.NewReader(shot.Data))
	if err != nil || geometry.Width != 2 || geometry.Height != 2 || shot.Frame.Width != 2 || shot.Frame.Height != 2 {
		t.Fatal("worker did not receive the preview", shot.Frame, err)
	}
	dir := filepath.Join(f.dir, "testing", string(f.run.ID), string(f.start.AttemptID))
	receipt := result.Evidence[0]
	data, err := os.ReadFile(filepath.Join(dir, receipt.RelativePath))
	if err != nil || !bytes.Equal(data, desktop.original.Data) {
		t.Fatal("evidence did not preserve the original PNG", err)
	}
	metadata, err := os.ReadFile(filepath.Join(dir, receipt.ID+".receipt.json"))
	var saved struct{ Frame domain.TestDesktopFrame }
	if err != nil || json.Unmarshal(metadata, &saved) != nil || saved.Frame.Width != 4 || saved.Frame.Height != 4 || saved.Frame.CaptureHandle != "" || saved.Frame.Target.ID != "" {
		t.Fatal("original frame evidence metadata was lost or leaked private identity", string(metadata), err)
	}
	wire, err := json.Marshal(result)
	if err != nil || bytes.Contains(wire, []byte(`"Original"`)) || bytes.Contains(wire, []byte(`"original"`)) {
		t.Fatal("original screenshot leaked into the worker JSON", err)
	}
	if _, err := f.call("preview-click", "click", domain.TestClickRequest{ScreenshotID: shot.Frame.ScreenshotID, X: 1, Y: 1}); err != nil {
		t.Fatal(err)
	}
	if frame := f.provider.inputFrames[0]; !reflect.DeepEqual(frame, shot.Frame) {
		t.Fatal("input received the original frame instead of the preview frame", frame)
	}
}

func TestScreenshotRefusesInvalidOriginalEvidence(t *testing.T) {
	for _, corrupt := range []func(*domain.TestScreenshot){
		func(s *domain.TestScreenshot) { s.Frame.Target.WindowID = "foreign" },
		func(s *domain.TestScreenshot) { s.Frame.Width++ },
		func(s *domain.TestScreenshot) { s.Data = []byte("invalid PNG") },
	} {
		f := newFixture(t, func(deps *Deps) {
			deps.Desktop = &originalScreenshotDesktop{fakeProviders: deps.Desktop.(*fakeProviders), corrupt: corrupt}
		})
		result, err := f.call("invalid-original", "screenshot", domain.TestScreenshotRequest{})
		if code(err) != "TEST_TARGET_CHANGED" || len(result.Evidence) != 0 {
			t.Fatal("invalid original was saved as evidence", result, err)
		}
	}
}

func TestDeclaredRecordingGapPersistsAndReleasesBeforeTargetStop(t *testing.T) {
	var desktop *policyDesktop
	f := newFixture(t, func(deps *Deps) {
		desktop = &policyDesktop{fakeProviders: deps.Desktop.(*fakeProviders), mode: "foreground", gap: "main display recording is refused"}
		deps.Desktop = desktop
	})
	rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || rec.RecordingGap != desktop.gap || desktop.starts != 1 {
		t.Fatal("provider recording gap was not saved", rec.RecordingGap, err)
	}
	if _, err := f.svc.Cancel(context.Background(), f.start.AttemptID); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	if !reflect.DeepEqual(f.provider.cleanupEvents, []string{"desktop_release", "target_stop"}) {
		t.Fatal("gap provider was recorded or released after target stop", f.provider.cleanupEvents)
	}
	journal, err := os.ReadFile(filepath.Join(f.dir, "testing", string(f.run.ID), string(f.start.AttemptID), "actions.jsonl"))
	if err != nil || !bytes.Contains(journal, []byte(`"recordingGap":"main display recording is refused"`)) {
		t.Fatal("declared recording gap missing from journal", err)
	}
}

func TestRecordingJournalFailurePreventsWorkerLaunch(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Cancel(context.Background(), f.start.AttemptID)
	f.wait(t)
	f.evidence.failState = "dispatching"
	result, err := f.svc.StartAttempt(context.Background(), f.run.ID, StartAttemptInput{WorkerPrompt: "Investigate", Timeout: time.Minute})
	if code(err) != "TEST_EVIDENCE_WRITE_FAILED" || result.AttemptID == "" || f.worker.launches != 1 {
		t.Fatal("recording evidence failure launched a worker", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.svc.WaitCleanup(ctx, result.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func TestTargetLaunchReceivesRevisionAndFixtureSnapshot(t *testing.T) {
	f := newFixture(t, func(deps *Deps) {
		recipe := deps.Recipes["native"]
		recipe.VisualMarker = true
		deps.Recipes["native"] = recipe
	})
	spec := f.provider.launchSpecs[0]
	var fixture struct {
		VisualMarker bool `json:"visualMarker"`
	}
	if err := json.Unmarshal([]byte(spec.RecipeSnapshot), &fixture); err != nil || !fixture.VisualMarker {
		t.Fatal("fixture flag missing from target snapshot", err)
	}
	rec, _, err := f.store.GetTestAttempt(context.Background(), f.start.AttemptID)
	if err != nil || spec.AttemptID != rec.ID || spec.Generation != rec.LeaseGeneration || spec.CommitSHA != f.run.CommitSHA || spec.RecipeSnapshot != f.run.RecipeSnapshot || spec.CheckoutPath != f.dir || spec.StateRoot != filepath.Join(f.deps.TargetStateRoot, string(rec.ID)) || !spec.Deadline.Equal(rec.Deadline) {
		t.Fatal("target launch spec did not retain the run and attempt", err)
	}
}
