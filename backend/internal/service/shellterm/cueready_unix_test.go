//go:build !windows

package shellterm

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCueReadinessUsesEffectiveZshEnvironment(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, mode := range []string{"project", "inherited", "home-fallback"} {
		t.Run(mode, func(t *testing.T) {
			home, daemonDir := t.TempDir(), t.TempDir()
			projectDir := filepath.Join(t.TempDir(), "project's config")
			if err := os.Mkdir(projectDir, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", t.TempDir())
			t.Setenv("ZDOTDIR", daemonDir)
			env := map[string]string{"HOME": home}
			startupDir, restored := projectDir, projectDir
			switch mode {
			case "project":
				env["ZDOTDIR"] = projectDir
			case "inherited":
				startupDir, restored = daemonDir, daemonDir
			case "home-fallback":
				env["ZDOTDIR"] = ""
				startupDir, restored = home, "unset"
			}
			for name, content := range map[string]string{
				".zshenv": "typeset -g startup_order=env\n",
				".zshrc":  "startup_order+=,rc\nfunction audit_custom_function() { print -r -- AUDIT_FUNCTION_OK; }\n",
			} {
				if err := os.WriteFile(filepath.Join(startupDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ready, err := prepareCueShellReadiness(t.TempDir(), []string{zsh}, env)
			if err != nil {
				t.Fatal(err)
			}
			defer ready.cleanup()
			cmd := exec.Command(ready.argv[0], ready.argv[1:]...)
			cmd.Dir = home
			cmd.Env = append(os.Environ(), "HOME="+home)
			for key, value := range ready.env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			terminal, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = terminal.Close(); _ = cmd.Wait() }()
			go func() { _, _ = io.Copy(io.Discard, terminal) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				data, _ := os.ReadFile(ready.file)
				if string(data) == "ready" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("zsh did not reach its first prompt")
				}
				time.Sleep(25 * time.Millisecond)
			}
			ready.cleanup()
			for _, path := range []string{ready.file, ready.env["ZDOTDIR"]} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("readiness artifact remains at %s: %v", path, err)
				}
			}
			command := "{ audit_custom_function; print -r -- $startup_order; print -r -- ${ZDOTDIR-unset}; print -r -- ${precmd_functions[(Ie)_ao_cue_ready]}; } > checked\n"
			if _, err := io.WriteString(terminal, command); err != nil {
				t.Fatal(err)
			}
			want := "AUDIT_FUNCTION_OK\nenv,rc\n" + restored + "\n0\n"
			var got []byte
			for time.Now().Before(deadline) {
				got, _ = os.ReadFile(filepath.Join(home, "checked"))
				if string(got) == want {
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatalf("startup behavior = %q, want %q", got, want)
		})
	}
}

func TestCueCommandPreparesReadinessWithProjectEnvironment(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("ENV", "/daemon/profile")
	root := t.TempDir()
	profile := filepath.Join(root, "project.env")
	rt := newFakeShellRuntime()
	rt.cueReady = true
	rt.onCreate = func(cfg ports.RuntimeConfig) {
		data, err := os.ReadFile(cfg.Env["ENV"])
		if err != nil || !strings.HasPrefix(string(data), ". "+shellSingleQuote(profile)+"\n") {
			t.Errorf("readiness profile = %q, error = %v", data, err)
		}
	}
	svc := newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{
		roots: map[domain.ProjectID]string{"project": root},
		envs:  map[domain.ProjectID]map[string]string{"project": {"ENV": profile}},
	})
	svc.dataDir = t.TempDir()
	_, err := svc.RunCueCommand(context.Background(), RunCueCommandInput{ProjectID: "project", Command: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCueReadinessFromInteractiveUnixShell(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "sh", "fish"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s unavailable: %v", name, err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ENV", "")
			t.Setenv("ZDOTDIR", "")
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			if name == "fish" {
				configDir := filepath.Join(home, ".config", "fish")
				if err := os.MkdirAll(configDir, 0o700); err != nil {
					t.Fatal(err)
				}
				config := "printf 'startup-blocked\\n'; read -l answer\nset -g ao_test_config loaded\nfunction fish_prompt; printf 'custom-prompt> '; end\n"
				if err := os.WriteFile(filepath.Join(configDir, "config.fish"), []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "bash" {
				// Bash must not signal before a user startup hook finishes
				// reading from its terminal.
				if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("printf 'startup-blocked\\n'; read -r _\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ready, err := prepareCueShellReadiness(t.TempDir(), []string{path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ready.cleanup()
			cmd := exec.Command(ready.argv[0], ready.argv[1:]...)
			cmd.Dir = home
			cmd.Env = append(os.Environ(), "HOME="+home)
			for key, value := range ready.env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			terminal, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = terminal.Close(); _ = cmd.Wait() }()
			output := make(chan string, 32)
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := terminal.Read(buf)
					if n > 0 {
						select {
						case output <- string(buf[:n]):
						default:
						}
					}
					if err != nil {
						return
					}
				}
			}()
			if name == "bash" || name == "fish" {
				deadline := time.After(5 * time.Second)
				var transcript string
				for {
					select {
					case part := <-output:
						transcript += part
						if strings.Contains(transcript, "startup-blocked") {
							goto startupBlocked
						}
					case <-deadline:
						t.Fatalf("%s startup hook did not run", name)
					}
				}
			startupBlocked:
				if data, err := os.ReadFile(ready.file); err == nil && strings.TrimSpace(string(data)) == "ready" {
					t.Fatalf("%s signaled before its startup hook read from stdin", name)
				}
				if _, err := io.WriteString(terminal, "continue\n"); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if data, err := os.ReadFile(ready.file); err == nil && strings.TrimSpace(string(data)) == "ready" {
					if name == "fish" {
						// The normal config and prompt survive, and the hook is gone
						// before a cue command executes in this interactive shell.
						check := filepath.Join(home, "checked")
						command := "if test \"$ao_test_config\" = loaded; and not functions -q _ao_cue_ready; printf preserved > checked; end\n"
						if _, err := io.WriteString(terminal, command); err != nil {
							t.Fatal(err)
						}
						var transcript string
						for time.Now().Before(deadline) {
							select {
							case part := <-output:
								transcript += part
							default:
							}
							data, _ := os.ReadFile(check)
							if string(data) == "preserved" && strings.Contains(transcript, "custom-prompt>") {
								return
							}
							time.Sleep(25 * time.Millisecond)
						}
						t.Fatalf("fish config, prompt, or one-shot hook was not preserved: %s", transcript)
					}
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatalf("%s did not signal at its first prompt", name)
		})
	}
}
