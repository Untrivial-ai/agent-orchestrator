package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type startupEventStore struct {
	Store
	events []domain.ClientEvent
	types  []string
}

func (s *startupEventStore) AppendSessionEvent(
	_ context.Context,
	_, _ string,
	eventType string,
	payload json.RawMessage,
) (domain.ClientEvent, error) {
	event := domain.ClientEvent{Type: eventType, Payload: payload}
	s.events = append(s.events, event)
	s.types = append(s.types, eventType)
	return event, nil
}

func TestWorkerStartupMilestonesAreValidatedAndAppended(t *testing.T) {
	store := &startupEventStore{}
	server := testServer(store)
	cases := []struct {
		eventType string
		payload   string
	}{
		{"checkout.started", `{"workerId":"w1","epoch":1}`},
		{"checkout.completed", `{"workerId":"w1","epoch":1}`},
		{"restore.started", `{"workerId":"w1","epoch":1}`},
		{"restore.completed", `{"workerId":"w1","epoch":1,"restored":false}`},
		{"workspace.ready", `{"workerId":"w1","epoch":1}`},
		{"agent.launch_started", `{"workerId":"w1","epoch":1}`},
		{"startup.failed", `{"workerId":"w1","epoch":1,"phase":"starting_agent","code":"HARNESS_LAUNCH_FAILED","message":"The coding harness could not start."}`},
	}
	for _, testCase := range cases {
		body := `{"type":"` + testCase.eventType + `","payload":` + testCase.payload + `}`
		response := httptest.NewRecorder()
		server.workerEvent(
			response,
			workerRequest(t, http.MethodPost, "/worker/events", body, "worker:event"),
		)
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, body = %s", testCase.eventType, response.Code, response.Body.String())
		}
	}
	if strings.Join(store.types, ",") != "checkout.started,checkout.completed,restore.started,restore.completed,workspace.ready,agent.launch_started,startup.failed" {
		t.Fatalf("event order = %v", store.types)
	}
}

func TestWorkerStartupMilestoneRejectsWrongEpochAndUnboundedFailure(t *testing.T) {
	server := testServer(&startupEventStore{})
	cases := []string{
		`{"type":"workspace.ready","payload":{"workerId":"w1","epoch":2}}`,
		`{"type":"restore.completed","payload":{"workerId":"w1","epoch":1}}`,
		`{"type":"startup.failed","payload":{"workerId":"w1","epoch":1,"phase":"starting_agent","code":"bad-code","message":"failed"}}`,
		`{"type":"startup.failed","payload":{"workerId":"w1","epoch":1,"phase":"allocating_workspace","code":"START_FAILED","message":"failed"}}`,
	}
	for _, body := range cases {
		response := httptest.NewRecorder()
		server.workerEvent(
			response,
			workerRequest(t, http.MethodPost, "/worker/events", body, "worker:event"),
		)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_EVENT_PAYLOAD") {
			t.Fatalf("body %s produced status %d: %s", body, response.Code, response.Body.String())
		}
	}
}
