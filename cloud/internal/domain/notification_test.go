package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNotificationTypeValidAllowsOnlyAgentEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typeValue NotificationType
		want      bool
	}{
		{name: "needs input", typeValue: NotificationTypeNeedsInput, want: true},
		{name: "agent failed", typeValue: NotificationTypeAgentFailed, want: true},
		{name: "agent completed", typeValue: NotificationTypeAgentCompleted, want: true},
		{name: "empty", typeValue: "", want: false},
		{name: "phase two scm event", typeValue: "pull_request_created", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.typeValue.Valid(); got != tt.want {
				t.Fatalf("NotificationType(%q).Valid() = %v, want %v", tt.typeValue, got, tt.want)
			}
		})
	}
}

func TestAgentNotificationEventContainsOnlyUntrustedEventData(t *testing.T) {
	t.Parallel()

	occurredAt := time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC)
	event := AgentNotificationEvent{
		EventID:    "evt_01KTEST",
		Type:       NotificationTypeNeedsInput,
		OccurredAt: occurredAt,
		Payload:    json.RawMessage(`{"activityId":"tool-7"}`),
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"eventId":"evt_01KTEST","type":"needs_input","occurredAt":"2026-09-21T10:00:00Z","payload":{"activityId":"tool-7"}}`
	if string(encoded) != want {
		t.Fatalf("json.Marshal(event) = %s, want %s", encoded, want)
	}
}

func TestNotificationStatusValid(t *testing.T) {
	t.Parallel()

	if !NotificationStatusUnread.Valid() || !NotificationStatusRead.Valid() {
		t.Fatal("unread and read must be valid notification statuses")
	}
	if NotificationStatus("resolved").Valid() {
		t.Fatal("resolved is represented by resolvedAt, not an inbox status")
	}
}

func TestNotificationJSONUsesCloudAPIFieldNames(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Notification{
		ID: "notification-1", OrgID: "org-1", RecipientUserID: "user-1",
		ProjectID: "project-1", SessionID: "session-1", Source: "cloud",
		Type: "needs_input", Title: "Agent needs input", Body: "Choose an option",
		DedupeKey: "needs-input:session-1:tool-1", Status: "unread",
		EventID: "evt-1", Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "orgId", "recipientUserId", "projectId", "sessionId", "source", "eventId", "createdAt"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("encoded notification lacks %q: %s", key, encoded)
		}
	}
	if _, leaked := fields["OrgID"]; leaked {
		t.Fatalf("encoded notification leaked Go field names: %s", encoded)
	}
}
