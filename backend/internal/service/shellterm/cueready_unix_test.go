//go:build !windows

package shellterm

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestCueReadinessFromInteractiveUnixShell(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "sh"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s unavailable: %v", name, err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ENV", "")
			t.Setenv("ZDOTDIR", "")
			if name == "bash" {
				// Bash must not signal before a user startup hook finishes
				// reading from its terminal.
				if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("printf 'startup-blocked\\n'; read -r _\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ready, err := prepareCueShellReadiness(t.TempDir(), []string{path})
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
			if name == "bash" {
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
						t.Fatal("bash startup hook did not run")
					}
				}
			startupBlocked:
				if data, err := os.ReadFile(ready.file); err == nil && strings.TrimSpace(string(data)) == "ready" {
					t.Fatal("bash signaled before its startup hook read from stdin")
				}
				if _, err := io.WriteString(terminal, "continue\n"); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if data, err := os.ReadFile(ready.file); err == nil && strings.TrimSpace(string(data)) == "ready" {
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatalf("%s did not signal at its first prompt", name)
		})
	}
}
