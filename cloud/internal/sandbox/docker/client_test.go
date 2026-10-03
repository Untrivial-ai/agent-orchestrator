package docker

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

func TestLabelsForIncludesProviderExtraLabels(t *testing.T) {
	client := &Client{
		namespace:   "measurement",
		extraLabels: map[string]string{"ao.session": "session-owner"},
	}
	labels, _, err := client.labelsFor(sandbox.Spec{
		SessionID: "00000000-0000-0000-0000-000000000001",
		OrgID:     "00000000-0000-0000-0000-000000000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := labels["ao.session"]; got != "session-owner" {
		t.Fatalf("ao.session label = %q, want session-owner", got)
	}
}

func TestLabelsForRejectsSpecConflictWithProviderExtraLabel(t *testing.T) {
	client := &Client{
		namespace:   "measurement",
		extraLabels: map[string]string{"ao.session": "session-owner"},
	}
	_, _, err := client.labelsFor(sandbox.Spec{
		SessionID: "00000000-0000-0000-0000-000000000001",
		OrgID:     "00000000-0000-0000-0000-000000000002",
		Labels:    map[string]string{"ao.session": "another-owner"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with the provider value") {
		t.Fatalf("labelsFor error = %v, want provider-label conflict", err)
	}
}

func TestNewRejectsInvalidExtraLabel(t *testing.T) {
	_, err := New(Config{
		WorkerImage: "worker:test",
		Namespace:   "measurement",
		ExtraLabels: map[string]string{"": "owner"},
	})
	if err == nil || !strings.Contains(err.Error(), "extra label") {
		t.Fatalf("New error = %v, want invalid extra label", err)
	}
}
