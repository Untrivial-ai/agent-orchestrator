package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPostHogSinkCapturesEvent(t *testing.T) {
	requests := make(chan map[string]any, 1)
	sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", "", "", roundTripClient(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests <- body
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	}), nil)
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}

	projectID := domain.ProjectID("proj-1")
	sessionID := domain.SessionID("sess-1")
	sink.Emit(context.Background(), ports.TelemetryEvent{
		Name:       "ao.session.spawned",
		Source:     "session_service",
		OccurredAt: time.Unix(1700000000, 0).UTC(),
		Level:      ports.TelemetryLevelInfo,
		ProjectID:  &projectID,
		SessionID:  &sessionID,
		RequestID:  "req-1",
		Payload: map[string]any{
			"kind": "worker",
		},
	})
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case req := <-requests:
		if got := req["event"]; got != "ao.session.spawned" {
			t.Fatalf("event = %#v, want ao.session.spawned", got)
		}
		props, ok := req["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties type = %T, want map[string]any", req["properties"])
		}
		if props["kind"] != "worker" {
			t.Fatalf("properties.kind = %#v, want worker", props["kind"])
		}
		if props["project_id_hash"] == "" || props["session_id_hash"] == "" {
			t.Fatalf("hashed ids missing from properties: %#v", props)
		}
		if props["$process_person_profile"] != false {
			t.Fatalf("properties.$process_person_profile = %#v, want false", props["$process_person_profile"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PostHog sink did not send request")
	}
}

func TestPostHogSinkSanitizesPayloads(t *testing.T) {
	requests := make(chan map[string]any, 1)
	sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", "", "", roundTripClient(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests <- body
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	}), nil)
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}

	sink.Emit(context.Background(), ports.TelemetryEvent{
		Name:       "ao.daemon.panic",
		Source:     "http",
		OccurredAt: time.Unix(1700000000, 0).UTC(),
		Level:      ports.TelemetryLevelError,
		Payload: map[string]any{
			"component":         "httpd",
			"operation":         "http_request_panic",
			"method":            http.MethodGet,
			"path":              "/api/v1/sessions/demo",
			"panic_kind":        "error",
			"fingerprint":       "abc123",
			"stack_fingerprint": "def456",
			"panic":             "open /Users/name/private: no such file",
			"stack":             "stack trace with local path",
		},
	})
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case req := <-requests:
		props, ok := req["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties type = %T, want map[string]any", req["properties"])
		}
		if props["component"] != "httpd" || props["operation"] != "http_request_panic" {
			t.Fatalf("sanitized properties = %#v, want allowlisted metadata", props)
		}
		if props["method"] != http.MethodGet || props["path"] != "/api/v1/sessions/demo" || props["panic_kind"] != "error" {
			t.Fatalf("sanitized properties = %#v, want allowlisted fields", props)
		}
		if props["fingerprint"] != "abc123" || props["stack_fingerprint"] != "def456" {
			t.Fatalf("sanitized properties = %#v, want exported fingerprints", props)
		}
		if _, ok := props["panic"]; ok {
			t.Fatalf("panic property should be dropped: %#v", props)
		}
		if _, ok := props["stack"]; ok {
			t.Fatalf("stack property should be dropped: %#v", props)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PostHog sink did not send request")
	}
}

func TestPostHogSinkSanitizesAppActivePayload(t *testing.T) {
	requests := make(chan map[string]any, 1)
	sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", "", "", roundTripClient(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests <- body
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	}), nil)
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}

	sink.Emit(context.Background(), ports.TelemetryEvent{
		Name:       "ao.app.active",
		Source:     "cli",
		OccurredAt: time.Unix(1700000000, 0).UTC(),
		Level:      ports.TelemetryLevelInfo,
		Payload: map[string]any{
			"channel":      "cli",
			"command":      "spawn",
			"command_path": "ao spawn",
			"ip":           "203.0.113.10",
			"country":      "US",
			"city":         "San Francisco",
			"latitude":     37.7749,
			"longitude":    -122.4194,
		},
	})
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case req := <-requests:
		if got := req["event"]; got != "ao.v2.app.active" {
			t.Fatalf("event = %#v, want ao.v2.app.active", got)
		}
		props, ok := req["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties type = %T, want map[string]any", req["properties"])
		}
		if props["legacy_event_name"] != "ao.app.active" {
			t.Fatalf("legacy_event_name = %#v, want ao.app.active", props["legacy_event_name"])
		}
		if props["telemetry_schema_version"] != float64(2) {
			t.Fatalf("telemetry_schema_version = %#v, want 2", props["telemetry_schema_version"])
		}
		if props["channel"] != "cli" || props["command"] != "spawn" || props["command_path"] != "ao spawn" {
			t.Fatalf("sanitized properties = %#v, want active CLI metadata", props)
		}
		for _, key := range []string{"ip", "country", "city", "latitude", "longitude"} {
			if _, ok := props[key]; ok {
				t.Fatalf("%s property should be dropped: %#v", key, props)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PostHog sink did not send request")
	}
}

