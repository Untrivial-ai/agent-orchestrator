package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type cleanupTransport func(*http.Request) (*http.Response, error)

func (fn cleanupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestRejectedCreateRetainsWorkspaceUntilCancellation(t *testing.T) {
	for _, test := range []struct {
		name                              string
		lostVolumeResponse, foreignVolume bool
	}{
		{name: "missing image"}, {name: "lost volume response", lostVolumeResponse: true}, {name: "foreign volume", foreignVolume: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var volume *volumeView
			created, deleted := 0, 0
			failDelete := true
			client := &Client{baseURL: "http://docker.test", namespace: "review", image: "missing:tag"}
			client.http = &http.Client{Transport: cleanupTransport(func(r *http.Request) (*http.Response, error) {
				code, body := http.StatusOK, any(nil)
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/volumes/create":
					if err := json.NewDecoder(r.Body).Decode(&volume); err != nil {
						return nil, err
					}
					created++
					if test.lostVolumeResponse {
						return nil, context.DeadlineExceeded
					}
					body = volume
				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/volumes/"):
					if volume == nil {
						code = http.StatusNotFound
					} else {
						body = volume
					}
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/volumes/"):
					if failDelete {
						code = http.StatusInternalServerError
					} else {
						volume = nil
						deleted++
					}
				case r.URL.Path == "/containers/create":
					code = http.StatusNotFound
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				encoded, _ := json.Marshal(body)
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
			})}
			spec := sandbox.Spec{SessionID: "11111111-1111-1111-1111-111111111111", OrgID: "22222222-2222-2222-2222-222222222222"}
			_, err := client.Create(context.Background(), spec)
			if err == nil || errors.Is(err, sandbox.ErrCreateRejected) == test.lostVolumeResponse {
				t.Fatalf("create classification=%v", err)
			}
			if volume == nil || created != 1 {
				t.Fatal("partial workspace missing")
			}
			if test.lostVolumeResponse {
				return
			} // The creation journal must retain uncertain writes.
			_, _ = client.Create(context.Background(), spec)
			if created != 1 {
				t.Fatal("retry replaced the durable workspace")
			}
			if test.foreignVolume {
				volume.Labels[labelOrgID] = "different-owner"
			}
			if err := client.CleanupSession(context.Background(), spec.OrgID, spec.SessionID); err == nil {
				t.Fatal("failed cleanup was treated as complete")
			}
			if volume == nil {
				t.Fatal("failed cleanup lost the workspace")
			}
			failDelete = false
			err = client.CleanupSession(context.Background(), spec.OrgID, spec.SessionID)
			if test.foreignVolume {
				if err == nil || deleted != 0 {
					t.Fatal("foreign workspace was deleted")
				}
				return
			}
			if err != nil || volume != nil || deleted != 1 {
				t.Fatalf("cleanup err=%v volume=%v deletes=%d", err, volume, deleted)
			}
			if err := client.CleanupSession(context.Background(), spec.OrgID, spec.SessionID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

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
