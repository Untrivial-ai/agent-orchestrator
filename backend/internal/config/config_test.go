package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Clear every recognised var so we observe pure defaults regardless of the
	// surrounding environment.
	for _, k := range []string{"AO_PORT", "AO_REQUEST_TIMEOUT", "AO_SHUTDOWN_TIMEOUT", "AO_RUN_FILE", "AO_DATA_DIR", "AO_AGENT", "AO_PROCESS_CONTAINMENT", "AO_ALLOWED_ORIGINS", "AO_TELEMETRY_EVENTS", "AO_TELEMETRY_METRICS", "AO_TELEMETRY_REMOTE", "AO_TELEMETRY_POSTHOG_KEY", "AO_TELEMETRY_POSTHOG_HOST", "AO_TELEMETRY_DISABLED_EVENTS", "AO_TELEMETRY_APP_VERSION"} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != LoopbackHost {
		t.Errorf("Host = %q, want %q", cfg.Host, LoopbackHost)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("RequestTimeout = %s, want %s", cfg.RequestTimeout, DefaultRequestTimeout)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %s, want %s", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if cfg.ProcessContainment != ProcessContainmentNone {
		t.Fatalf("ProcessContainment = %q, want unset", cfg.ProcessContainment)
	}
	if cfg.RunFilePath == "" {
		t.Error("RunFilePath is empty, want a resolved default path")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	wantRunFilePath := filepath.Join(homeDir, ".ao", "running.json")
	if cfg.RunFilePath != wantRunFilePath {
		t.Errorf("RunFilePath = %q, want %q", cfg.RunFilePath, wantRunFilePath)
	}
	if cfg.DataDir == "" {
		t.Error("DataDir is empty, want a resolved default path")
	}
	wantDataDir := filepath.Join(homeDir, ".ao", "data")
	if cfg.DataDir != wantDataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, wantDataDir)
	}
	if cfg.Telemetry.Remote != TelemetryRemoteOff || cfg.Telemetry.PostHogHost != DefaultTelemetryPostHogHost {
		t.Fatalf("Telemetry defaults = %+v", cfg.Telemetry)
	}
}

func TestLoadAbsolutizesRelativeOverrides(t *testing.T) {
	// A relative override must be resolved to absolute at Load time. The daemon
	// chdir's into its data dir at startup, so a relative path left as-is would
	// be re-resolved against the new cwd and double-nest state.
	t.Setenv("AO_RUN_FILE", "rel-running.json")
	t.Setenv("AO_DATA_DIR", "rel-data")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !filepath.IsAbs(cfg.RunFilePath) {
		t.Errorf("RunFilePath = %q, want absolute", cfg.RunFilePath)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("DataDir = %q, want absolute", cfg.DataDir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, "rel-data"); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
	if want := filepath.Join(cwd, "rel-running.json"); cfg.RunFilePath != want {
		t.Errorf("RunFilePath = %q, want %q", cfg.RunFilePath, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	overrideDir := t.TempDir()
	runFilePath := filepath.Join(overrideDir, "ao-test-running.json")
	dataDir := filepath.Join(overrideDir, "ao-test-data")

	t.Setenv("AO_PORT", "4002")
	t.Setenv("AO_REQUEST_TIMEOUT", "5s")
	t.Setenv("AO_SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("AO_PROCESS_CONTAINMENT", "")
	t.Setenv("AO_RUN_FILE", runFilePath)
	t.Setenv("AO_DATA_DIR", dataDir)
	t.Setenv("AO_TELEMETRY_EVENTS", "on")
	t.Setenv("AO_TELEMETRY_METRICS", "off")
	t.Setenv("AO_TELEMETRY_REMOTE", "posthog")
	t.Setenv("AO_TELEMETRY_POSTHOG_KEY", "phc_test")
	t.Setenv("AO_TELEMETRY_POSTHOG_HOST", "https://eu.i.posthog.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr() != "127.0.0.1:4002" {
		t.Errorf("Addr() = %q, want 127.0.0.1:4002", cfg.Addr())
	}
	if cfg.RequestTimeout != 5*time.Second {
		t.Errorf("RequestTimeout = %s, want 5s", cfg.RequestTimeout)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 3s", cfg.ShutdownTimeout)
	}
	if cfg.RunFilePath != runFilePath {
		t.Errorf("RunFilePath = %q, want %q", cfg.RunFilePath, runFilePath)
	}
	if cfg.DataDir != dataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dataDir)
	}
	if !cfg.Telemetry.Events || cfg.Telemetry.Metrics {
		t.Fatalf("Telemetry toggles = %+v", cfg.Telemetry)
	}
	if cfg.Telemetry.Remote != TelemetryRemotePostHog || cfg.Telemetry.PostHogKey != "phc_test" || cfg.Telemetry.PostHogHost != "https://eu.i.posthog.com" {
		t.Fatalf("Telemetry remote = %+v", cfg.Telemetry)
	}
}

func TestParseProcessContainment(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		goos    string
		want    ProcessContainment
		wantErr string
	}{
		{name: "empty on linux", goos: "linux", want: ProcessContainmentNone},
		{name: "empty on darwin", goos: "darwin", want: ProcessContainmentNone},
		{name: "empty on windows", goos: "windows", want: ProcessContainmentNone},
		{name: "systemd on linux", raw: "systemd", goos: "linux", want: ProcessContainmentSystemd},
		{name: "normalized systemd on linux", raw: " SystemD ", goos: "linux", want: ProcessContainmentSystemd},
		{name: "systemd on darwin", raw: "systemd", goos: "darwin", wantErr: "systemd is Linux-only (GOOS=darwin)"},
		{name: "systemd on windows", raw: "systemd", goos: "windows", wantErr: "systemd is Linux-only (GOOS=windows)"},
		{name: "invalid on linux", raw: "cgroup", goos: "linux", wantErr: "must be unset|systemd"},
		{name: "invalid on darwin", raw: "cgroup", goos: "darwin", wantErr: "must be unset|systemd"},
		{name: "invalid on windows", raw: "cgroup", goos: "windows", wantErr: "must be unset|systemd"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProcessContainment(tc.raw, tc.goos)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseProcessContainment(%q, %q) = %q, nil; want error containing %q", tc.raw, tc.goos, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseProcessContainment(%q, %q) error = %q; want substring %q", tc.raw, tc.goos, err, tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseProcessContainment(%q, %q): %v", tc.raw, tc.goos, err)
			}
			if got != tc.want {
				t.Errorf("parseProcessContainment(%q, %q) = %q, want %q", tc.raw, tc.goos, got, tc.want)
			}
		})
	}
}

func TestLoadProcessContainmentSystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd process containment is Linux-only")
	}
	t.Setenv("AO_PROCESS_CONTAINMENT", " SystemD ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProcessContainment != ProcessContainmentSystemd {
		t.Errorf("ProcessContainment = %q, want systemd", cfg.ProcessContainment)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"non-numeric port", map[string]string{"AO_PORT": "abc"}},
		{"port out of range", map[string]string{"AO_PORT": "70000"}},
		{"bad request timeout", map[string]string{"AO_REQUEST_TIMEOUT": "soon"}},
		{"bad shutdown timeout", map[string]string{"AO_SHUTDOWN_TIMEOUT": "later"}},
		{"bad process containment", map[string]string{"AO_PROCESS_CONTAINMENT": "cgroup"}},
		{"zero request timeout", map[string]string{"AO_REQUEST_TIMEOUT": "0s"}},
		{"negative request timeout", map[string]string{"AO_REQUEST_TIMEOUT": "-1s"}},
		{"zero shutdown timeout", map[string]string{"AO_SHUTDOWN_TIMEOUT": "0s"}},
		{"negative shutdown timeout", map[string]string{"AO_SHUTDOWN_TIMEOUT": "-5s"}},
		{"null origin", map[string]string{"AO_ALLOWED_ORIGINS": "app://renderer,null"}},
		{"wildcard origin", map[string]string{"AO_ALLOWED_ORIGINS": "*"}},
		{"bad telemetry events", map[string]string{"AO_TELEMETRY_EVENTS": "maybe"}},
		{"bad telemetry metrics", map[string]string{"AO_TELEMETRY_METRICS": "maybe"}},
		{"bad telemetry remote", map[string]string{"AO_TELEMETRY_REMOTE": "otlp"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load() = nil error, want error")
			}
		})
	}
}