type roundTripClient func(*http.Request) (*http.Response, error)

func (f roundTripClient) Do(req *http.Request) (*http.Response, error) { return f(req) }

var _ postHogClient = roundTripClient(nil)

// Daemon events shipped with no version at all, so a session-spawn failure rate
// could not be attributed to a release. The supervisor supplies the version
// because the daemon binary has none that release tooling sets.
func TestPostHogSinkStampsAppVersionWhenSupplied(t *testing.T) {
	requests := make(chan map[string]any, 1)
	newSink := func(appVersion string) *PostHogSink {
		sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", appVersion, "", roundTripClient(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				return nil, err
			}
			requests <- body
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		}), nil)
		if err != nil {
			t.Fatalf("NewPostHogSink: %v", err)
		}
		return sink
	}

	emit := func(sink *PostHogSink) map[string]any {
		sink.Emit(context.Background(), ports.TelemetryEvent{
			Name:       "ao.session.spawn_failed",
			Source:     "session_service",
			OccurredAt: time.Unix(1700000000, 0).UTC(),
			Level:      ports.TelemetryLevelError,
		})
		select {
		case body := <-requests:
			props, ok := body["properties"].(map[string]any)
			if !ok {
				t.Fatalf("properties type = %T, want map[string]any", body["properties"])
			}
			return props
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for capture")
			return nil
		}
	}

	props := emit(newSink(" 0.11.2 "))
	if props["app_version"] != "0.11.2" || props["ao_version"] != "0.11.2" {
		t.Fatalf("version properties = %#v / %#v, want trimmed 0.11.2", props["app_version"], props["ao_version"])
	}

	// An unset supervisor env var must leave the properties off rather than
	// reporting a misleading placeholder that would pollute version breakdowns.
	props = emit(newSink(""))
	if _, ok := props["app_version"]; ok {
		t.Fatalf("app_version present without the option: %#v", props["app_version"])
	}
	if _, ok := props["ao_version"]; ok {
		t.Fatalf("ao_version present without the option: %#v", props["ao_version"])
	}
}

// An event name missing from remotePayloadAllowlist exports with no properties
// at all rather than failing loudly, so a key added at an emit site and not
// here ships silently stripped. These assertions are what a review-funnel
// dashboard actually reads: a renamed key is a broken chart, not a build
// failure, and nothing ties the emit sites to this map at compile time.
func TestReviewPayloadAllowlistCoversTheReviewFunnel(t *testing.T) {
	want := map[string][]string{
		"ao.review.triggered":      {"harness", "created_runs", "reused", "trigger"},
		"ao.review.trigger_failed": {"error_kind", "trigger"},
		"ao.review.submitted":      {"harness", "verdict", "duration_ms", "posted_to_provider", "trigger", "body_bytes", "auto_inject"},
		"ao.review.cancelled":      {"cancelled_runs"},
	}
	for name, keys := range want {
		allowed, ok := remotePayloadAllowlist[name]
		if !ok {
			t.Errorf("%s has no allowlist entry, so it would export with no properties", name)
			continue
		}
		for _, key := range keys {
			if _, ok := allowed[key]; !ok {
				t.Errorf("%s is missing allowlisted key %q", name, key)
			}
		}
		if len(allowed) != len(keys) {
			t.Errorf("%s allowlist has %d keys, want exactly %d (%v)", name, len(allowed), len(keys), keys)
		}
	}
}

// Review payloads carry counts, enums, and booleans. The review body is
// reviewer prose about someone's code; the PR URL and SHA identify the
// repository. None of them may survive into an exported property.
func TestReviewPayloadAllowlistRejectsIdentifyingKeys(t *testing.T) {
	forbidden := []string{"body", "pr_url", "url", "target_sha", "head_sha", "branch", "repo", "title", "review_body"}
	for name, allowed := range remotePayloadAllowlist {
		if !strings.HasPrefix(name, "ao.review.") {
			continue
		}
		for _, key := range forbidden {
			if _, ok := allowed[key]; ok {
				t.Errorf("%s allowlists identifying key %q", name, key)
			}
		}
	}
}

// The submitted event's own sanitizer pass has to drop an unknown key rather
// than trust the emit site.
func TestSanitizeRemotePayloadDropsUnlistedReviewKeys(t *testing.T) {
	got := sanitizeRemotePayload("ao.review.submitted", map[string]any{
		"verdict": "changes_requested",
		"trigger": "auto",
		"body":    "leaks credentials in src/config/prod.ts",
		"pr_url":  "https://github.com/acme/secret-repo/pull/7",
	})
	if got["verdict"] != "changes_requested" || got["trigger"] != "auto" {
		t.Fatalf("allowlisted keys were dropped: %#v", got)
	}
	if _, ok := got["body"]; ok {
		t.Fatalf("body survived sanitization: %#v", got)
	}
	if _, ok := got["pr_url"]; ok {
		t.Fatalf("pr_url survived sanitization: %#v", got)
	}
}
