package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStartProcess_WithStreams(t *testing.T) {
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "out.txt")
	errFile := filepath.Join(tmpDir, "err.txt")

	fOut, err := os.Create(outFile)
	if err != nil {
		t.Fatal(err)
	}
	defer fOut.Close()

	fErr, err := os.Create(errFile)
	if err != nil {
		t.Fatal(err)
	}
	defer fErr.Close()

	var shell, arg string
	if runtime.GOOS == "windows" {
		shell = "cmd"
		arg = "/c"
	} else {
		shell = "sh"
		arg = "-c"
	}

	cfg := processStartConfig{
		Path:   shell,
		Args:   []string{arg, "echo hello stdout && echo hello stderr 1>&2"},
		Env:    os.Environ(),
		Stdout: fOut,
		Stderr: fErr,
	}

	err = startProcess(cfg)
	if err != nil {
		t.Fatalf("startProcess failed: %v", err)
	}

	// startProcess runs asynchronously, wait a bit for it to finish
	time.Sleep(500 * time.Millisecond)

	outBytes, _ := os.ReadFile(outFile)
	if len(outBytes) == 0 {
		t.Errorf("expected stdout to be written, got empty")
	}

	errBytes, _ := os.ReadFile(errFile)
	if len(errBytes) == 0 {
		t.Errorf("expected stderr to be written, got empty")
	}
}

func TestStartProcess_NilStreams(t *testing.T) {
	var shell, arg string
	if runtime.GOOS == "windows" {
		shell = "cmd"
		arg = "/c"
	} else {
		shell = "sh"
		arg = "-c"
	}

	// Pass completely nil streams to ensure no panic or Start() errors.
	cfg := processStartConfig{
		Path:   shell,
		Args:   []string{arg, "echo test"},
		Env:    os.Environ(),
		Stdout: nil,
		Stderr: nil,
	}

	err := startProcess(cfg)
	if err != nil {
		t.Fatalf("startProcess failed with nil streams: %v", err)
	}
}
