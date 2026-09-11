package sentryobs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const fixtureEventID = "0123456789abcdef0123456789abcdef"

func TestParseAgentSwitchDSNNormalizesStandardDestinations(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		production   bool
		wantEndpoint string
	}{
		{
			name:         "production root",
			raw:          "https://public@SENTRY.EXAMPLE/42",
			production:   true,
			wantEndpoint: "https://sentry.example/api/42/envelope/",
		},
		{
			name:         "production base path and nondefault port",
			raw:          "https://public@sentry.example:8443/monitoring/sentry/42",
			production:   true,
			wantEndpoint: "https://sentry.example:8443/monitoring/sentry/api/42/envelope/",
		},
		{
			name:         "development IPv4 loopback",
			raw:          "http://public@127.0.0.1:9000/sentry/42",
			wantEndpoint: "http://127.0.0.1:9000/sentry/api/42/envelope/",
		},
		{
			name:         "development IPv6 loopback",
			raw:          "http://public@[::1]:9000/42",
			wantEndpoint: "http://[::1]:9000/api/42/envelope/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			destination, err := ParseAgentSwitchDSN(tc.raw, tc.production)
			if err != nil {
				t.Fatalf("ParseAgentSwitchDSN: %v", err)
			}
			if got := destination.Endpoint.String(); got != tc.wantEndpoint {
				t.Fatalf("endpoint = %q, want %q", got, tc.wantEndpoint)
			}
			if destination.PublicKey != "public" || destination.ProjectID != "42" {
				t.Fatalf("unexpected normalized destination: %+v", destination)
			}
			if len(destination.Fingerprint) != 64 {
				t.Fatalf("fingerprint length = %d, want 64", len(destination.Fingerprint))
			}
		})
	}
}

func TestParseAgentSwitchDSNFingerprintUsesEffectiveDestination(t *testing.T) {
	implicit, err := ParseAgentSwitchDSN("https://public@SENTRY.EXAMPLE/base/00042", true)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := ParseAgentSwitchDSN("https://public@sentry.example:443/base/42", true)
	if err != nil {
		t.Fatal(err)
	}
	if implicit.Fingerprint != explicit.Fingerprint {
		t.Fatalf("equivalent destinations differ: %q != %q", implicit.Fingerprint, explicit.Fingerprint)
	}

	variants := []string{
		"http://public@127.0.0.1/base/42",
		"https://public@sentry.example:444/base/42",
		"https://public@sentry.example/other/42",
		"https://public@sentry.example/base/43",
		"https://other@sentry.example/base/42",
	}
	for _, raw := range variants {
		production := strings.HasPrefix(raw, "https://")
		got, parseErr := ParseAgentSwitchDSN(raw, production)
		if parseErr != nil {
			t.Fatalf("ParseAgentSwitchDSN(%q): %v", raw, parseErr)
		}
		if got.Fingerprint == implicit.Fingerprint {
			t.Fatalf("destination component missing from fingerprint for %q", raw)
		}
	}
}

