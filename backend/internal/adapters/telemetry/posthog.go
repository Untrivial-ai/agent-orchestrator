package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const postHogBufferSize = 128
const maxCommandShapeLength = 48
const remoteTelemetrySchemaVersion = 2

var remoteEventNameAliases = map[string]string{
	"ao.app.active":              "ao.v2.app.active",
	"ao.cli.invoked":             "ao.v2.cli.invoked",
	"ao.renderer.route_viewed":   "ao.v2.renderer.route_viewed",
	"ao.renderer.loaded":         "ao.v2.renderer.loaded",
	"ao.renderer.api_error":      "ao.v2.renderer.api_error",
	"ao.renderer.daemon_failure": "ao.v2.renderer.daemon_failure",
}

var remoteCommandTokens = map[string]struct{}{
	"add":              {},
	"agent":            {},
	"agent-process":    {},
	"cancel":           {},
	"claim-pr":         {},
	"cleanup":          {},
	"clear":            {},
	"completion":       {},
	"daemon":           {},
	"delete":           {},
	"dev":              {},
	"doctor":           {},
	"execute":          {},
	"get":              {},
	"help":             {},
	"hooks":            {},
	"import":           {},
	"import-projects":  {},
	"kill":             {},
	"launch":           {},
	"list":             {},
	"ls":               {},
	"merge":            {},
	"orchestrator":     {},
	"preview":          {},
	"pr":               {},
	"project":          {},
	"remove":           {},
	"rename":           {},
	"resolve-comments": {},
	"restart":          {},
	"restore":          {},
	"review":           {},
	"rm":               {},
	"send":             {},
	"session":          {},
	"set-config":       {},
	"spawn":            {},
	"start":            {},
	"status":           {},
	"stop":             {},
	"submit":           {},
	"supervise":        {},
	"trigger":          {},
	"version":          {},
}

var remotePayloadAllowlist = map[string]map[string]struct{}{
	// Reported by the LAN listener on the first authenticated request per
	// transport per day (httpd/mobile_connect_telemetry.go). "transport" is a
	// closed vocabulary — lan / tailscale / loopback / other — never an address.
	"ao.mobile.device_connected": {
		"transport": {},
	},
	"ao.app.active": {
		"actor_type":   {},
		"channel":      {},
		"command":      {},
		"command_path": {},
	},
	"ao.cli.invoked": {
		"actor_type":   {},
		"command":      {},
		"command_path": {},
	},
	"ao.cli.usage_errors": {
		"component":    {},
		"command":      {},
		"command_path": {},
		"error_kind":   {},
		"fingerprint":  {},
		"operation":    {},
		// Rolled up by AggregatingSink (adapters/telemetry/aggregate.go) into
		// one event per minute; without these, a rollup's whole reason for
		// existing - the true occurrence count of a burst - is silently
		// stripped before it ever reaches PostHog.
		"count":        {},
		"window_start": {},
		"window_end":   {},
	},
	"ao.daemon.panic": {
		"component":         {},
		"fingerprint":       {},
		"method":            {},
		"operation":         {},
		"path":              {},
		"panic_kind":        {},
		"stack_fingerprint": {},
		"count":             {},
		"window_start":      {},
		"window_end":        {},
	},
	"ao.daemon.started": {
		"agent": {},
		"port":  {},
	},
	"ao.http.5xx": {
		"component":     {},
		"duration":      {},
		"error_code":    {},
		"error_kind":    {},
		"fingerprint":   {},
		"method":        {},
		"operation":     {},
		"path":          {},
		"status":        {},
		"status_family": {},
		"count":         {},
		"window_start":  {},
		"window_end":    {},
	},
	"ao.onboarding.first_project_added": {
		"has_git_remote": {},
		"kind":           {},
		"github_org":     {},
	},
	"ao.onboarding.first_session_spawned": {
		"harness":                {},
		"kind":                   {},
		"since_first_project_ms": {},
	},
	"ao.review.triggered": {
		"created_runs": {},
		"harness":      {},
		"reused":       {},
		"trigger":      {},
	},
	"ao.review.trigger_failed": {
		"error_kind": {},
		"trigger":    {},
	},
	"ao.review.submitted": {
		"auto_inject":        {},
		"body_bytes":         {},
		"duration_ms":        {},
		"harness":            {},
		"posted_to_provider": {},
		"trigger":            {},
		"verdict":            {},
	},
	"ao.review.cancelled": {
		"cancelled_runs": {},
	},
	"ao.projects.created": {
		"has_git_remote": {},
		"kind":           {},
		"github_org":     {},
	},
	"ao.session.spawn_failed": {
		"component":   {},
		"duration_ms": {},
		"error_code":  {},
		"error_kind":  {},
		"fingerprint": {},
		"harness":     {},
		"kind":        {},
		"operation":   {},
	},
	"ao.session.spawned": {
		"duration_ms": {},
		"harness":     {},
		"kind":        {},
	},
	"ao.session.waiting_input_entered": {
		"state": {},
	},
	"ao.session.waiting_input_exited": {
		"dwell_ms":  {},
		"exited_to": {},
		"state":     {},
	},
}

type postHogClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// PostHogSink exports allowlisted telemetry events to PostHog.
type PostHogSink struct {
	apiKey       string
	host         string
	distinctID   string
	defaultAgent string
	tenure       *tenureTracker
	// appVersion stamps app_version/ao_version on every exported event. Empty
	// leaves the properties off entirely rather than reporting a misleading
	// "unknown" that would show up as a real version in release breakdowns.
	appVersion string
	client     postHogClient
	log        *slog.Logger
	ch         chan ports.TelemetryEvent
	wg         sync.WaitGroup
	closeOnce  sync.Once
	// ctx bounds every in-flight send and its retry backoff. Close cancels it
	// when the caller's shutdown context is done, so pending retry work stops
	// promptly instead of outliving shutdown on an uncancellable sleep or a
	// long HTTP timeout.
	ctx    context.Context
	cancel context.CancelFunc
}

// NewPostHogSink starts a buffered PostHog exporter with a stable install ID.
func NewPostHogSink(dataDir, apiKey, host, appVersion, defaultAgent string, client postHogClient, log *slog.Logger) (*PostHogSink, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("posthog api key is required")
	}
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("posthog host is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	distinctID, err := loadOrCreateInstallID(dataDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &PostHogSink{
		apiKey:       apiKey,
		host:         strings.TrimRight(host, "/"),
		distinctID:   distinctID,
		defaultAgent: safeAgentSlug(defaultAgent),
		tenure:       newTenureTracker(dataDir, time.Now),
		client:       client,
		log:          telemetryLogger(log),
		appVersion:   strings.TrimSpace(appVersion),
		ch:           make(chan ports.TelemetryEvent, postHogBufferSize),
		ctx:          ctx,
		cancel:       cancel,
	}
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

// Emit enqueues an event for best-effort export.
func (s *PostHogSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	select {
	case s.ch <- ev:
	default:
		s.log.Warn("telemetry posthog sink buffer full; dropping event", "name", ev.Name, "source", ev.Source)
	}
}

// Close drains the exporter until completion or context cancellation.
func (s *PostHogSink) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { close(s.ch) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.wg.Wait()
	}()
	select {
	case <-ctx.Done():
		// The caller's shutdown deadline elapsed: cancel in-flight sends and
		// their retry backoff so the drain goroutine exits promptly rather than
		// blocking on a sleep or a long HTTP timeout.
		s.cancel()
		<-done
		return ctx.Err()
	case <-done:
		s.cancel()
		return nil
	}
}

func (s *PostHogSink) loop() {
	defer s.wg.Done()
	for ev := range s.ch {
		s.send(s.ctx, ev)
	}
}