func TestLoadAllowedOrigins(t *testing.T) {
	t.Run("default includes the packaged renderer origin", func(t *testing.T) {
		t.Setenv("AO_ALLOWED_ORIGINS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		found := false
		for _, origin := range cfg.AllowedOrigins {
			if origin == "app://renderer" {
				found = true
			}
		}
		if !found {
			t.Errorf("AllowedOrigins = %v, want app://renderer included", cfg.AllowedOrigins)
		}
	})

	t.Run("override replaces defaults and trims entries", func(t *testing.T) {
		t.Setenv("AO_ALLOWED_ORIGINS", " app://renderer , http://localhost:9999 ,")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := []string{"app://renderer", "http://localhost:9999"}
		if len(cfg.AllowedOrigins) != len(want) {
			t.Fatalf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
		}
		for i, origin := range want {
			if cfg.AllowedOrigins[i] != origin {
				t.Errorf("AllowedOrigins[%d] = %q, want %q", i, cfg.AllowedOrigins[i], origin)
			}
		}
	})
}

// The kill switch and the supervisor-supplied version are user-visible
// boundaries: the daemon reads them from the environment the desktop app hands
// it, so a parsing regression here silently disables the switch.
func TestLoadTelemetryDisabledEventsAndAppVersion(t *testing.T) {
	t.Setenv("AO_TELEMETRY_DISABLED_EVENTS", " ao.v2.app.active , ao.renderer.* ,, ")
	t.Setenv("AO_TELEMETRY_APP_VERSION", "  0.11.2  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"ao.v2.app.active", "ao.renderer.*"}
	if len(cfg.Telemetry.DisabledEvents) != len(want) {
		t.Fatalf("DisabledEvents = %#v, want %#v", cfg.Telemetry.DisabledEvents, want)
	}
	for i, name := range want {
		if cfg.Telemetry.DisabledEvents[i] != name {
			t.Fatalf("DisabledEvents[%d] = %q, want %q", i, cfg.Telemetry.DisabledEvents[i], name)
		}
	}
	if cfg.Telemetry.AppVersion != "0.11.2" {
		t.Fatalf("AppVersion = %q, want trimmed 0.11.2", cfg.Telemetry.AppVersion)
	}
}

// An unparseable or blank list must never stop the daemon booting: the switch
// has to be usable in a hurry, so a bad entry is inert rather than fatal.
func TestLoadTelemetryDisabledEventsBlankIsInert(t *testing.T) {
	t.Setenv("AO_TELEMETRY_DISABLED_EVENTS", " , , ")
	t.Setenv("AO_TELEMETRY_APP_VERSION", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Telemetry.DisabledEvents) != 0 {
		t.Fatalf("DisabledEvents = %#v, want empty", cfg.Telemetry.DisabledEvents)
	}
	if cfg.Telemetry.AppVersion != "" {
		t.Fatalf("AppVersion = %q, want empty", cfg.Telemetry.AppVersion)
	}
}

func TestLoadGitLabDefaults(t *testing.T) {
	// Clear the GitLab config vars so we observe pure defaults.
	for _, k := range []string{"AO_GITLAB_ALLOWED_HOSTS", "AO_GITLAB_HOST_TOKENS"} {
		t.Setenv(k, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitLab.AllowedHosts != nil {
		t.Errorf("GitLab.AllowedHosts = %v, want nil", cfg.GitLab.AllowedHosts)
	}
	if cfg.GitLab.HostTokens != nil {
		t.Errorf("GitLab.HostTokens = %v, want nil", cfg.GitLab.HostTokens)
	}
}

func TestLoadGitLabAllowedHosts(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{"single host", "gitlab.mycompany.com", []string{"gitlab.mycompany.com"}},
		{"comma-separated", "gitlab.mycompany.com,gitlab.internal.corp", []string{"gitlab.mycompany.com", "gitlab.internal.corp"}},
		{"trimmed whitespace", " gitlab.mycompany.com , gitlab.internal.corp ", []string{"gitlab.mycompany.com", "gitlab.internal.corp"}},
		{"empty entries skipped", "gitlab.mycompany.com,,gitlab.internal.corp,", []string{"gitlab.mycompany.com", "gitlab.internal.corp"}},
		{"with port", "gitlab.internal:8443", []string{"gitlab.internal:8443"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_GITLAB_ALLOWED_HOSTS", tc.env)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.GitLab.AllowedHosts) != len(tc.want) {
				t.Fatalf("AllowedHosts = %v, want %v", cfg.GitLab.AllowedHosts, tc.want)
			}
			for i, h := range tc.want {
				if cfg.GitLab.AllowedHosts[i] != h {
					t.Errorf("AllowedHosts[%d] = %q, want %q", i, cfg.GitLab.AllowedHosts[i], h)
				}
			}
		})
	}
}

func TestLoadGitLabHostTokens(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want map[string]string
	}{
		{
			name: "single host=token",
			env:  "gitlab.mycompany.com=token-1",
			want: map[string]string{"gitlab.mycompany.com": "token-1"},
		},
		{
			name: "multiple host=token pairs",
			env:  "gitlab.mycompany.com=token-1,gitlab.internal.corp=token-2",
			want: map[string]string{
				"gitlab.mycompany.com": "token-1",
				"gitlab.internal.corp": "token-2",
			},
		},
		{
			name: "trimmed whitespace around entries",
			env:  " gitlab.mycompany.com = token-1 , gitlab.internal.corp = token-2 ",
			want: map[string]string{
				"gitlab.mycompany.com": "token-1",
				"gitlab.internal.corp": "token-2",
			},
		},
		{
			name: "host with port",
			env:  "gitlab.internal:8443=token-port",
			want: map[string]string{"gitlab.internal:8443": "token-port"},
		},
		{
			name: "empty entries skipped",
			env:  "gitlab.mycompany.com=token-1,,",
			want: map[string]string{"gitlab.mycompany.com": "token-1"},
		},
		{
			name: "entry without equals skipped",
			env:  "gitlab.mycompany.com=token-1,not-a-pair",
			want: map[string]string{"gitlab.mycompany.com": "token-1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_GITLAB_HOST_TOKENS", tc.env)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.GitLab.HostTokens) != len(tc.want) {
				t.Fatalf("HostTokens = %v, want %v", cfg.GitLab.HostTokens, tc.want)
			}
			for k, v := range tc.want {
				got, ok := cfg.GitLab.HostTokens[k]
				if !ok {
					t.Errorf("HostTokens missing key %q", k)
					continue
				}
				if got != v {
					t.Errorf("HostTokens[%q] = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestLoadGitLabInvalidHostTokens(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{"token contains equals", "gitlab.mycompany.com=token=with=equals"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_GITLAB_HOST_TOKENS", tc.env)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() = nil error, want error for malformed AO_GITLAB_HOST_TOKENS")
			}
		})
	}
}