func TestParseAgentSwitchDSNRejectsUnsafeOrMalformedInputs(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		production bool
	}{
		{name: "secret", raw: "https://public:secret@sentry.example/42", production: true},
		{name: "empty secret delimiter", raw: "https://public:@sentry.example/42", production: true},
		{name: "query", raw: "https://public@sentry.example/42?x=1", production: true},
		{name: "empty query", raw: "https://public@sentry.example/42?", production: true},
		{name: "fragment", raw: "https://public@sentry.example/42#part", production: true},
		{name: "empty fragment", raw: "https://public@sentry.example/42#", production: true},
		{name: "malformed path escape", raw: "https://public@sentry.example/base%zz/42", production: true},
		{name: "escaped path separator", raw: "https://public@sentry.example/base%2fchild/42", production: true},
		{name: "nonnumeric project", raw: "https://public@sentry.example/project", production: true},
		{name: "missing project", raw: "https://public@sentry.example/", production: true},
		{name: "trailing slash", raw: "https://public@sentry.example/42/", production: true},
		{name: "missing public key", raw: "https://sentry.example/42", production: true},
		{name: "empty public key", raw: "https://@sentry.example/42", production: true},
		{name: "missing host", raw: "https://public@/42", production: true},
		{name: "unsupported scheme", raw: "ftp://public@sentry.example/42", production: true},
		{name: "production HTTP", raw: "http://public@127.0.0.1/42", production: true},
		{name: "development nonloopback HTTP", raw: "http://public@sentry.example/42"},
		{name: "path traversal", raw: "https://public@sentry.example/base/../42", production: true},
		{name: "duplicate separator", raw: "https://public@sentry.example/base//42", production: true},
		{name: "invalid port", raw: "https://public@sentry.example:70000/42", production: true},
		{name: "raw whitespace", raw: " https://public@sentry.example/42", production: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAgentSwitchDSN(tc.raw, tc.production); err == nil {
				t.Fatalf("ParseAgentSwitchDSN(%q) succeeded", tc.raw)
			}
		})
	}
}

func TestEncodeAgentSwitchEnvelopeV1MatchesFrozenFixture(t *testing.T) {
	canonical := loadAgentSwitchFixture(t)
	event := domain.AgentSwitchFailureEvent{
		EventID:                 fixtureEventID,
		EnvelopeEncodingVersion: AgentSwitchEnvelopeEncodingV1,
		CanonicalEventJSON:      canonical,
	}

	got, err := EncodeAgentSwitchEnvelopeV1(event)
	if err != nil {
		t.Fatalf("EncodeAgentSwitchEnvelopeV1: %v", err)
	}
	prefix := "{\"event_id\":\"0123456789abcdef0123456789abcdef\"}\n" +
		"{\"type\":\"event\",\"length\":1532}\n"
	want := append([]byte(prefix), canonical...)
	if !bytes.Equal(got, want) {
		t.Fatalf("envelope bytes differ\ngot:  %q\nwant: %q", got, want)
	}
	if len(got) != 1611 {
		t.Fatalf("envelope length = %d, want 1611", len(got))
	}
}

