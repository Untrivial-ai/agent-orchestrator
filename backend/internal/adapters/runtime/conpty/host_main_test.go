package conpty

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPTYHostIdentityRequiresToken(t *testing.T) {
	t.Setenv(runtimeHostTokenEnv, "")
	if _, err := ptyHostTokenFromEnvironment(); err == nil {
		t.Fatal("tokenless pty-host was allowed")
	}
}

func TestPTYHostIdentityReturnsAndScrubsToken(t *testing.T) {
	t.Setenv(runtimeHostTokenEnv, "token-1")
	token, err := ptyHostTokenFromEnvironment()
	if err != nil || token != "token-1" {
		t.Fatalf("ptyHostTokenFromEnvironment = token %q err=%v", token, err)
	}
	scrubPTYHostTokenEnvironment()
	if got := os.Getenv(runtimeHostTokenEnv); got != "" {
		t.Fatalf("host token leaked to child environment: %q", got)
	}
}

func TestRunHostRejectsMissingWorkingDirectory(t *testing.T) {
	t.Setenv(runtimeHostTokenEnv, "host-main-test-token")
	missing := filepath.Join(t.TempDir(), "missing")
	code := RunHost([]string{"sess-1", missing, "agent.exe"}, io.Discard)
	if code != 1 {
		t.Fatalf("RunHost code = %d, want 1", code)
	}
}
