package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type observedDesktop struct {
	*fakeProviders
	change bool
}

func TestObservationDigestUsesOriginalPixelsAndIgnoresCaptureIDs(t *testing.T) {
	shot := domain.TestScreenshot{Data: []byte("unchanged preview"), Original: &domain.TestScreenshot{Data: []byte("original1")},
		Elements: []domain.TestElement{{ElementID: "capture1", Label: "Settings"}}}
	first := observationDigest(&shot)
	shot.Elements[0].ElementID = "capture2"
	if observationDigest(&shot) != first {
		t.Fatal("ephemeral ID prevents settling")
	}
	shot.Original.Data = []byte("original2")
	if observationDigest(&shot) == first {
		t.Fatal("full-resolution change hidden by preview")
	}
	shot.Original.Data = []byte("original1")
	shot.Elements[0].Label = "Clone"
	if observationDigest(&shot) == first {
		t.Fatal("AX change hidden by identical pixels")
	}
}

func (d *observedDesktop) Screenshot(ctx context.Context, target domain.TestTargetIdentity) (domain.TestScreenshot, error) {
	shot, err := d.fakeProviders.Screenshot(ctx, target)
	if err != nil {
		return shot, err
	}
	d.mu.Lock()
	n := d.shots
	d.mu.Unlock()
	label := "Settings"
	if d.change {
		label = fmt.Sprintf("Settings-%d", n)
	}
	shot.Elements = []domain.TestElement{{ElementID: fmt.Sprintf("element-%d", n), Role: "AXButton", Label: label,
		Frame: domain.TestWindowBounds{Width: 1, Height: 1}}}
	return shot, nil
}

func TestObserveAndOneInputReturnSavedFreshObservation(t *testing.T) {
	f := newFixture(t, func(deps *Deps) { deps.Desktop = &observedDesktop{fakeProviders: deps.Desktop.(*fakeProviders)} })
	first, err := f.call("observe", "observe", domain.TestObserveRequest{})
	if err != nil || first.ObservationStatus != "captured" || len(first.Screenshot.Elements) != 1 {
		t.Fatal(first, err)
	}
	id := first.Screenshot.Frame.ScreenshotID
	result, err := f.call("click", "click", domain.TestClickRequest{ScreenshotID: id, ElementID: first.Screenshot.Elements[0].ElementID})
	if err != nil || result.Action == nil || !result.Action.Delivered || result.Screenshot == nil || result.ObservationStatus != "settled" {
		t.Fatal(result, err)
	}
	if f.provider.clicks != 1 || f.provider.shots != 3 || result.Screenshot.Frame.ScreenshotID == id || len(result.Evidence) != 5 {
		t.Fatal("input repeated or observation not saved", f.provider.clicks, f.provider.shots, result)
	}
	if _, err := f.call("repeat-old", "click", domain.TestClickRequest{ScreenshotID: id, ElementID: first.Screenshot.Elements[0].ElementID}); err == nil || f.provider.clicks != 1 {
		t.Fatal("consumed observation accepted", err)
	}
	dir := filepath.Join(f.dir, "testing", string(f.run.ID), string(f.start.AttemptID))
	for _, receipt := range result.Evidence {
		if receipt.Kind != "observation" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, receipt.RelativePath))
		if err != nil || !bytes.Contains(data, []byte(`"elements"`)) || bytes.Contains(data, []byte("private-capture-receipt")) {
			t.Fatal("observation missing or leaked receipt", err, string(data))
		}
	}
}

func TestPostInputCaptureOrEvidenceFailureRetainsDeliveryWithoutRetry(t *testing.T) {
	for _, failure := range []string{"capture", "screenshot", "observation", "delivery", "completed"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			shot, err := f.call("observe", "observe", domain.TestObserveRequest{})
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "capture":
				f.provider.screenshotHook = func(context.Context) error { return errors.New("provider failed") }
			case "completed":
				f.evidence.failState = failure
			default:
				f.evidence.failKind = failure
			}
			result, err := f.call("click", "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID})
			if err != nil || result.Action == nil || !result.Action.Delivered || result.ObservationStatus != "failed" || result.ObservationError == "" || f.provider.clicks != 1 {
				t.Fatal("delivery hidden or input repeated", result, err)
			}
			if _, err := f.call("click", "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID}); code(err) != "DUPLICATE_TEST_REQUEST" || f.provider.clicks != 1 {
				t.Fatal("failed capture replayed input", err)
			}
		})
	}
}

