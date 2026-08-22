// Package config loads the daemon's runtime configuration. The HTTP daemon is
// a loopback-only sidecar: it binds 127.0.0.1, takes no public traffic, and
// reads everything it needs from the environment with sane defaults so it can
// boot with zero configuration in development.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// LoopbackHost is the only host the daemon ever binds. There is deliberately
	// no AO_HOST env var: the daemon has no auth/CORS/TLS and a stray
	// AO_HOST=0.0.0.0 would turn it into a public no-auth service. If a
	// non-default loopback (e.g. ::1, 127.0.0.2) is ever needed, add it back with
	// an IsLoopback() validator — not a raw env read.
	LoopbackHost = "127.0.0.1"
	// DefaultPort is the single port for REST, terminal mux, health, and control.
	DefaultPort = 3001
	// DefaultRequestTimeout bounds a single REST request. Long-lived terminal mux
	// connections are mounted outside this timeout.
	DefaultRequestTimeout = 60 * time.Second
	// DefaultShutdownTimeout is the hard cap on graceful shutdown. After this
	// the process exits even if connections are still draining.
	DefaultShutdownTimeout = 10 * time.Second
	// DefaultAgent is the compatibility value used when AO_AGENT is unset. The
	// daemon validates it at startup, but worker/orchestrator spawns resolve from
	// explicit requests or project role config instead of falling back to it.
	DefaultAgent = "claude-code"
	// DefaultTelemetryPostHogHost is the default PostHog ingestion host when
	// remote telemetry is enabled and AO_TELEMETRY_POSTHOG_HOST is unset.
	DefaultTelemetryPostHogHost = "https://us.i.posthog.com"
)

// TelemetryRemote selects the remote telemetry exporter.
type TelemetryRemote string

const (
	// TelemetryRemoteOff disables remote telemetry export.
	TelemetryRemoteOff TelemetryRemote = "off"
	// TelemetryRemotePostHog exports allowlisted events to PostHog.
	TelemetryRemotePostHog TelemetryRemote = "posthog"
)

// ProcessContainment selects the operating-system boundary used for worker
// processes. The zero value preserves the historical tmux session-id reaper.
type ProcessContainment string

const (
	// ProcessContainmentNone keeps the existing tmux session-id reaper.
	ProcessContainmentNone ProcessContainment = ""
	// ProcessContainmentSystemd opts into Linux systemd user-scope containment.
	ProcessContainmentSystemd ProcessContainment = "systemd"
)

// TelemetryConfig controls local and remote telemetry behavior.
type TelemetryConfig struct {
	Events      bool
	Metrics     bool
	Remote      TelemetryRemote
	PostHogKey  string
	PostHogHost string
	// DisabledEvents names event streams that must never reach the remote
	// (billed) sink. This is the kill switch: a stream that turns out to be
	// noisy or expensive can be silenced by configuration, without waiting for
	// users to install a new build. Local storage still records everything.
	DisabledEvents []string
	// AppVersion is the desktop app version the daemon was launched by, stamped
	// on remote events so failures can be attributed to a release. The daemon
	// binary has no reliable version of its own (see cli.Version, which release
	// tooling does not currently override), so the supervisor passes it in.
	AppVersion string
}

// GitLabConfig carries the self-managed GitLab host allowlist and per-host
// token overrides. It is loaded once at daemon boot from environment variables
// (no hot-reload), matching the existing config pattern. gitlab.com is always
// allowed (hardcoded in the provider) and does not need to appear here.
type GitLabConfig struct {
	// AllowedHosts is the list of self-managed GitLab hosts (each may include a
	// port, e.g. "gitlab.internal:8443"). gitlab.com is always allowed and is not
	// included here.
	AllowedHosts []string
	// HostTokens maps a self-managed host to a token override. Hosts in
	// AllowedHosts without an explicit entry fall back to the default token
	// (AO_GITLAB_TOKEN / GITLAB_TOKEN / glab).
	HostTokens map[string]string
}

// DefaultAllowedOrigins are the browser origins the daemon's CORS boundary
// trusts, beyond loopback-served content (which the middleware always trusts —
// local pages can reach the no-auth daemon directly anyway). The daemon has no
// auth, so every entry must be an origin web content cannot present:
// app://renderer is the packaged Electron renderer, served from a custom
// scheme only the desktop app registers — no website can bear it. The opaque
// "null" origin (file:// pages, sandboxed iframes on any website) must never
// be added.
var DefaultAllowedOrigins = []string{
	"app://renderer",
}

