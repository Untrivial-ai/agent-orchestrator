//go:build windows

package shellterm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
)

func TestCueReadinessFromInteractiveWindowsShell(t *testing.T) {
	for _, choice := range []string{"powershell", "pwsh", "cmd", "git-bash"} {
		t.Run(choice, func(t *testing.T) {
			argv, fallback := resolveWindowsShell(choice)
			if fallback || len(argv) == 0 {
				t.Skip("shell unavailable")
			}
			ready, err := prepareCueShellReadiness(t.TempDir(), argv)
			if err != nil {
				t.Fatal(err)
			}
			defer ready.cleanup()
			p, err := gopty.New()
			if err != nil {
				t.Fatal(err)
			}
			cp := p.(gopty.ConPty)
			defer cp.Close()
			if err := cp.Resize(120, 40); err != nil {
				t.Fatal(err)
			}
			cmd := cp.Command(ready.argv[0], ready.argv[1:]...)
			cmd.Dir = t.TempDir()
			cmd.Env = append([]string{}, os.Environ()...)
			for key, value := range ready.env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			output := make(chan string, 64)
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := cp.Read(buf)
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
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				if data, err := os.ReadFile(ready.file); err == nil && strings.TrimSpace(string(data)) == "ready" {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			var transcript string
			for {
				select {
				case part := <-output:
					transcript += part
				default:
					t.Fatalf("%s did not signal readiness at %s: %q", choice, filepath.Base(ready.file), transcript)
				}
			}
		})
	}
}