func TestEncodeAgentSwitchEnvelopeRejectsIdentityAndSizeViolations(t *testing.T) {
	canonical := loadAgentSwitchFixture(t)
	tests := []struct {
		name  string
		event domain.AgentSwitchFailureEvent
		err   error
	}{
		{
			name:  "invalid EventID",
			event: domain.AgentSwitchFailureEvent{EventID: strings.ToUpper(fixtureEventID), CanonicalEventJSON: canonical},
			err:   ErrInvalidEventID,
		},
		{
			name:  "event JSON mismatch",
			event: domain.AgentSwitchFailureEvent{EventID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CanonicalEventJSON: canonical},
			err:   ErrEventIDMismatch,
		},
		{
			name:  "duplicate event ID",
			event: domain.AgentSwitchFailureEvent{EventID: fixtureEventID, CanonicalEventJSON: []byte(`{"event_id":"0123456789abcdef0123456789abcdef","event_id":"0123456789abcdef0123456789abcdef"}`)},
			err:   ErrEventIDMismatch,
		},
		{
			name:  "malformed event JSON",
			event: domain.AgentSwitchFailureEvent{EventID: fixtureEventID, CanonicalEventJSON: []byte(`{"event_id":`)},
			err:   ErrEventIDMismatch,
		},
		{
			name:  "envelope too large",
			event: domain.AgentSwitchFailureEvent{EventID: fixtureEventID, CanonicalEventJSON: oversizedCanonicalEvent(fixtureEventID)},
			err:   ErrEnvelopeTooLarge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeAgentSwitchEnvelopeV1(tc.event)
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestAgentSwitchSenderRejectsUnknownEncodingWithoutNetwork(t *testing.T) {
	transport := &recordingRoundTripper{status: http.StatusAccepted}
	sender := newTestSender(t, transport)
	event := fixtureAgentSwitchEvent(t)
	event.EnvelopeEncodingVersion = 99

	result := sender.ObserveAgentSwitchFailure(context.Background(), event)
	if result.Outcome != ports.DeliveryPermanentFailure || result.Class != ports.DeliveryErrorUnsupportedEncoding {
		t.Fatalf("result = %+v", result)
	}
	if transport.calls() != 0 {
		t.Fatalf("network calls = %d, want 0", transport.calls())
	}
}

func TestAgentSwitchSenderAcknowledgesOnlyProvider2xx(t *testing.T) {
	tests := []struct {
		status  int
		outcome ports.DeliveryOutcome
		class   ports.DeliveryErrorClass
	}{
		{status: 200, outcome: ports.DeliveryAccepted, class: ports.DeliveryErrorNone},
		{status: 202, outcome: ports.DeliveryAccepted, class: ports.DeliveryErrorNone},
		{status: 204, outcome: ports.DeliveryAccepted, class: ports.DeliveryErrorNone},
		{status: 302, outcome: ports.DeliveryPermanentFailure, class: ports.DeliveryErrorInvalidPayload},
		{status: 400, outcome: ports.DeliveryPermanentFailure, class: ports.DeliveryErrorInvalidPayload},
		{status: 401, outcome: ports.DeliveryPermanentFailure, class: ports.DeliveryErrorUnauthorized},
		{status: 403, outcome: ports.DeliveryPermanentFailure, class: ports.DeliveryErrorUnauthorized},
		{status: 408, outcome: ports.DeliveryTransientFailure, class: ports.DeliveryErrorTimeout},
		{status: 429, outcome: ports.DeliveryTransientFailure, class: ports.DeliveryErrorRateLimited},
		{status: 500, outcome: ports.DeliveryTransientFailure, class: ports.DeliveryErrorProviderUnavailable},
		{status: 503, outcome: ports.DeliveryTransientFailure, class: ports.DeliveryErrorProviderUnavailable},
	}

	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			transport := &recordingRoundTripper{status: tc.status}
			sender := newTestSender(t, transport)
			result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
			if result.Outcome != tc.outcome || result.Class != tc.class {
				t.Fatalf("status %d result = %+v, want outcome=%s class=%s", tc.status, result, tc.outcome, tc.class)
			}
		})
	}
}

func TestAgentSwitchSenderUsesBoundedDedicatedRequest(t *testing.T) {
	transport := &recordingRoundTripper{status: http.StatusAccepted}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Jar: jar, Timeout: time.Minute}
	destination, err := ParseAgentSwitchDSN("https://public@sentry.example/base/42", true)
	if err != nil {
		t.Fatal(err)
	}
	sender := NewAgentSwitchFailureSender(destination, client)

	result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
	if result.Outcome != ports.DeliveryAccepted {
		t.Fatalf("result = %+v", result)
	}
	req := transport.lastRequest()
	if req.url != "https://sentry.example/base/api/42/envelope/" {
		t.Fatalf("request URL = %q", req.url)
	}
	if req.auth != "Sentry sentry_version=7, sentry_key=public, sentry_client=ao-agent-switch/1" {
		t.Fatalf("X-Sentry-Auth = %q", req.auth)
	}
	if req.contentType != "application/x-sentry-envelope" {
		t.Fatalf("Content-Type = %q", req.contentType)
	}
	if req.cookie != "" {
		t.Fatalf("Cookie header = %q, want empty", req.cookie)
	}
	if !req.hasDeadline {
		t.Fatal("request has no deadline")
	}
	if req.deadlineRemaining <= 4*time.Second || req.deadlineRemaining > agentSwitchRequestTimeout {
		t.Fatalf("request deadline remaining = %v, want hard five-second context", req.deadlineRemaining)
	}
	if len(req.body) > maxAgentSwitchEnvelopeBytes {
		t.Fatalf("request body = %d bytes, exceeds bound", len(req.body))
	}
}