func TestPostInputCancellationRetainsDeliveryAndStopsCapture(t *testing.T) {
	f := newFixture(t)
	shot, err := f.call("observe", "observe", domain.TestObserveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	f.provider.screenshotHook = func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan ToolResult, 1)
	go func() {
		raw, _ := json.Marshal(domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID})
		result, _ := f.svc.Execute(ctx, f.start.AttemptID, f.start.WorkerSessionID, f.worker.binding.Capability, "click", "click", raw)
		done <- result
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not start")
	}
	cancel()
	select {
	case result := <-done:
		if result.Action == nil || !result.Action.Delivered || result.ObservationStatus != "failed" || f.provider.clicks != 1 {
			t.Fatal(result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled capture stalled")
	}
}

func TestSettleDeadlineDuringSecondCaptureRetainsVisualEvidenceWithoutInputIDs(t *testing.T) {
	f := newFixture(t, func(deps *Deps) { deps.PostActionCaptureTimeout = 200 * time.Millisecond })
	shot, err := f.call("observe", "observe", domain.TestObserveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	captures := 0
	f.provider.screenshotHook = func(ctx context.Context) error {
		captures++
		if captures == 1 {
			timer := time.NewTimer(20 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	result, err := f.call("click", "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID})
	if err != nil || result.Action == nil || !result.Action.Delivered || result.ObservationStatus != "unsettled" || result.ObservationError != "TEST_SETTLE_TIMEOUT" || result.Screenshot == nil || result.Screenshot.InputReady || captures != 2 || f.provider.clicks != 1 {
		t.Fatal("settlement timeout lost observation or delivery", result, err, captures)
	}
	if _, err := f.call("invalidated", "click", domain.TestClickRequest{ScreenshotID: result.Screenshot.Frame.ScreenshotID}); err == nil || f.provider.clicks != 1 {
		t.Fatal("invalidated receipt accepted", err)
	}
}

func TestChangingAXWithIdenticalPixelsRemainsUnsettled(t *testing.T) {
	f := newFixture(t, func(deps *Deps) {
		deps.Desktop = &observedDesktop{fakeProviders: deps.Desktop.(*fakeProviders), change: true}
		deps.PostActionCaptureTimeout = 200 * time.Millisecond
	})
	shot, err := f.call("observe", "observe", domain.TestObserveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := f.call("key", "key", domain.TestKeyRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID, Keys: []string{"Enter"}})
	if err != nil || result.ObservationStatus != "unsettled" || result.Screenshot == nil || f.provider.clicks != 1 {
		t.Fatal(result, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("settling exceeded bound")
	}
}

func TestSlowNativeCapturesCompleteAfterOneInput(t *testing.T) {
	f := newFixture(t)
	shot, err := f.call("observe", "observe", domain.TestObserveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.provider.screenshotHook = func(ctx context.Context) error {
		timer := time.NewTimer(1100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result, err := f.call("click", "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID})
	if err != nil || result.ObservationStatus != "settled" || result.Screenshot == nil || f.provider.clicks != 1 {
		t.Fatal("slow capture lost delivery or retried input", result, err)
	}
}

func TestElementAddressingInputValidation(t *testing.T) {
	for _, tool := range []string{"click", "type"} {
		text := ""
		if tool == "type" {
			text = `,"text":"hello"`
		}
		for _, addressing := range []string{``, `,"x":0`, `,"elementId":""`, `,"elementId":"e","x":0,"y":0`, `,"elementId":null`, `,"elementId":"` + strings.Repeat("x", 129) + `"`} {
			if _, _, err := decodeInput(json.RawMessage(`{"screenshotId":"s"`+addressing+text+`}`), tool); err == nil {
				t.Fatal("invalid address accepted", tool, addressing)
			}
		}
		for _, addressing := range []string{`,"x":0,"y":0`, `,"elementId":"e"`} {
			if _, _, err := decodeInput(json.RawMessage(`{"screenshotId":"s"`+addressing+text+`}`), tool); err != nil {
				t.Fatal("valid address refused", tool, addressing, err)
			}
		}
	}
}
