package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestInputFailureRetainsPrivateCauseAndSafeWireClassification(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint("refused=", refused), func(t *testing.T) {
			private := errors.New("AX lookup failed; password=private-password prompt=private-prompt")
			cause := errors.Join(private, context.Canceled)
			wantCode, wantStatus := "TEST_INPUT_FAILED", http.StatusServiceUnavailable
			wantMessage := "Target input failed; delivery is unverified"
			if refused {
				cause = errors.Join(cause, ports.ErrTestingInputRefused)
				wantCode, wantStatus = "TEST_INPUT_REFUSED", http.StatusConflict
				wantMessage = "Target input refused before dispatch; nothing was sent"
			}
			err := inputFailure(cause)
			var failure *apierr.Error
			if !errors.Is(err, private) || !errors.Is(err, context.Canceled) || !errors.As(err, &failure) || failure.Code != wantCode || failure.Message != wantMessage {
				t.Fatalf("input cause or stable API classification lost: %v", err)
			}
			if refused && !errors.Is(err, ports.ErrTestingInputRefused) {
				t.Fatal("refusal sentinel lost", err)
			}
			req, captured := envelope.WithErrorCapture(httptest.NewRequest(http.MethodPost, "/private-testing-tool", nil))
			response := httptest.NewRecorder()
			envelope.WriteError(response, req, err)
			var wire envelope.APIError
			if json.Unmarshal(response.Body.Bytes(), &wire) != nil || response.Code != wantStatus || wire.Code != wantCode || wire.Message != wantMessage || wire.Details != nil {
				t.Fatalf("public error envelope changed: %s", response.Body.String())
			}
			for _, private := range []string{"AX lookup failed", "private-password", "private-prompt"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("provider data leaked into safe wire error: %q", private)
				}
			}
			if !errors.Is(captured().Err, private) || !errors.Is(captured().Err, context.Canceled) {
				t.Fatal("request logger lost the original cause")
			}
		})
	}
}

func TestPostActionCaptureFailureLogsCorrelatedCauseWithoutChangingDelivery(t *testing.T) {
	var logs bytes.Buffer
	f := newFixture(t, func(deps *Deps) { deps.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	shot, err := f.call("before-input", "observe", domain.TestObserveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.provider.screenshotHook = func(context.Context) error {
		return errors.New("cua provider_failure: call get_window_state; exit status 1; AX window unavailable; password=private-password")
	}
	result, err := f.call("input-capture-failure", "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID})
	if err != nil || result.Action == nil || !result.Action.Delivered || result.ObservationStatus != "failed" || result.ObservationError != "TEST_SCREENSHOT_FAILED" || result.Screenshot != nil || f.provider.clicks != 1 {
		t.Fatalf("post-action capture failure changed delivery or repeated input: %+v err=%v", result, err)
	}
	for _, want := range []string{"testing post-action observation failed", string(f.start.AttemptID), "input-capture-failure", "call get_window_state", "exit status 1", "AX window unavailable"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("private log lost correlation or cause %q: %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "private-password") {
		t.Fatal("private log leaked credentials")
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"AX window unavailable", "get_window_state", "exit status 1", "private-password"} {
		if bytes.Contains(wire, []byte(private)) {
			t.Fatalf("private observation cause leaked into the successful tool result: %q", private)
		}
	}
	if _, err := f.call("input-capture-failure", "click", domain.TestClickRequest{ScreenshotID: shot.Screenshot.Frame.ScreenshotID}); code(err) != "DUPLICATE_TEST_REQUEST" || f.provider.clicks != 1 {
		t.Fatal("failed post-action capture redispatched delivered input", err)
	}
}