func TestAgentSwitchSenderDisablesRedirectsAndCookies(t *testing.T) {
	var mu sync.Mutex
	redirected := 0
	cookies := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/redirected" {
			redirected++
			w.WriteHeader(http.StatusAccepted)
			return
		}
		cookies = append(cookies, r.Header.Get("Cookie"))
		if len(cookies) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "provider", Value: "secret"})
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	destination := parseServerDestination(t, server.URL)
	sender := NewAgentSwitchFailureSender(destination, client)

	first := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
	second := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
	if first.Outcome != ports.DeliveryPermanentFailure || second.Outcome != ports.DeliveryAccepted {
		t.Fatalf("results = %+v, %+v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if redirected != 0 {
		t.Fatalf("redirect target calls = %d, want 0", redirected)
	}
	if len(cookies) != 2 || cookies[0] != "" || cookies[1] != "" {
		t.Fatalf("provider request cookies = %#v, want two empty values", cookies)
	}
}

func TestAgentSwitchSenderRetriesByteIdenticalEnvelope(t *testing.T) {
	transport := &recordingRoundTripper{status: http.StatusServiceUnavailable}
	sender := newTestSender(t, transport)
	event := fixtureAgentSwitchEvent(t)

	first := sender.ObserveAgentSwitchFailure(context.Background(), event)
	transport.status = http.StatusAccepted
	second := sender.ObserveAgentSwitchFailure(context.Background(), event)
	if first.Outcome != ports.DeliveryTransientFailure || second.Outcome != ports.DeliveryAccepted {
		t.Fatalf("results = %+v, %+v", first, second)
	}
	requests := transport.requests()
	if len(requests) != 2 || !bytes.Equal(requests[0].body, requests[1].body) {
		t.Fatalf("retry bodies are not byte-identical")
	}
	wantHeader := `{"event_id":"` + fixtureEventID + `"}` + "\n"
	if !bytes.HasPrefix(requests[0].body, []byte(wantHeader)) {
		t.Fatalf("envelope header does not retain EventID: %q", requests[0].body)
	}
}

func TestAgentSwitchSenderClassifiesTimeoutNetworkAndResponseLoss(t *testing.T) {
	tests := []struct {
		name      string
		transport http.RoundTripper
		class     ports.DeliveryErrorClass
	}{
		{name: "timeout", transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}), class: ports.DeliveryErrorTimeout},
		{name: "network", transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection failed with private provider detail")
		}), class: ports.DeliveryErrorNetwork},
		{name: "response lost", transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil {
				trace.WroteRequest(httptrace.WroteRequestInfo{})
			}
			return nil, errors.New("response lost after provider may have accepted")
		}), class: ports.DeliveryResponseLost},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := newTestSender(t, tc.transport)
			result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
			if result.Outcome != ports.DeliveryTransientFailure || result.Class != tc.class {
				t.Fatalf("result = %+v, want transient %s", result, tc.class)
			}
		})
	}
}

func TestAgentSwitchSenderBoundsResponseHeadersAndBody(t *testing.T) {
	t.Run("headers", func(t *testing.T) {
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil {
				trace.WroteRequest(httptrace.WroteRequestInfo{})
			}
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     http.Header{"X-Oversized": []string{strings.Repeat("x", maxAgentSwitchResponseBytes)}},
				Body:       io.NopCloser(strings.NewReader("ignored")),
				Request:    req,
			}, nil
		})
		sender := newTestSender(t, transport)
		result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
		if result.Outcome != ports.DeliveryTransientFailure {
			t.Fatalf("oversized header result = %+v", result)
		}
	})

	t.Run("body", func(t *testing.T) {
		body := &countingInfiniteBody{}
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     make(http.Header),
				Body:       body,
				Request:    req,
			}, nil
		})
		sender := newTestSender(t, transport)
		result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
		if result.Outcome != ports.DeliveryAccepted {
			t.Fatalf("result = %+v", result)
		}
		if body.readBytes != maxAgentSwitchResponseBytes {
			t.Fatalf("response body bytes read = %d, want %d", body.readBytes, maxAgentSwitchResponseBytes)
		}
		if !body.closed {
			t.Fatal("response body was not closed")
		}
	})
}

