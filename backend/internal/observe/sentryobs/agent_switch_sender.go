package sentryobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	maxAgentSwitchEnvelopeBytes = 64 << 10
	maxAgentSwitchWrapperBytes  = 4 << 10
	maxAgentSwitchResponseBytes = 64 << 10
	agentSwitchRequestTimeout   = 5 * time.Second
	maxAgentSwitchThrottleDelay = 7 * 24 * time.Hour
	agentSwitchSentryAuth       = "Sentry sentry_version=7, sentry_key=%s, sentry_client=ao-agent-switch/1"
)

// Agent-switch envelope validation errors describe rejected local payloads.
var (
	ErrInvalidEventID              = errors.New("invalid agent switch EventID")
	ErrEventIDMismatch             = errors.New("agent switch EventID mismatch")
	ErrEnvelopeTooLarge            = errors.New("agent switch envelope exceeds 64 KiB")
	ErrEnvelopeWrapperTooLarge     = errors.New("agent switch envelope wrapper exceeds 4 KiB")
	ErrUnsupportedEnvelopeEncoding = errors.New("unsupported agent switch envelope encoding")
	errResponseHeadersTooLarge     = errors.New("sentry response headers exceed processing bound")
	agentSwitchEventIDPattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type agentSwitchFailureSender struct {
	destination AgentSwitchDestination
	client      *http.Client
	now         func() time.Time
}

// NewAgentSwitchFailureSender returns a synchronous, status-aware observer. It
// clones the supplied client and enforces its own redirect, cookie, and bounds.
func NewAgentSwitchFailureSender(destination AgentSwitchDestination, client *http.Client) ports.AgentSwitchFailureObserver {
	clonedDestination := destination
	if destination.Endpoint != nil {
		endpoint := *destination.Endpoint
		clonedDestination.Endpoint = &endpoint
	}
	return &agentSwitchFailureSender{destination: clonedDestination, client: boundedAgentSwitchHTTPClient(client), now: time.Now}
}

func (sender *agentSwitchFailureSender) ObserveAgentSwitchFailure(ctx context.Context, event domain.AgentSwitchFailureEvent) ports.DeliveryResult {
	if event.EnvelopeEncodingVersion != AgentSwitchEnvelopeEncodingV1 {
		return permanentAgentSwitchDelivery(ports.DeliveryErrorUnsupportedEncoding)
	}
	if err := sender.destination.validate(); err != nil {
		return permanentAgentSwitchDelivery(ports.DeliveryErrorLocalInvariant)
	}
	envelope, err := encodeAgentSwitchEnvelope(event)
	if err != nil {
		if errors.Is(err, ErrUnsupportedEnvelopeEncoding) {
			return permanentAgentSwitchDelivery(ports.DeliveryErrorUnsupportedEncoding)
		}
		return permanentAgentSwitchDelivery(ports.DeliveryErrorLocalInvariant)
	}
	requestContext, cancel := context.WithTimeout(ctx, agentSwitchRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, sender.destination.Endpoint.String(), bytes.NewReader(envelope))
	if err != nil {
		return permanentAgentSwitchDelivery(ports.DeliveryErrorLocalInvariant)
	}
	request.Header.Set("Content-Type", "application/x-sentry-envelope")
	request.Header.Set("X-Sentry-Auth", fmt.Sprintf(agentSwitchSentryAuth, sender.destination.PublicKey))
	var requestWritten atomic.Bool
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			requestWritten.Store(true)
		}
	}}))
	response, err := sender.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return transientAgentSwitchDelivery(ports.DeliveryErrorTimeout)
		}
		if requestWritten.Load() {
			return transientAgentSwitchDelivery(ports.DeliveryResponseLost)
		}
		return transientAgentSwitchDelivery(ports.DeliveryErrorNetwork)
	}
	if response.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAgentSwitchResponseBytes))
		_ = response.Body.Close()
	}
	result := classifyAgentSwitchHTTPStatus(response.StatusCode)
	result.RetryNotBefore, result.ThrottleScope = parseAgentSwitchThrottle(response.Header, sender.now().UTC(), response.StatusCode)
	return result
}

func encodeAgentSwitchEnvelope(event domain.AgentSwitchFailureEvent) ([]byte, error) {
	switch event.EnvelopeEncodingVersion {
	case AgentSwitchEnvelopeEncodingV1:
		return EncodeAgentSwitchEnvelopeV1(event)
	default:
		return nil, ErrUnsupportedEnvelopeEncoding
	}
}

// EncodeAgentSwitchEnvelopeV1 wraps the immutable canonical event with the
// frozen v1 Sentry envelope header and item header.
func EncodeAgentSwitchEnvelopeV1(event domain.AgentSwitchFailureEvent) ([]byte, error) {
	if !agentSwitchEventIDPattern.MatchString(event.EventID) {
		return nil, ErrInvalidEventID
	}
	canonicalID, err := canonicalAgentSwitchEventID(event.CanonicalEventJSON)
	if err != nil || canonicalID != event.EventID {
		return nil, ErrEventIDMismatch
	}
	header := []byte(`{"event_id":"` + event.EventID + `"}` + "\n")
	item := []byte(`{"type":"event","length":` + strconv.Itoa(len(event.CanonicalEventJSON)) + `}` + "\n")
	if len(header)+len(item) > maxAgentSwitchWrapperBytes {
		return nil, ErrEnvelopeWrapperTooLarge
	}
	if len(header)+len(item)+len(event.CanonicalEventJSON) > maxAgentSwitchEnvelopeBytes {
		return nil, ErrEnvelopeTooLarge
	}
	out := make([]byte, 0, len(header)+len(item)+len(event.CanonicalEventJSON))
	out = append(out, header...)
	out = append(out, item...)
	out = append(out, event.CanonicalEventJSON...)
	return out, nil
}

