package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	reportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/report"
)

type fakeReportService struct {
	input reportsvc.CreateInput
	err   error
}

func (f *fakeReportService) Create(_ context.Context, input reportsvc.CreateInput) (domain.ReportRecord, error) {
	f.input = input
	if f.err != nil {
		return domain.ReportRecord{}, f.err
	}
	return domain.ReportRecord{ID: "rpt_1", SessionID: input.SessionID, ProjectID: "ao", State: input.State, Note: input.Note, Message: input.Message, Outputs: input.Outputs, CreatedAt: time.Unix(1, 0).UTC(), DeliveryState: domain.ReportPending}, nil
}

func TestReportsAPI_CreateAndEnvelope(t *testing.T) {
	svc := &fakeReportService{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Reports: svc}, httpd.ControlDeps{}))
	defer srv.Close()
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/reports", `{"sessionId":"ao-7","state":"done","note":"finished","outputs":[{"kind":"artifact","reference":"result","label":"Result"},{"kind":"pr_reviewed","reference":"https://github.com/o/r/pull/7"}]}`)
	if status != http.StatusCreated || svc.input.SessionID != "ao-7" || svc.input.State != domain.ReportDone || svc.input.Note != "finished" || len(svc.input.Outputs) != 2 || svc.input.Outputs[0].Label != "Result" || svc.input.Outputs[1].Kind != domain.ReportOutputPRReviewed {
		t.Fatalf("status=%d body=%s svc=%+v", status, body, svc)
	}
	if string(body) != `{"id":"rpt_1"}`+"\n" {
		t.Fatalf("response body = %s", body)
	}
	svc.err = apierr.Invalid("INVALID_REPORT", "bad report", nil)
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/reports", `{"sessionId":"ao-7","state":"done","note":"x"}`)
	if status != http.StatusBadRequest || !reportContainsAll(string(body), "INVALID_REPORT", "requestId") {
		t.Fatalf("status=%d body=%s", status, body)
	}
}

func TestReportsAPI_InvalidJSON(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Reports: &fakeReportService{}}, httpd.ControlDeps{}))
	defer srv.Close()
	_, status, _ := doRequest(t, srv, "POST", "/api/v1/reports", `{"unknown":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d", status)
	}
}

func reportContainsAll(s string, values ...string) bool {
	for _, v := range values {
		if !strings.Contains(s, v) {
			return false
		}
	}
	return true
}