func TestAgentSwitchSenderParsesProviderThrottleHeaders(t *testing.T) {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		status    int
		headers   http.Header
		outcome   ports.DeliveryOutcome
		wantAfter time.Duration
		wantScope ports.DeliveryThrottleScope
	}{
		{
			name:      "accepted error category throttle",
			status:    http.StatusAccepted,
			headers:   http.Header{"X-Sentry-Rate-Limits": []string{"120:error:organization:quota_exceeded"}},
			outcome:   ports.DeliveryAccepted,
			wantAfter: 2 * time.Minute,
			wantScope: ports.DeliveryThrottleErrorCategory,
		},
		{
			name:      "accepted all-category throttle",
			status:    http.StatusNoContent,
			headers:   http.Header{"X-Sentry-Rate-Limits": []string{"90::organization"}},
			outcome:   ports.DeliveryAccepted,
			wantAfter: 90 * time.Second,
			wantScope: ports.DeliveryThrottleAll,
		},
		{
			name:      "later relevant quota wins",
			status:    http.StatusTooManyRequests,
			headers:   http.Header{"X-Sentry-Rate-Limits": []string{"30::organization, 120:error:organization, 999:transaction:organization"}},
			outcome:   ports.DeliveryTransientFailure,
			wantAfter: 2 * time.Minute,
			wantScope: ports.DeliveryThrottleErrorCategory,
		},
		{
			name:      "retry after seconds",
			status:    http.StatusTooManyRequests,
			headers:   http.Header{"Retry-After": []string{"75"}},
			outcome:   ports.DeliveryTransientFailure,
			wantAfter: 75 * time.Second,
			wantScope: ports.DeliveryThrottleAll,
		},
		{
			name:      "retry after HTTP date",
			status:    http.StatusServiceUnavailable,
			headers:   http.Header{"Retry-After": []string{now.Add(3 * time.Minute).Format(http.TimeFormat)}},
			outcome:   ports.DeliveryTransientFailure,
			wantAfter: 3 * time.Minute,
			wantScope: ports.DeliveryThrottleAll,
		},
		{
			name:      "unknown category ignored",
			status:    http.StatusAccepted,
			headers:   http.Header{"X-Sentry-Rate-Limits": []string{"999:transaction:organization"}},
			outcome:   ports.DeliveryAccepted,
			wantScope: ports.DeliveryThrottleNone,
		},
		{
			name:      "429 without header remains all scope",
			status:    http.StatusTooManyRequests,
			headers:   make(http.Header),
			outcome:   ports.DeliveryTransientFailure,
			wantScope: ports.DeliveryThrottleAll,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &recordingRoundTripper{status: tc.status, responseHeaders: tc.headers}
			observer := newTestSender(t, transport)
			sender := observer.(*agentSwitchFailureSender)
			sender.now = func() time.Time { return now }
			result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
			if result.Outcome != tc.outcome || result.ThrottleScope != tc.wantScope {
				t.Fatalf("result = %+v, want outcome=%s scope=%s", result, tc.outcome, tc.wantScope)
			}
			wantTime := time.Time{}
			if tc.wantAfter > 0 {
				wantTime = now.Add(tc.wantAfter)
			}
			if !result.RetryNotBefore.Equal(wantTime) {
				t.Fatalf("RetryNotBefore = %s, want %s", result.RetryNotBefore, wantTime)
			}
		})
	}
}