// Bounded retry for a single event. A dropped send is a permanent gap,
// because the upstream reservoir has already marked this event's dedup slot
// (ao.app.active per UTC day, ao.cli.invoked per command/day) spent before it
// ever reached this sink, so there is no second attempt from the caller. Only
// failed sends are retried, so a healthy path still makes exactly one request
// per event and adds no billable volume. A sustained outage gives up after
// postHogSendMaxAttempts rather than blocking the drain goroutine forever.
const (
	postHogSendMaxAttempts = 2
	postHogSendRetryDelay  = 200 * time.Millisecond
)

func (s *PostHogSink) send(ctx context.Context, ev ports.TelemetryEvent) {
	eventName := remoteEventName(ev.Name)
	// A stable per-event UUID, reused across every attempt, is PostHog's
	// idempotency key. A transport error only means AO received no response;
	// PostHog may already have ingested the first attempt, so without a shared
	// UUID a retry would land as a duplicate (inflating counts and the billed
	// volume, breaking the "retries add no volume" guarantee). PostHog dedupes
	// events that carry the same uuid.
	eventUUID := uuid.NewString()
	body := map[string]any{
		"api_key":     s.apiKey,
		"event":       eventName,
		"distinct_id": s.distinctID,
		"uuid":        eventUUID,
		"properties":  s.properties(ev),
		"timestamp":   ev.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		s.log.Warn("telemetry posthog payload marshal failed", "name", ev.Name, "error", err)
		return
	}

	for attempt := 1; attempt <= postHogSendMaxAttempts; attempt++ {
		retryable, err := s.sendOnce(ctx, payload)
		if err == nil {
			return
		}
		if !retryable || attempt == postHogSendMaxAttempts {
			s.log.Warn("telemetry posthog export failed", "name", ev.Name, "attempts", attempt, "error", err)
			return
		}
		// Cancellable backoff: shutdown must be able to stop pending retry work
		// instead of waiting out the full delay.
		timer := time.NewTimer(postHogSendRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// sendOnce posts a single capture attempt. retryable is true only for
// transient failures a retry might clear (connection errors, HTTP 429, and
// 5xx); a permanent rejection (any other non-2xx, e.g. a 4xx auth/shape error)
// returns retryable=false so a retry does not waste a call that will fail
// identically.
func (s *PostHogSink) sendOnce(ctx context.Context, payload []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.host+"/capture/", bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return true, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	rejected := fmt.Errorf("rejected status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, rejected
}

func (s *PostHogSink) properties(ev ports.TelemetryEvent) map[string]any {
	props := map[string]any{
		"source":                   ev.Source,
		"level":                    string(ev.Level),
		"telemetry_schema_version": remoteTelemetrySchemaVersion,
		// Classifies this install as the CLI/daemon on every event, matching the
		// desktop renderer (client="desktop") and mobile (client="mobile"), so a
		// shared event like ao.app.active splits by one property across surfaces.
		"client": "cli",
		// The distinct ID is a random install ID with no person data behind it,
		// so skip PostHog person-profile processing: identified events bill at
		// several times the anonymous rate and the profiles would hold nothing.
		"$process_person_profile": false,
	}
	if remoteEventName(ev.Name) != ev.Name {
		props["legacy_event_name"] = ev.Name
	}
	// Without this, every daemon event lands with no version at all, so a
	// failure rate cannot be attributed to a release. Renderer events already
	// carry app_version; these are the matching daemon-side values.
	if s.appVersion != "" {
		props["app_version"] = s.appVersion
		props["ao_version"] = s.appVersion
		// Channel of the build actually running, from the version string. A
		// nightly build carries "-nightly." (CI stamps 0.11.3-nightly.5); plain
		// semver is stable. The renderer sends the same property.
		props["version_channel"] = versionChannel(s.appVersion)
	}
	// Which agent this install actually defaults to. Without it, "how many people
	// use Claude versus Codex" is only answerable for sessions that were spawned,
	// which silently excludes everyone who installed and never ran one.
	if s.defaultAgent != "" {
		props["default_agent"] = s.defaultAgent
	}
	// Tenure turns retention into a property rather than a cohort query, so the
	// spread of first-day versus long-standing installs is one breakdown.
	if s.tenure != nil {
		ageDays, activeDays, bucket := s.tenure.observe()
		props["install_age_days"] = ageDays
		props["active_days"] = activeDays
		props["tenure"] = string(bucket)
	}
	if ev.RequestID != "" {
		props["request_id"] = ev.RequestID
	}
	if ev.ProjectID != nil {
		props["project_id_hash"] = sha256String(string(*ev.ProjectID))
	}
	if ev.SessionID != nil {
		props["session_id_hash"] = sha256String(string(*ev.SessionID))
	}
	for k, v := range sanitizeRemotePayload(ev.Name, ev.Payload) {
		props[k] = v
	}
	return props
}

func remoteEventName(name string) string {
	if alias, ok := remoteEventNameAliases[name]; ok {
		return alias
	}
	return name
}

func sanitizeRemotePayload(name string, payload map[string]any) map[string]any {
	allowed := remotePayloadAllowlist[name]
	if len(allowed) == 0 || len(payload) == 0 {
		return nil
	}
	sanitized := make(map[string]any, len(allowed))
	for key := range allowed {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if safe, ok := sanitizeRemoteValue(key, value); ok {
			sanitized[key] = safe
		}
	}
	return sanitized
}

func isAllowedCommandValue(key, value string) bool {
	if value == "" || len(value) > maxCommandShapeLength {
		return false
	}
	tokens := strings.Split(value, " ")
	if key == "command" {
		if len(tokens) != 1 {
			return false
		}
		if value == "ao" || value == "<unknown>" {
			return true
		}
		_, ok := remoteCommandTokens[value]
		return ok
	}
	if key != "command_path" || tokens[0] != "ao" {
		return false
	}
	for i, token := range tokens[1:] {
		if token == "<unknown>" {
			if i != len(tokens[1:])-1 {
				return false
			}
			continue
		}
		if _, ok := remoteCommandTokens[token]; !ok {
			return false
		}
	}
	return true
}

func sanitizeRemoteValue(key string, v any) (any, bool) {
	switch value := v.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, false
		}
		if (key == "command" || key == "command_path") && !isAllowedCommandValue(key, value) {
			return "sha256:" + sha256String(value)[:16], true
		}
		return value, true
	case bool:
		return value, true
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		return uint64(value), true
	case uint8:
		return uint64(value), true
	case uint16:
		return uint64(value), true
	case uint32:
		return uint64(value), true
	case uint64:
		return value, true
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, false
		}
		return float64(value), true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false
		}
		return value, true
	default:
		return nil, false
	}
}

// agentSlugPattern is the shape of a real adapter id (claude-code, codex, ...).
// AO_AGENT is free text and cfg.Agent is only validated against the registry
// when the daemon builds its resolver, which happens after ao.daemon.started is
// already emitted. So a malformed value, including a filesystem path, could ride
// on that first event. safeAgentSlug drops anything that is not a plain slug, so
// telemetry carries a known-shape id or nothing, never a path.
var agentSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func safeAgentSlug(agent string) string {
	trimmed := strings.TrimSpace(agent)
	if agentSlugPattern.MatchString(trimmed) {
		return trimmed
	}
	return ""
}

// versionChannel derives stable/nightly from the version string, matching the
// renderer's versionChannelFrom.
func versionChannel(appVersion string) string {
	if strings.Contains(strings.ToLower(appVersion), "-nightly.") {
		return "nightly"
	}
	return "stable"
}

func loadOrCreateInstallID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "telemetry_install_id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read telemetry install id: %w", err)
	}
	id := "ins_" + uuid.NewString()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write telemetry install id: %w", err)
	}
	return id, nil
}

func sha256String(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func telemetryLogger(log *slog.Logger) *slog.Logger {
	if log != nil {
		return log
	}
	return slog.Default()
}