// Config is the fully-resolved daemon configuration. It is immutable once
// built by Load.
type Config struct {
	// Host is the bind address. Always loopback — see LoopbackHost.
	Host string
	// Port is the TCP port to bind. The daemon fails fast if it is taken.
	Port int
	// RequestTimeout bounds REST request handling.
	RequestTimeout time.Duration
	// ShutdownTimeout is the hard graceful-shutdown deadline.
	ShutdownTimeout time.Duration
	// RunFilePath is where the PID + port handshake file (running.json) is
	// written so the Electron supervisor can discover and reap the daemon.
	RunFilePath string
	// DataDir is the directory holding durable SQLite state: DB and WAL files.
	// It is created on first use by the storage layer.
	DataDir string
	// Agent is the compatibility agent adapter id selected by AO_AGENT;
	// startSession fails fast if no adapter with this id is registered.
	Agent string
	// AppRunID identifies one desktop-app launch. The Electron supervisor mints
	// it and passes it down (AO_APP_RUN_ID), holding it constant across daemon
	// restarts it performs, so standalone shell terminals can survive a daemon
	// restart while still being reaped when the APP itself goes away.
	//
	// Empty means no supervising app (a bare `ao daemon`): the daemon mints a
	// fresh id per boot, which correctly makes any surviving shell terminals
	// from an earlier run look like orphans and get cleaned up.
	AppRunID string
	// AllowedOrigins are the browser origins granted CORS read access (see
	// DefaultAllowedOrigins). Overridden by AO_ALLOWED_ORIGINS.
	AllowedOrigins []string
	// Telemetry controls local/remote telemetry sinks.
	Telemetry TelemetryConfig
	// StartupWorkingDirectory is the daemon process cwd before startup
	// normalizes it. The desktop uses this to identify dev daemons after the
	// process cwd is moved to the stable data dir.
	StartupWorkingDirectory string
	// ProcessContainment selects the worker process ownership boundary.
	// Unset preserves the historical tmux session-id reaper; systemd is an
	// explicit Linux-only opt-in.
	ProcessContainment ProcessContainment
	// GitLab carries the self-managed GitLab host allowlist and per-host
	// token overrides, loaded once at boot from environment variables.
	GitLab GitLabConfig
}