func TestAgentSwitchSenderClassifiesHeaderless429AsAllScopeThrottle(t *testing.T) {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	transport := &recordingRoundTripper{status: http.StatusTooManyRequests, responseHeaders: make(http.Header)}
	observer := newTestSender(t, transport)
	sender := observer.(*agentSwitchFailureSender)
	sender.now = func() time.Time { return now }

	result := sender.ObserveAgentSwitchFailure(context.Background(), fixtureAgentSwitchEvent(t))
	if result.Outcome != ports.DeliveryTransientFailure || result.Class != ports.DeliveryErrorRateLimited || result.ThrottleScope != ports.DeliveryThrottleAll {
		t.Fatalf("headerless 429 result = %+v", result)
	}
	if !result.RetryNotBefore.IsZero() {
		t.Fatalf("sender invented provider retry deadline %s", result.RetryNotBefore)
	}
}

func loadAgentSwitchFixture(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "test", "fixtures", "agent-switch-observability", "envelope-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(raw)
}

func fixtureAgentSwitchEvent(t *testing.T) domain.AgentSwitchFailureEvent {
	t.Helper()
	return domain.AgentSwitchFailureEvent{
		EventID:                 fixtureEventID,
		EnvelopeEncodingVersion: AgentSwitchEnvelopeEncodingV1,
		CanonicalEventJSON:      loadAgentSwitchFixture(t),
	}
}

func oversizedCanonicalEvent(eventID string) []byte {
	prefix := []byte(`{"event_id":"` + eventID + `","padding":"`)
	suffix := []byte(`"}`)
	padding := bytes.Repeat([]byte{'x'}, maxAgentSwitchEnvelopeBytes-len(prefix)-len(suffix))
	return append(append(prefix, padding...), suffix...)
}

func parseServerDestination(t *testing.T, serverURL string) AgentSwitchDestination {
	t.Helper()
	raw := strings.Replace(serverURL, "http://", "http://public@", 1) + "/42"
	destination, err := ParseAgentSwitchDSN(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	return destination
}

func newTestSender(t *testing.T, transport http.RoundTripper) ports.AgentSwitchFailureObserver {
	t.Helper()
	destination, err := ParseAgentSwitchDSN("https://public@sentry.example/42", true)
	if err != nil {
		t.Fatal(err)
	}
	return NewAgentSwitchFailureSender(destination, &http.Client{Transport: transport})
}

type recordedRequest struct {
	url               string
	auth              string
	contentType       string
	cookie            string
	body              []byte
	hasDeadline       bool
	deadlineRemaining time.Duration
}

type recordingRoundTripper struct {
	mu              sync.Mutex
	status          int
	responseHeaders http.Header
	recorded        []recordedRequest
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	deadline, hasDeadline := req.Context().Deadline()
	record := recordedRequest{
		url:               req.URL.String(),
		auth:              req.Header.Get("X-Sentry-Auth"),
		contentType:       req.Header.Get("Content-Type"),
		cookie:            req.Header.Get("Cookie"),
		body:              body,
		hasDeadline:       hasDeadline,
		deadlineRemaining: time.Until(deadline),
	}
	r.mu.Lock()
	r.recorded = append(r.recorded, record)
	status := r.status
	headers := r.responseHeaders.Clone()
	r.mu.Unlock()
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader("provider body must be discarded")),
		Request:    req,
	}, nil
}

func (r *recordingRoundTripper) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.recorded)
}

func (r *recordingRoundTripper) lastRequest() recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recorded[len(r.recorded)-1]
}

func (r *recordingRoundTripper) requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.recorded...)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type countingInfiniteBody struct {
	readBytes int
	closed    bool
}

func (b *countingInfiniteBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.readBytes += len(p)
	return len(p), nil
}

func (b *countingInfiniteBody) Close() error {
	b.closed = true
	return nil
}
