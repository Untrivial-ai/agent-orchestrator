package shellterm

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestCueReadinessPreservesEffectiveStartupHooks(t *testing.T) {
	for _, shell := range []string{"bash", "sh"} {
		for _, mode := range []string{"project", "inherited", "empty"} {
			t.Run(shell+"/"+mode, func(t *testing.T) {
				key := "PROMPT_COMMAND"
				if shell == "sh" {
					key = "ENV"
				}
				t.Setenv(key, "daemon-hook")
				env := map[string]string{}
				want := "daemon-hook"
				switch mode {
				case "project":
					env[key], want = "project-hook", "project-hook"
				case "empty":
					env[key], want = "", ""
				}
				name := shell
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				ready, err := prepareCueShellReadiness(t.TempDir(), []string{name}, env)
				if err != nil {
					t.Fatal(err)
				}
				defer ready.cleanup()
				got := ready.env[key]
				if shell == "sh" {
					data, err := os.ReadFile(got)
					if err != nil {
						t.Fatal(err)
					}
					got = string(data)
				}
				if want != "" && !strings.Contains(got, want) {
					t.Fatalf("startup hook = %q, missing %q", got, want)
				}
				if mode != "inherited" && strings.Contains(got, "daemon-hook") {
					t.Fatalf("startup hook used overridden daemon value: %q", got)
				}
				if env[key] != want && mode != "inherited" {
					t.Fatalf("readiness mutated input environment: %v", env)
				}
			})
		}
	}
}
