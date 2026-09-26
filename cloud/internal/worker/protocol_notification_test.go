package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTerminalNotificationFrameKeepsNotificationSeparateFromPTYBytes(t *testing.T) {
	t.Parallel()
	frame := TerminalStreamFrame{
		Type: "notification", EventID: "evt-1", EventType: "needs_input",
		OccurredAt: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), Payload: json.RawMessage(`{"activityId":"tool-1"}`),
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"eventId":"evt-1"`, `"eventType":"needs_input"`, `"occurredAt":"2026-09-21T00:00:00Z"`, `"payload":{"activityId":"tool-1"}`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("notification field %s missing from %s", field, encoded)
		}
	}
	if strings.Contains(string(encoded), `"data"`) {
		t.Fatalf("notification frame leaked terminal bytes: %s", encoded)
	}
}
