package kilocode

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestKiloLocalEvidenceIsConfigured(t *testing.T) {
	for _, source := range []string{"environment", "auth file", "auth list", "injected auth"} {
		t.Run(source, func(t *testing.T) {
			p := kiloAuthFixture(t, "0 credentials\n", "")
			switch source {
			case "environment":
				t.Setenv("OPENAI_API_KEY", "secret")
			case "auth file":
				writeKiloAuthFile(t, "auth.json", "{\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}")
			case "auth list":
				p = kiloAuthFixture(t, "OpenAI api\n1 credential\n", "")
			case "injected auth":
				t.Setenv("KILO_AUTH_CONTENT", "{\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}")
			}
			got, err := p.AuthStatus(context.Background())
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("AuthStatus = %q, %v; want configured", got, err)
			}
		})
	}
}

func TestKiloScopedProviderEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, model, content string
		env                  map[string]string
		want                 ports.AgentAuthStatus
	}{
		{"matching env", "openai/gpt-5", "", map[string]string{"OPENAI_API_KEY": "key"}, ports.AgentAuthStatusConfigured},
		{"unrelated env", "anthropic/claude", "", map[string]string{"OPENAI_API_KEY": "key"}, ports.AgentAuthStatusUnknown},
		{"matching file", "openai/gpt-5", "{\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}", nil, ports.AgentAuthStatusConfigured},
		{"unrelated file", "anthropic/claude", "{\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}", nil, ports.AgentAuthStatusUnknown},
		{"valid oauth", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"secret\",\"refresh\":\"\",\"expires\":4102444800000}}", nil, ports.AgentAuthStatusConfigured},
		{"expired oauth", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"secret\",\"refresh\":\"\",\"expires\":1}}", nil, ports.AgentAuthStatusUnknown},
		{"refreshable oauth", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"old\",\"refresh\":\"refresh\",\"expires\":1}}", nil, ports.AgentAuthStatusConfigured},
		{"malformed expiry", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"secret\",\"refresh\":\"refresh\",\"expires\":\"bad\"}}", nil, ports.AgentAuthStatusUnknown},
		{"missing expiry", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"secret\",\"refresh\":\"refresh\"}}", nil, ports.AgentAuthStatusUnknown},
		{"untyped token", "openai/gpt-5", "{\"openai\":{\"token\":\"secret\"}}", nil, ports.AgentAuthStatusUnknown},
		{"bootstrap is not model auth", "openai/gpt-5", "{\"openai\":{\"type\":\"wellknown\",\"key\":\"name\",\"token\":\"secret\"}}", nil, ports.AgentAuthStatusUnknown},
		{"malformed sibling", "openai/gpt-5", "{\"broken\":7,\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}", nil, ports.AgentAuthStatusConfigured},
		{"malformed json", "openai/gpt-5", "{", nil, ports.AgentAuthStatusUnknown},
		{"free kilo model", "kilo/minimax/minimax-m2.5:free", "", nil, ports.AgentAuthStatusNotApplicable},
		{"paid kilo model", "kilo/anthropic/claude-opus-4.6", "", nil, ports.AgentAuthStatusUnknown},
		{"unrelated free suffix", "openai/custom:free", "", nil, ports.AgentAuthStatusUnknown},
		{"free OpenRouter still needs auth", "openrouter/minimax/minimax-m2.5:free", "", nil, ports.AgentAuthStatusUnknown},
		{"local model", "ollama/llama3", "", nil, ports.AgentAuthStatusNotApplicable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := kiloAuthFixture(t, "0 credentials\n", "")
			if tc.content != "" {
				writeKiloAuthFile(t, "auth.json", tc.content)
			}
			got := kiloScopedStatus(t, p, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: tc.model}, Env: tc.env})
			if got != tc.want {
				t.Fatalf("status = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestKiloInjectedAuthIsolatesHostCredentials(t *testing.T) {
	for _, content := range []string{"", "{}", "{", "{\"anthropic\":{\"type\":\"api\",\"key\":\"other\"}}"} {
		t.Run(content, func(t *testing.T) {
			p := kiloAuthFixture(t, "OpenAI api\n1 credential\n", "")
			writeKiloAuthFile(t, "auth.json", "{\"openai\":{\"type\":\"api\",\"key\":\"host\"}}")
			got := kiloScopedStatus(t, p, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}, Env: map[string]string{"KILO_AUTH_CONTENT": content}})
			if got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q; want unknown for isolated content", got)
			}
		})
	}
}

func TestKiloSelectedOptionsAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name, model, config string
		want                ports.AgentAuthStatus
	}{
		{"selected", "custom/model", "{\"provider\":{\"custom\":{\"options\":{\"apiKey\":\"secret\"}}}}", ports.AgentAuthStatusConfigured},
		{"unrelated", "openai/gpt-5", "{\"provider\":{\"custom\":{\"options\":{\"apiKey\":\"secret\"}}}}", ports.AgentAuthStatusUnknown},
		{"config model", "", "{\"model\":\"custom/model\",\"provider\":{\"custom\":{\"options\":{\"apiKey\":\"secret\"}}}}", ports.AgentAuthStatusConfigured},
		{"unresolved env", "custom/model", "{\"provider\":{\"custom\":{\"options\":{\"apiKey\":\"{env:MISSING_TEST_KEY}\"}}}}", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := kiloAuthFixture(t, "0 credentials\n", "")
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "kilo.json"), []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			got := kiloScopedStatus(t, p, ports.AgentAuthCheck{WorkingDir: workspace, Config: ports.AgentConfig{Model: tc.model}})
			if got != tc.want {
				t.Fatalf("status = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestKiloCurrentCredentialDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, integration, payload string
		active                     any
		want                       ports.AgentAuthStatus
	}{
		{"key", "openai", "{\"type\":\"key\",\"key\":\"secret\"}", nil, ports.AgentAuthStatusConfigured},
		{"unrelated", "anthropic", "{\"type\":\"key\",\"key\":\"secret\"}", nil, ports.AgentAuthStatusUnknown},
		{"empty", "openai", "{\"type\":\"key\",\"key\":\" \"}", nil, ports.AgentAuthStatusUnknown},
		{"unknown payload", "openai", "{\"token\":\"secret\"}", nil, ports.AgentAuthStatusUnknown},
		{"disabled", "openai", "{\"type\":\"key\",\"key\":\"secret\"}", 0, ports.AgentAuthStatusUnknown},
		{"oauth", "openai", "{\"type\":\"oauth\",\"methodID\":\"chatgpt-browser\",\"access\":\"secret\",\"refresh\":\"\",\"expires\":4102444800000}", 1, ports.AgentAuthStatusConfigured},
		{"expired", "openai", "{\"type\":\"oauth\",\"methodID\":\"chatgpt-browser\",\"access\":\"secret\",\"refresh\":\"\",\"expires\":1}", 1, ports.AgentAuthStatusUnknown},
		{"refreshable", "openai", "{\"type\":\"oauth\",\"methodID\":\"chatgpt-browser\",\"access\":\"old\",\"refresh\":\"refresh\",\"expires\":1}", 1, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := kiloAuthFixture(t, "0 credentials\n", "")
			path := filepath.Join(t.TempDir(), "credential #?.db")
			db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE credential(id text PRIMARY KEY,integration_id text,label text,value text,connector_id text,method_id text,active integer,time_created integer,time_updated integer)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO credential(id,integration_id,label,value,active,time_created) VALUES ('cred_1',?,'default',?,?,1)", tc.integration, tc.payload, tc.active); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got := kiloScopedStatus(t, p, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}, Env: map[string]string{"KILO_DB": path}})
			if got != tc.want {
				t.Fatalf("status = %q; want %q", got, tc.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("auth probe changed database")
			}
		})
	}
}

func TestKiloDBPathCommandAndActiveAccount(t *testing.T) {
	for _, tc := range []struct {
		name, active, token, refresh string
		expiry                       int64
		want                         ports.AgentAuthStatus
	}{
		{"selected account", "acct", "token", "", 4102444800000, ports.AgentAuthStatusConfigured},
		{"dangling selection", "other", "token", "", 4102444800000, ports.AgentAuthStatusUnknown},
		{"empty token", "acct", "", "", 4102444800000, ports.AgentAuthStatusUnknown},
		{"expired", "acct", "token", "", 1, ports.AgentAuthStatusUnknown},
		{"refreshable", "acct", "token", "refresh", 1, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "official.db")
			p := kiloAuthFixture(t, "0 credentials\n", path)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE account(id text,access_token text,refresh_token text,token_expiry integer);CREATE TABLE account_state(active_account_id text)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO account VALUES('acct',?,?,?)", tc.token, tc.refresh, tc.expiry); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO account_state VALUES(?)", tc.active); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			got := kiloScopedStatus(t, p, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "kilo/anthropic/claude"}})
			if got != tc.want {
				t.Fatalf("status = %q; want %q", got, tc.want)
			}
			got = kiloScopedStatus(t, p, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}})
			if got != ports.AgentAuthStatusUnknown {
				t.Fatalf("unrelated account status = %q; want unknown", got)
			}
		})
	}
}

