package termtheme

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRejectsMissingAndJunk(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Read(""); ok {
		t.Fatal("empty data dir must not report a scheme")
	}
	if _, ok := Read(dir); ok {
		t.Fatal("missing file must not report a scheme")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("system\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(dir); ok {
		t.Fatal("unrecognized scheme must not report a scheme")
	}
}

func TestApplySetsHintsFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("light\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"KEEP": "yes"}
	Apply(env, dir)
	if env[EnvTheme] != "light" {
		t.Fatalf("TERM_THEME = %q, want light", env[EnvTheme])
	}
	if env[EnvColorFgBg] != "0;15" {
		t.Fatalf("COLORFGBG = %q, want 0;15", env[EnvColorFgBg])
	}
	if env["KEEP"] != "yes" {
		t.Fatalf("KEEP = %q, want yes", env["KEEP"])
	}
}

func TestApplyPreservesExplicitOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("light\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{EnvTheme: "dark", EnvColorFgBg: "15;0"}
	Apply(env, dir)
	if env[EnvTheme] != "dark" {
		t.Fatalf("TERM_THEME = %q, want caller dark", env[EnvTheme])
	}
	if env[EnvColorFgBg] != "15;0" {
		t.Fatalf("COLORFGBG = %q, want caller 15;0", env[EnvColorFgBg])
	}
}

func TestApplyDarkHint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("DARK"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	Apply(env, dir)
	if env[EnvTheme] != "dark" || env[EnvColorFgBg] != "15;0" {
		t.Fatalf("env = %#v, want dark / 15;0", env)
	}
}

// Windows folds env key case: a project's lowercase override must win, and no
// uppercase duplicate may be added beside it for ConPTY to dedupe in map order.
func TestApplyFoldingKeysKeepsCaseVariantOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("light\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"term_theme": "dark", "ColorFgBg": "15;0"}
	ApplyFoldingKeys(env, dir, true)
	if len(env) != 2 || env["term_theme"] != "dark" || env["ColorFgBg"] != "15;0" {
		t.Fatalf("env = %v, want only the project's case variants", env)
	}

	// A blank variant is not an override: it is replaced by the canonical key.
	env = map[string]string{"term_theme": " "}
	ApplyFoldingKeys(env, dir, true)
	if _, stale := env["term_theme"]; stale || env[EnvTheme] != "light" {
		t.Fatalf("env = %v, want blank variant replaced by TERM_THEME=light", env)
	}

	// Without folding (Unix), case variants are distinct keys.
	env = map[string]string{"term_theme": "dark"}
	ApplyFoldingKeys(env, dir, false)
	if env[EnvTheme] != "light" || env["term_theme"] != "dark" {
		t.Fatalf("env = %v, want both keys on case-sensitive platforms", env)
	}
}
