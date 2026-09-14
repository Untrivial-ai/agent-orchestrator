package agentlaunch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPinCLIProtectsCanonicalReference(t *testing.T) {
	canonical := filepath.Join(t.TempDir(), "install with spaces", "ao")
	for _, tc := range []struct {
		name, path string
		err        error
	}{
		{"canonical", canonical, nil}, {"relative refused", "ao", nil}, {"unavailable", "", errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{EnvCLI: "foreign", "PATH": "user tools"}
			if runtime.GOOS == "windows" {
				env["ao_cli"] = "foreign lower case"
			}
			err := PinCLI(env, func() (string, error) { return tc.path, tc.err })
			if tc.name == "canonical" {
				if err != nil || env[EnvCLI] != filepath.ToSlash(canonical) {
					t.Fatalf("pin = %v, %v", env, err)
				}
			} else if err == nil || env[EnvCLI] != "" {
				t.Fatalf("failed pin inherited foreign CLI: %v, %v", env, err)
			}
			if runtime.GOOS == "windows" {
				if _, ok := env["ao_cli"]; ok {
					t.Fatal("case variant survived")
				}
			}
			if env["PATH"] != "user tools" {
				t.Fatal("changed user PATH")
			}
		})
	}
}

// Real zsh startup files reproduce the reported nested tool-shell boundary.
// Nothing reads or changes the user's shell files or installed executables.
func TestCanonicalCLISurvivesNestedLoginShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows shell execution is covered separately")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	home, foreign := t.TempDir(), t.TempDir()
	canonical := filepath.Join(t.TempDir(), "AO's install", "ao")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		canonical:                        "#!/bin/sh\nprintf 'CANONICAL:%s\\n' \"$1\"\n",
		filepath.Join(foreign, "ao"):     "#!/bin/sh\necho FOREIGN\n",
		filepath.Join(home, ".zprofile"): "export PATH=\"$FOREIGN_DIR:$PATH\"\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{EnvCLI: "foreign"}
	if err := PinCLI(env, func() (string, error) { return canonical, nil }); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), zsh, "-f", "-c", `exec "$ZSH_BINARY" -l -c 'ao; "$AO_CLI" "browser snapshot"'`)
	cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "ZSH_BINARY=" + zsh, "FOREIGN_DIR=" + foreign, "PATH=" + filepath.Dir(canonical) + ":/usr/bin:/bin", EnvCLI + "=" + env[EnvCLI]}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "FOREIGN\nCANONICAL:browser snapshot" {
		t.Fatalf("nested login shell: %v: %s", err, out)
	}
}