// Addr returns the host:port the HTTP server binds. It uses net.JoinHostPort so
// the result is correct for IPv6 literals as well as IPv4 / hostnames.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Load resolves configuration from the environment, applying defaults. It
// returns an error only for values that are present but malformed (e.g. a
// non-numeric AO_PORT); missing values fall back to defaults.
//
// Recognised variables:
//
//	AO_PORT              bind port           (default 3001)
//	AO_REQUEST_TIMEOUT   per-request timeout (Go duration > 0, default 60s)
//	AO_SHUTDOWN_TIMEOUT  shutdown deadline   (Go duration > 0, default 10s)
//	AO_RUN_FILE          running.json path   (default ~/.ao/running.json)
//	AO_DATA_DIR          durable state dir   (default ~/.ao/data)
//	AO_AGENT             compatibility agent id (default claude-code)
//	AO_APP_RUN_ID        desktop-app launch id, set by the Electron supervisor
//	                     (default: a fresh id minted per daemon boot)
//	AO_ALLOWED_ORIGINS   CORS origins, comma-separated (default DefaultAllowedOrigins)
//	AO_TELEMETRY_EVENTS  local event capture off|on (default off)
//	AO_TELEMETRY_METRICS local metric capture off|on (default off)
//	AO_TELEMETRY_REMOTE  remote exporter off|posthog (default off)
//	AO_TELEMETRY_POSTHOG_KEY   PostHog project key
//	AO_TELEMETRY_POSTHOG_HOST  PostHog host (default DefaultTelemetryPostHogHost)
//	AO_PROCESS_CONTAINMENT     worker process boundary (unset|systemd)
//	AO_GITLAB_ALLOWED_HOSTS    comma-separated self-managed GitLab hosts (each may include :port)
//	AO_GITLAB_HOST_TOKENS      host=token,host=token per-host token overrides
//
// The bind host is not configurable: the daemon is loopback-only by design.
func Load() (Config, error) {
	cfg := Config{
		Host:            LoopbackHost,
		Port:            DefaultPort,
		RequestTimeout:  DefaultRequestTimeout,
		ShutdownTimeout: DefaultShutdownTimeout,
		Agent:           DefaultAgent,
		AllowedOrigins:  DefaultAllowedOrigins,
		Telemetry: TelemetryConfig{
			Remote:      TelemetryRemoteOff,
			PostHogHost: DefaultTelemetryPostHogHost,
		},
	}

	if raw := os.Getenv("AO_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AO_PORT %q: %w", raw, err)
		}
		if port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("invalid AO_PORT %d: out of range 1-65535", port)
		}
		cfg.Port = port
	}

	if raw := os.Getenv("AO_REQUEST_TIMEOUT"); raw != "" {
		d, err := parsePositiveDuration("AO_REQUEST_TIMEOUT", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.RequestTimeout = d
	}

	if raw := os.Getenv("AO_SHUTDOWN_TIMEOUT"); raw != "" {
		d, err := parsePositiveDuration("AO_SHUTDOWN_TIMEOUT", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.ShutdownTimeout = d
	}

	if raw := os.Getenv("AO_AGENT"); raw != "" {
		cfg.Agent = raw
	}

	containment, err := parseProcessContainment(os.Getenv("AO_PROCESS_CONTAINMENT"), runtime.GOOS)
	if err != nil {
		return Config{}, err
	}
	cfg.ProcessContainment = containment

	// A missing AO_APP_RUN_ID means nothing is supervising this daemon, so this
	// boot IS the run: mint an id rather than leaving it empty, which would make
	// every boot share one run id and defeat orphan detection entirely.
	if raw := os.Getenv("AO_APP_RUN_ID"); raw != "" {
		cfg.AppRunID = raw
	} else {
		cfg.AppRunID = newAppRunID()
	}

	if raw, ok := os.LookupEnv("AO_ALLOWED_ORIGINS"); ok && raw != "" {
		// Explicit override replaces the defaults entirely so a deployment can
		// also narrow the list. The "null" origin is rejected, never silently
		// dropped: an operator allowing it would open the no-auth daemon to
		// every sandboxed iframe on the web.
		origins := make([]string, 0, 4)
		for _, origin := range strings.Split(raw, ",") {
			origin = strings.TrimSpace(origin)
			if origin == "" {
				continue
			}
			if origin == "null" || origin == "*" {
				return Config{}, fmt.Errorf("invalid AO_ALLOWED_ORIGINS entry %q: wildcard and null origins are not allowed", origin)
			}
			origins = append(origins, origin)
		}
		cfg.AllowedOrigins = origins
	}

	if raw := os.Getenv("AO_TELEMETRY_EVENTS"); raw != "" {
		v, err := parseToggleEnv("AO_TELEMETRY_EVENTS", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.Telemetry.Events = v
	}
	if raw := os.Getenv("AO_TELEMETRY_METRICS"); raw != "" {
		v, err := parseToggleEnv("AO_TELEMETRY_METRICS", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.Telemetry.Metrics = v
	}
	if raw := os.Getenv("AO_TELEMETRY_REMOTE"); raw != "" {
		remote, err := parseTelemetryRemote(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AO_TELEMETRY_REMOTE %q: %w", raw, err)
		}
		cfg.Telemetry.Remote = remote
	}
	if raw := os.Getenv("AO_TELEMETRY_POSTHOG_KEY"); raw != "" {
		cfg.Telemetry.PostHogKey = raw
	}
	if raw := os.Getenv("AO_TELEMETRY_POSTHOG_HOST"); raw != "" {
		cfg.Telemetry.PostHogHost = raw
	}
	if raw := os.Getenv("AO_TELEMETRY_DISABLED_EVENTS"); raw != "" {
		cfg.Telemetry.DisabledEvents = parseTelemetryDisabledEvents(raw)
	}
	if raw := os.Getenv("AO_TELEMETRY_APP_VERSION"); raw != "" {
		cfg.Telemetry.AppVersion = strings.TrimSpace(raw)
	}

	if raw, ok := os.LookupEnv("AO_GITLAB_ALLOWED_HOSTS"); ok && raw != "" {
		hosts := make([]string, 0, 4)
		for _, h := range strings.Split(raw, ",") {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			hosts = append(hosts, h)
		}
		cfg.GitLab.AllowedHosts = hosts
	}

	if raw, ok := os.LookupEnv("AO_GITLAB_HOST_TOKENS"); ok && raw != "" {
		tokens, err := parseHostTokenMap("AO_GITLAB_HOST_TOKENS", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.GitLab.HostTokens = tokens
	}

	runFile, err := resolveRunFilePath()
	if err != nil {
		return Config{}, err
	}
	cfg.RunFilePath = runFile

	dataDir, err := resolveDataDir()
	if err != nil {
		return Config{}, err
	}
	cfg.DataDir = dataDir

	return cfg, nil
}

func parseProcessContainment(raw, goos string) (ProcessContainment, error) {
	containment := ProcessContainment(strings.ToLower(strings.TrimSpace(raw)))
	switch containment {
	case ProcessContainmentNone:
		return ProcessContainmentNone, nil
	case ProcessContainmentSystemd:
		if goos != "linux" {
			return ProcessContainmentNone, fmt.Errorf("invalid AO_PROCESS_CONTAINMENT %q: systemd is Linux-only (GOOS=%s)", raw, goos)
		}
		return ProcessContainmentSystemd, nil
	default:
		return ProcessContainmentNone, fmt.Errorf("invalid AO_PROCESS_CONTAINMENT %q: must be unset|systemd", raw)
	}
}

func parseToggleEnv(name, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be off|on", name)
	}
}

func parseTelemetryRemote(raw string) (TelemetryRemote, error) {
	switch TelemetryRemote(strings.ToLower(strings.TrimSpace(raw))) {
	case TelemetryRemoteOff:
		return TelemetryRemoteOff, nil
	case TelemetryRemotePostHog:
		return TelemetryRemotePostHog, nil
	default:
		return "", fmt.Errorf("must be off|posthog")
	}
}

// parseTelemetryDisabledEvents reads the comma-separated kill-switch list.
// Unlike the other telemetry env vars this never fails: an unparseable or
// misspelled entry must not stop the daemon from booting, because the whole
// point of the switch is to be usable in a hurry during an incident. An entry
// that matches no event name is simply inert.
func parseTelemetryDisabledEvents(raw string) []string {
	var names []string
	for _, part := range strings.Split(raw, ",") {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// parseHostTokenMap parses a host=token,host=token map. Whitespace around
// entries, hosts, and tokens is trimmed. Empty entries and entries without an
// equals sign are skipped. A token containing an equals sign is rejected as
// ambiguous (a token value with embedded '=' would be indistinguishable from
// a malformed entry).
func parseHostTokenMap(name, raw string) (map[string]string, error) {
	tokens := make(map[string]string, 4)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eq := strings.IndexByte(entry, '=')
		if eq < 0 {
			continue // skip entries without an equals sign
		}
		host := strings.TrimSpace(entry[:eq])
		token := strings.TrimSpace(entry[eq+1:])
		if host == "" {
			continue
		}
		// Reject tokens containing '=' — they would be ambiguous on re-parse
		// and likely indicate a malformed entry (e.g. host=token=with=equals).
		if strings.ContainsRune(token, '=') {
			return nil, fmt.Errorf("invalid %s entry %q: token contains '='", name, entry)
		}
		tokens[host] = token
	}
	return tokens, nil
}

// parsePositiveDuration rejects zero and negative durations: a zero
// RequestTimeout would expire every request instantly, and a non-positive
// ShutdownTimeout would defeat graceful shutdown.
func parsePositiveDuration(name, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", name, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid %s %q: must be > 0", name, raw)
	}
	return d, nil
}

// newAppRunID mints the fallback launch id used when no supervising app
// supplied one. Randomness (not a timestamp or PID) is what guarantees two
// boots never collide, which is what orphan detection relies on. A failure to
// read entropy falls back to the boot time — worse, but still monotonic enough
// to distinguish runs, and never worth refusing to start the daemon over.
func newAppRunID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "apprun-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "apprun-" + hex.EncodeToString(buf)
}

// resolveRunFilePath picks where running.json lives. An explicit AO_RUN_FILE
// wins; otherwise it sits under the canonical AO home directory so the CLI and
// Electron supervisor share one handshake location.
func resolveRunFilePath() (string, error) {
	if p, ok := os.LookupEnv("AO_RUN_FILE"); ok && p != "" {
		return absOverride("AO_RUN_FILE", p)
	}
	stateDir, err := defaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "running.json"), nil
}

// resolveDataDir picks where durable state (the SQLite DB) lives. An explicit
// AO_DATA_DIR wins; otherwise it defaults under the same canonical AO home
// directory as the run-file.
func resolveDataDir() (string, error) {
	if p, ok := os.LookupEnv("AO_DATA_DIR"); ok && p != "" {
		return absOverride("AO_DATA_DIR", p)
	}
	stateDir, err := defaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "data"), nil
}

func defaultStateDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve state dir: %w", err)
	}
	return filepath.Join(homeDir, ".ao"), nil
}

// absOverride resolves an explicit AO_DATA_DIR/AO_RUN_FILE override to an
// absolute path against the process's launch cwd. The daemon chdir's into its
// data dir at startup (see stabilizeWorkingDirectory), so a relative override
// left as-is would be re-resolved against the new cwd and double-nest state
// (e.g. AO_DATA_DIR=data -> <cwd>/data/data). Absolutizing here keeps the path
// stable regardless of the later chdir.
func absOverride(name, p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", name, p, err)
	}
	return abs, nil
}