func TestKiloIgnoresUnsupportedDataDir(t *testing.T) {
	p := kiloAuthFixture(t, "0 credentials\n", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{\"openai\":{\"type\":\"api\",\"key\":\"unsupported\"}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILO_DATA_DIR", dir)
	got, err := p.AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, %v; want unknown", got, err)
	}
}

func TestKiloAuthListCounts(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   ports.AgentAuthStatus
		known  bool
	}{
		{"0 credentials\n", ports.AgentAuthStatusUnknown, true},
		{"0 credentials\n0 environment variables", ports.AgentAuthStatusUnknown, true},
		{"2 credentials\n", ports.AgentAuthStatusConfigured, true},
		{"0 credentials\nOpenAI OPENAI_API_KEY\n1 environment variable", ports.AgentAuthStatusConfigured, true},
		{"credential service unavailable", ports.AgentAuthStatusUnknown, false},
	} {
		got, known := kilocodeAuthListStatus(tc.output)
		if got != tc.want || known != tc.known {
			t.Fatalf("output %q: %q, %v; want %q, %v", tc.output, got, known, tc.want, tc.known)
		}
	}
}

func TestKiloRelativeDatabaseUsesNativeDataDirectory(t *testing.T) {
	p := kiloAuthFixture(t, "0 credentials\n", "")
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "kilo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "custom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE credential(integration_id text,value text,active integer,time_created integer); INSERT INTO credential VALUES('openai','{"type":"key","key":"secret"}',NULL,1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got := kiloScopedStatus(t, p, ports.AgentAuthCheck{WorkingDir: t.TempDir(), Config: ports.AgentConfig{Model: "openai/gpt-5"}, Env: map[string]string{"KILO_DB": "custom.db"}})
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("relative database status = %q; want configured", got)
	}
}

func TestKiloInjectedRunnerAndClock(t *testing.T) {
	t.Setenv("KILO_AUTH_CONTENT", "")
	if err := os.Unsetenv("KILO_AUTH_CONTENT"); err != nil {
		t.Fatal(err)
	}
	deps := authutil.Dependencies{Getenv: func(string) string { return "" }, Now: func() time.Time { return time.UnixMilli(1000) }, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "injected-kilo" {
			t.Fatalf("unexpected executable %q", name)
		}
		if reflect.DeepEqual(args, []string{"db", "path"}) {
			return []byte("/missing/auth.db\n"), nil
		}
		if reflect.DeepEqual(args, []string{"auth", "list"}) {
			return []byte("1 credential\n"), nil
		}
		t.Fatalf("unexpected arguments %q", args)
		return nil, errors.New("unexpected command")
	}}
	got, err := kilocodeAuthStatusFor(context.Background(), "injected-kilo", ports.AgentAuthCheck{}, deps)
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, %v; want configured", got, err)
	}
	got, err = kilocodeAuthStatusFor(context.Background(), "injected-kilo", ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}, Env: map[string]string{"KILO_AUTH_CONTENT": `{"openai":{"type":"oauth","access":"token","refresh":"","expires":1001}}`}}, deps)
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("clock status = %q, %v; want configured", got, err)
	}
}

func TestKiloConfigMergePreservesOmittedAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name, project string
		want          ports.AgentAuthStatus
	}{
		{"provider metadata", `{"provider":{"custom":{"name":"Project"}}}`, ports.AgentAuthStatusConfigured},
		{"non-key option", `{"provider":{"custom":{"options":{"baseURL":"https://example.invalid"}}}}`, ports.AgentAuthStatusConfigured},
		{"explicit empty key", `{"provider":{"custom":{"options":{"apiKey":""}}}}`, ports.AgentAuthStatusUnknown},
		{"explicit replacement", `{"provider":{"custom":{"options":{"apiKey":"{env:MISSING_KEY}"}}}}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			path := filepath.Join(home, ".config", "kilo", "kilo.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"model":"custom/model","provider":{"custom":{"options":{"apiKey":"global-key"}}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "kilo.json"), []byte(tc.project), 0o600); err != nil {
				t.Fatal(err)
			}
			deps := authutil.Dependencies{Getenv: func(name string) string {
				if name == "HOME" {
					return home
				}
				return ""
			}, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }}
			got, err := kilocodeAuthStatusFor(context.Background(), "injected-kilo", ports.AgentAuthCheck{WorkingDir: workspace}, deps)
			if err != nil || got != tc.want {
				t.Fatalf("merged config = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestKiloWindowsProfilePaths(t *testing.T) {
	// The injected environment never falls through to the machine's real home.
	t.Setenv("KILO_AUTH_CONTENT", "")
	if err := os.Unsetenv("KILO_AUTH_CONTENT"); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"auth", "config", "relative database"} {
		t.Run(source, func(t *testing.T) {
			profile := t.TempDir()
			env := map[string]string{"USERPROFILE": profile}
			dataDir := filepath.Join(profile, ".local", "share", "kilo")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			switch source {
			case "auth":
				if err := os.WriteFile(filepath.Join(dataDir, "auth.json"), []byte(`{"openai":{"type":"api","key":"secret"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "config":
				path := filepath.Join(profile, ".config", "kilo", "kilo.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"model":"custom/model","provider":{"custom":{"options":{"apiKey":"secret"}}}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "relative database":
				env["KILO_DB"] = "selected.db"
				db, err := sql.Open("sqlite", filepath.Join(dataDir, "selected.db"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE credential(integration_id text,value text,active integer,time_created integer); INSERT INTO credential VALUES('openai','{"type":"key","key":"secret"}',NULL,1)`); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			deps := authutil.Dependencies{GOOS: "windows", Getenv: func(name string) string { return env[name] }, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }}
			got, err := kilocodeAuthStatusFor(context.Background(), "injected-kilo", ports.AgentAuthCheck{}, deps)
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("Windows %s = %q, %v; want configured", source, got, err)
			}
			deps.GOOS = "linux"
			got, err = kilocodeAuthStatusFor(context.Background(), "injected-kilo", ports.AgentAuthCheck{}, deps)
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("non-Windows profile = %q, %v; want unknown", got, err)
			}
		})
	}
}

func kiloScopedStatus(t *testing.T, p *Plugin, in ports.AgentAuthCheck) ports.AgentAuthStatus {
	t.Helper()
	checker, ok := any(p).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Kilo does not implement scoped auth")
	}
	got, err := checker.AuthStatusFor(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func kiloAuthFixture(t *testing.T, output, dbpath string) *Plugin {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, name := range append(append([]string{}, kilocodeAPIKeyEnvVars...), "KILO_AUTH_CONTENT", "KILO_DB", "KILO_DATA_DIR", "KILO_CONFIG", "KILO_CONFIG_CONTENT", "XDG_DATA_HOME", "XDG_CONFIG_HOME") {
		t.Setenv(name, "")
	}
	if err := os.Unsetenv("KILO_AUTH_CONTENT"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	binary := filepath.Join(t.TempDir(), "kilo")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nif [ \"$1\" = db ] && [ \"$2\" = path ]; then\nprintf '%s\\n' " + quote(dbpath) + "\nelif [ \"$1\" = auth ] && [ \"$2\" = list ]; then\nprintf '%s' " + quote(output) + "\nelse\nexit 1\nfi\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Plugin{resolvedBinary: binary}
}

func writeKiloAuthFile(t *testing.T, name, content string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "kilo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
