package agentcreds

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeSettingsCommandContext(t *testing.T) {
	for _, provider := range []Provider{ProviderBedrock, ProviderVertex} {
		t.Run(string(provider), func(t *testing.T) {
			project := t.TempDir()
			writeClaudeSettingsFixture(t, filepath.Join(project, ".claude", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"configured-model","SECRET":"must-not-load"}}`)
			explicit := map[string]string{"AWS_PROFILE": "project-sso", "CLOUDSDK_CONFIG": "/project/gcloud", "LAUNCH_ONLY": "kept"}
			opts := (ResolveOptions{Env: envFrom(nil), ConfigDir: t.TempDir(), WorkingDir: project, CommandEnv: explicit}).WithClaudeSettings()
			called := false
			validator := newWithCommandRunner(nil, func(_ context.Context, invocation commandInvocation, name string, _ ...string) (commandOutput, error) {
				called = true
				wantName := "aws"
				if provider == ProviderVertex {
					wantName = "gcloud"
				}
				if name != wantName || invocation.WorkingDir != project || invocation.Env["ANTHROPIC_MODEL"] != "configured-model" || invocation.Env["AWS_PROFILE"] != "project-sso" || invocation.Env["CLOUDSDK_CONFIG"] != "/project/gcloud" || invocation.Env["LAUNCH_ONLY"] != "kept" {
					t.Error("provider command did not receive the merged launch context")
				}
				if _, ok := invocation.Env["SECRET"]; ok {
					t.Error("unapproved settings key passed to command")
				}
				return commandOutput{Stderr: []byte("fixture diagnostic")}, errors.New("fixture command unavailable")
			})
			result := validator.ValidateLocal(context.Background(), string(provider), opts)
			if !called || result.State != StateUnknown || result.Err == nil || !strings.Contains(result.Err.Error(), "fixture diagnostic") {
				t.Fatal("chain failure lost its safe unknown verdict or stderr diagnostic")
			}
			if _, mutated := explicit["ANTHROPIC_MODEL"]; mutated {
				t.Fatal("caller environment was mutated")
			}
		})
	}
}

func writeClaudeSettingsFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveClaudeSettingsPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		user, project, local string
		explicit             map[string]string
		wantKey, wantModel   string
	}{
		{name: "absent files", wantKey: "process-key", wantModel: "process-model"},
		{name: "user beats process", user: `{"env":{"ANTHROPIC_API_KEY":"user-key","ANTHROPIC_MODEL":"user-model"}}`, wantKey: "user-key", wantModel: "user-model"},
		{name: "project beats user", user: `{"env":{"ANTHROPIC_API_KEY":"user-key","ANTHROPIC_MODEL":"user-model"}}`, project: `{"env":{"ANTHROPIC_API_KEY":"project-key","ANTHROPIC_MODEL":"project-model"}}`, wantKey: "project-key", wantModel: "project-model"},
		{name: "local beats project", project: `{"env":{"ANTHROPIC_API_KEY":"project-key","ANTHROPIC_MODEL":"project-model"}}`, local: `{"env":{"ANTHROPIC_API_KEY":"local-key","ANTHROPIC_MODEL":"local-model"}}`, wantKey: "local-key", wantModel: "local-model"},
		{name: "explicit beats local", local: `{"env":{"ANTHROPIC_API_KEY":"local-key","ANTHROPIC_MODEL":"local-model"}}`, explicit: map[string]string{"ANTHROPIC_API_KEY": "explicit-key", "ANTHROPIC_MODEL": "explicit-model"}, wantKey: "explicit-key", wantModel: "explicit-model"},
		{name: "explicit empty clears inherited value", local: `{"model":"top-model","env":{"ANTHROPIC_API_KEY":"local-key","ANTHROPIC_MODEL":"local-model"}}`, explicit: map[string]string{"ANTHROPIC_API_KEY": "", "ANTHROPIC_MODEL": ""}, wantModel: "top-model"},
		{name: "malformed local does not hide user", user: `{"env":{"ANTHROPIC_API_KEY":"user-key","ANTHROPIC_MODEL":"user-model"}}`, local: `{"env":{"ANTHROPIC_API_KEY":"do-not-expose"`, wantKey: "user-key", wantModel: "user-model"},
		{name: "over one MiB is ignored even with valid JSON prefix", local: `{"env":{"ANTHROPIC_API_KEY":"oversized"}}` + strings.Repeat(" ", 1<<20), wantKey: "process-key", wantModel: "process-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			for path, body := range map[string]string{
				filepath.Join(home, ".claude", "settings.json"):          tc.user,
				filepath.Join(project, ".claude", "settings.json"):       tc.project,
				filepath.Join(project, ".claude", "settings.local.json"): tc.local,
			} {
				if body != "" {
					writeClaudeSettingsFixture(t, path, body)
				}
			}
			got := ResolveClaudeSettings(project, tc.explicit, ResolveOptions{Env: envFrom(map[string]string{
				"HOME": home, "USERPROFILE": home, "ANTHROPIC_API_KEY": "process-key", "ANTHROPIC_MODEL": "process-model",
			})})
			if got.Env["ANTHROPIC_API_KEY"] != tc.wantKey || got.Model != tc.wantModel {
				t.Fatalf("wrong resolved credential or model (model = %q, want %q)", got.Model, tc.wantModel)
			}
		})
	}
}

func TestClaudeSettingsTopLevelModelPrecedence(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	opts := ResolveOptions{Env: envFrom(map[string]string{"HOME": home, "USERPROFILE": home})}
	for _, fixture := range []struct{ path, model string }{
		{filepath.Join(home, ".claude", "settings.json"), "user-model"},
		{filepath.Join(project, ".claude", "settings.json"), "project-model"},
		{filepath.Join(project, ".claude", "settings.local.json"), "local-model"},
	} {
		writeClaudeSettingsFixture(t, fixture.path, `{"model":"`+fixture.model+`"}`)
		if got := ResolveClaudeSettings(project, nil, opts).Model; got != fixture.model {
			t.Fatalf("model = %q, want %q", got, fixture.model)
		}
	}
}

func TestClaudeSettingsConfigDirectory(t *testing.T) {
	home, configured, explicit := t.TempDir(), t.TempDir(), t.TempDir()
	writeClaudeSettingsFixture(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"home-model"}`)
	writeClaudeSettingsFixture(t, filepath.Join(configured, "settings.json"), `{"model":"configured-model"}`)
	writeClaudeSettingsFixture(t, filepath.Join(explicit, "settings.json"), `{"model":"explicit-model"}`)
	opts := ResolveOptions{Env: envFrom(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": configured})}
	if got := ResolveClaudeSettings("", nil, opts).Model; got != "configured-model" {
		t.Fatalf("model = %q", got)
	}
	if got := ResolveClaudeSettings("", map[string]string{"CLAUDE_CONFIG_DIR": explicit}, opts).Model; got != "explicit-model" {
		t.Fatalf("explicit config model = %q", got)
	}
	opts.ConfigDir = configured
	if got := ResolveClaudeSettings("", map[string]string{"CLAUDE_CONFIG_DIR": explicit}, opts).Model; got != "configured-model" {
		t.Fatalf("ResolveOptions config model = %q", got)
	}
}

func TestClaudeSettingsAndCredentialsUseTheSameLaunchHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	home, xdg := t.TempDir(), t.TempDir()
	writeClaudeSettingsFixture(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"home-model"}`)
	writeClaudeSettingsFixture(t, filepath.Join(home, ".claude", ".credentials.json"), `{"accessToken":"home-token"}`)
	writeClaudeSettingsFixture(t, filepath.Join(xdg, "claude", ".credentials.json"), `{"accessToken":"wrong-xdg-token"}`)
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			opts := ResolveOptions{GOOS: goos, Env: envFrom(nil), CommandEnv: map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": xdg}}
			settings := ResolveClaudeSettings("", opts.CommandEnv, opts)
			if settings.Model != "home-model" {
				t.Fatalf("model = %q", settings.Model)
			}
			cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, opts.WithClaudeSettings())
			if !ok || cred.Secret != "home-token" {
				t.Fatal("credential resolution did not use the same launch home as settings")
			}
		})
	}
}

func TestClaudeSettingsOnlyApprovedFileEnvironmentKeys(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{
		"ANTHROPIC_BASE_URL": "https://gateway.example", "ANTHROPIC_API_KEY": "fixture-key",
		"ANTHROPIC_AUTH_TOKEN": "fixture-token", "ANTHROPIC_MODEL": "kimi-k2",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "glm-opus", "ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-sonnet",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "glm-haiku", "ANTHROPIC_SMALL_FAST_MODEL": "small-model",
	}
	fileEnv := make(map[string]any, len(want)+4)
	for key, value := range want {
		fileEnv[key] = value
	}
	fileEnv["SECRET"] = "unrelated-secret"
	fileEnv["CLAUDE_CODE_USE_BEDROCK"] = "1"
	fileEnv["CLAUDE_CODE_OAUTH_TOKEN"] = "not-allowlisted-in-files"
	fileEnv["PATH"] = []string{"unknown non-string values must not break parsing"}
	raw, err := json.Marshal(map[string]any{"env": fileEnv, "apiKeyHelper": "must-not-run"})
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeSettingsFixture(t, filepath.Join(dir, "settings.json"), string(raw))
	got := ResolveClaudeSettings("", map[string]string{"SECRET": "explicit-secret"}, ResolveOptions{ConfigDir: dir, Env: envFrom(nil)})
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatal("resolved env did not match the exact approved settings keys")
	}
	if got.Model != "kimi-k2" {
		t.Fatalf("model = %q", got.Model)
	}
	serialized, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "fixture-") {
		t.Fatal("settings credentials are serializable")
	}
}