func canonicalAgentSwitchEventID(canonical []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", ErrEventIDMismatch
	}
	count, eventID := 0, ""
	for decoder.More() {
		key, keyErr := decoder.Token()
		if keyErr != nil {
			return "", ErrEventIDMismatch
		}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return "", ErrEventIDMismatch
		}
		if key == "event_id" {
			count++
			if decodeErr := json.Unmarshal(value, &eventID); decodeErr != nil {
				return "", ErrEventIDMismatch
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || count != 1 {
		return "", ErrEventIDMismatch
	}
	if token, trailingErr := decoder.Token(); trailingErr != io.EOF || token != nil {
		return "", ErrEventIDMismatch
	}
	return eventID, nil
}

func boundedAgentSwitchHTTPClient(source *http.Client) *http.Client {
	base := http.DefaultClient
	if source != nil {
		base = source
	}
	client := *base
	client.Timeout = 0
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if standard, ok := transport.(*http.Transport); ok {
		cloned := standard.Clone()
		if cloned.MaxResponseHeaderBytes <= 0 || cloned.MaxResponseHeaderBytes > maxAgentSwitchResponseBytes {
			cloned.MaxResponseHeaderBytes = maxAgentSwitchResponseBytes
		}
		transport = cloned
	}
	client.Transport = boundedResponseHeaderTransport{base: transport}
	return &client
}

type boundedResponseHeaderTransport struct{ base http.RoundTripper }

func (transport boundedResponseHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}
	if agentSwitchHeaderBytes(response.Header) > maxAgentSwitchResponseBytes {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errResponseHeadersTooLarge
	}
	return response, nil
}

func agentSwitchHeaderBytes(header http.Header) int {
	total := 2
	for name, values := range header {
		for _, value := range values {
			total += len(name) + len(value) + 4
		}
	}
	return total
}

func classifyAgentSwitchHTTPStatus(status int) ports.DeliveryResult {
	switch {
	case status >= 200 && status < 300:
		return ports.DeliveryResult{Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone, ThrottleScope: ports.DeliveryThrottleNone}
	case status == http.StatusRequestTimeout:
		return transientAgentSwitchDelivery(ports.DeliveryErrorTimeout)
	case status == http.StatusTooManyRequests:
		return transientAgentSwitchDelivery(ports.DeliveryErrorRateLimited)
	case status >= 500 && status < 600:
		return transientAgentSwitchDelivery(ports.DeliveryErrorProviderUnavailable)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return permanentAgentSwitchDelivery(ports.DeliveryErrorUnauthorized)
	default:
		return permanentAgentSwitchDelivery(ports.DeliveryErrorInvalidPayload)
	}
}

func transientAgentSwitchDelivery(class ports.DeliveryErrorClass) ports.DeliveryResult {
	return ports.DeliveryResult{Outcome: ports.DeliveryTransientFailure, Class: class, ThrottleScope: ports.DeliveryThrottleNone}
}

func permanentAgentSwitchDelivery(class ports.DeliveryErrorClass) ports.DeliveryResult {
	return ports.DeliveryResult{Outcome: ports.DeliveryPermanentFailure, Class: class, ThrottleScope: ports.DeliveryThrottleNone}
}

func parseAgentSwitchThrottle(header http.Header, now time.Time, status int) (time.Time, ports.DeliveryThrottleScope) {
	deadline := time.Time{}
	scope := ports.DeliveryThrottleNone
	if retryAt, ok := parseAgentSwitchRetryAfter(header.Get("Retry-After"), now); ok {
		deadline, scope = retryAt, ports.DeliveryThrottleAll
	}
	quotas := strings.Join(header.Values("X-Sentry-Rate-Limits"), ",")
	for _, quota := range strings.Split(quotas, ",") {
		parts := strings.Split(strings.TrimSpace(quota), ":")
		if len(parts) < 2 {
			continue
		}
		seconds, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
			continue
		}
		quotaScope, relevant := agentSwitchQuotaScope(strings.TrimSpace(parts[1]))
		if !relevant {
			continue
		}
		delay := time.Duration(seconds * float64(time.Second))
		if delay > maxAgentSwitchThrottleDelay {
			delay = maxAgentSwitchThrottleDelay
		}
		candidate := now.Add(delay)
		if candidate.After(deadline) || (candidate.Equal(deadline) && quotaScope == ports.DeliveryThrottleAll) {
			deadline, scope = candidate, quotaScope
		}
	}
	if status == http.StatusTooManyRequests && scope == ports.DeliveryThrottleNone {
		scope = ports.DeliveryThrottleAll
	}
	return deadline, scope
}

func parseAgentSwitchRetryAfter(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseUint(raw, 10, 32); err == nil {
		delay := time.Duration(seconds) * time.Second
		if delay > maxAgentSwitchThrottleDelay {
			delay = maxAgentSwitchThrottleDelay
		}
		return now.Add(delay), true
	}
	parsed, err := http.ParseTime(raw)
	if err != nil || !parsed.After(now) {
		return time.Time{}, false
	}
	if parsed.After(now.Add(maxAgentSwitchThrottleDelay)) {
		parsed = now.Add(maxAgentSwitchThrottleDelay)
	}
	return parsed, true
}

func agentSwitchQuotaScope(categories string) (ports.DeliveryThrottleScope, bool) {
	if categories == "" {
		return ports.DeliveryThrottleAll, true
	}
	for _, category := range strings.Split(categories, ";") {
		switch strings.TrimSpace(category) {
		case "error", "default":
			return ports.DeliveryThrottleErrorCategory, true
		}
	}
	return ports.DeliveryThrottleNone, false
}
