package junie

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRuntimeFilesPrepareWritesPrivateOverlay(t *testing.T) {
	dataDir := t.TempDir()
	builder := NewRuntimeFileBuilder()
	files, err := builder.Prepare(context.Background(), RuntimeFileRequest{
		DataDir:          dataDir,
		SessionID:        "session-123",
		SystemPrompt:     "inline instructions\n\n",
		SystemPromptFile: filepath.Join(dataDir, "missing-prompt.md"),
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	sessionDir := filepath.Join(dataDir, "agent-runtime", "junie", "session-123")
	if got, want := files, (RuntimeFiles{
		ConfigPath:     filepath.Join(sessionDir, "config.json"),
		GuidelinesPath: filepath.Join(sessionDir, "guidelines.md"),
	}); got != want {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
	assertRuntimeFileContent(t, files.GuidelinesPath, "inline instructions\n")
	assertRuntimeConfig(t, files.ConfigPath)

	for _, dir := range []string{
		filepath.Join(dataDir, "agent-runtime"),
		filepath.Join(dataDir, "agent-runtime", "junie"),
		sessionDir,
	} {
		assertRuntimePathMode(t, dir, 0o700)
	}
	assertRuntimePathMode(t, files.ConfigPath, 0o600)
	assertRuntimePathMode(t, files.GuidelinesPath, 0o600)
}

func TestRuntimeFilesPrepareUsesFileInstructionsAndOmitsEmptyGuidelines(t *testing.T) {
	t.Run("file instructions", func(t *testing.T) {
		dataDir := t.TempDir()
		promptPath := filepath.Join(t.TempDir(), "standing.md")
		if err := os.WriteFile(promptPath, []byte("from file\n\n"), 0o600); err != nil {
			t.Fatalf("write prompt: %v", err)
		}

		files, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
			DataDir:          dataDir,
			SessionID:        "from-file",
			SystemPromptFile: promptPath,
		})
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		assertRuntimeFileContent(t, files.GuidelinesPath, "from file\n")
	})

	t.Run("absent instructions", func(t *testing.T) {
		dataDir := t.TempDir()
		files, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
			DataDir:   dataDir,
			SessionID: "empty",
		})
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if files.GuidelinesPath != "" {
			t.Fatalf("GuidelinesPath = %q, want empty", files.GuidelinesPath)
		}
		guidelinesPath := filepath.Join(dataDir, "agent-runtime", "junie", "empty", "guidelines.md")
		if _, statErr := os.Lstat(guidelinesPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("empty instructions created guidelines file: %v", statErr)
		}
		assertRuntimeConfig(t, files.ConfigPath)
	})

	t.Run("whitespace-only inline instructions win over file", func(t *testing.T) {
		dataDir := t.TempDir()
		files, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
			DataDir:          dataDir,
			SessionID:        "inline-whitespace",
			SystemPrompt:     " \t\n\n",
			SystemPromptFile: filepath.Join(dataDir, "missing-prompt.md"),
		})
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if files.GuidelinesPath != "" {
			t.Fatalf("GuidelinesPath = %q, want empty", files.GuidelinesPath)
		}
		guidelinesPath := filepath.Join(dataDir, "agent-runtime", "junie", "inline-whitespace", "guidelines.md")
		if _, statErr := os.Lstat(guidelinesPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("whitespace instructions created guidelines file: %v", statErr)
		}
	})

	t.Run("whitespace-only file instructions", func(t *testing.T) {
		dataDir := t.TempDir()
		promptPath := filepath.Join(t.TempDir(), "standing.md")
		if err := os.WriteFile(promptPath, []byte(" \t\n\n"), 0o600); err != nil {
			t.Fatalf("write prompt: %v", err)
		}
		files, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
			DataDir:          dataDir,
			SessionID:        "file-whitespace",
			SystemPromptFile: promptPath,
		})
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if files.GuidelinesPath != "" {
			t.Fatalf("GuidelinesPath = %q, want empty", files.GuidelinesPath)
		}
		guidelinesPath := filepath.Join(dataDir, "agent-runtime", "junie", "file-whitespace", "guidelines.md")
		if _, statErr := os.Lstat(guidelinesPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("whitespace file created guidelines file: %v", statErr)
		}
	})

	t.Run("previous guidelines remain but are not returned", func(t *testing.T) {
		dataDir := t.TempDir()
		builder := NewRuntimeFileBuilder()
		first, err := builder.Prepare(context.Background(), RuntimeFileRequest{
			DataDir: dataDir, SessionID: "previous", SystemPrompt: "keep on disk",
		})
		if err != nil {
			t.Fatalf("first Prepare: %v", err)
		}
		second, err := builder.Prepare(context.Background(), RuntimeFileRequest{
			DataDir: dataDir, SessionID: "previous",
		})
		if err != nil {
			t.Fatalf("second Prepare: %v", err)
		}
		if second.GuidelinesPath != "" {
			t.Fatalf("GuidelinesPath = %q, want empty", second.GuidelinesPath)
		}
		assertRuntimeFileContent(t, first.GuidelinesPath, "keep on disk\n")
	})
}

func TestRuntimeFilesPrepareRejectsUnsafeInputs(t *testing.T) {
	for _, sessionID := range []string{"", " ", ".", "..", "../escape", "nested/session", `nested\session`} {
		t.Run(sessionID, func(t *testing.T) {
			dataDir := t.TempDir()
			_, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
				DataDir:   dataDir,
				SessionID: sessionID,
			})
			if err == nil {
				t.Fatal("Prepare succeeded with unsafe session ID")
			}
			if _, statErr := os.Stat(filepath.Join(dataDir, "agent-runtime")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsafe input created runtime tree: %v", statErr)
			}
		})
	}

	_, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{SessionID: "valid"})
	if err == nil {
		t.Fatal("Prepare succeeded without an AO data directory")
	}
}

func TestRuntimeFilesPrepareRejectsRelativeDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	relativeDataDir := filepath.Join("relative", "ao-data")

	_, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
		DataDir: relativeDataDir, SessionID: "valid",
	})
	if err == nil {
		t.Fatal("Prepare succeeded with a relative AO data directory")
	}
	if _, statErr := os.Lstat(relativeDataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("relative data directory was created: %v", statErr)
	}
}

func TestRuntimeFilesPrepareRejectsSymlinkRedirects(t *testing.T) {
	for _, linkAt := range []string{"junie", "session", "config", "guidelines"} {
		t.Run(linkAt, func(t *testing.T) {
			dataDir := t.TempDir()
			outside := t.TempDir()
			outsideSentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(outsideSentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}

			runtimeRoot := filepath.Join(dataDir, "agent-runtime")
			junieRoot := filepath.Join(runtimeRoot, "junie")
			sessionDir := filepath.Join(junieRoot, "session")
			switch linkAt {
			case "junie":
				mustRuntimeMkdirAll(t, runtimeRoot)
				mustRuntimeSymlink(t, outside, junieRoot)
			case "session":
				mustRuntimeMkdirAll(t, junieRoot)
				mustRuntimeSymlink(t, outside, sessionDir)
			case "config":
				mustRuntimeMkdirAll(t, sessionDir)
				mustRuntimeSymlink(t, outsideSentinel, filepath.Join(sessionDir, "config.json"))
			case "guidelines":
				mustRuntimeMkdirAll(t, sessionDir)
				mustRuntimeSymlink(t, outsideSentinel, filepath.Join(sessionDir, "guidelines.md"))
			}

			_, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{
				DataDir:      dataDir,
				SessionID:    "session",
				SystemPrompt: "new content",
			})
			if err == nil {
				t.Fatal("Prepare followed an unsafe symlink")
			}
			assertRuntimeFileContent(t, outsideSentinel, "untouched")
		})
	}
}

func TestRuntimeFilesPrepareIsIdempotentAndDoesNotClobberOtherFiles(t *testing.T) {
	dataDir := t.TempDir()
	builder := NewRuntimeFileBuilder()
	request := RuntimeFileRequest{DataDir: dataDir, SessionID: "repeat", SystemPrompt: "first"}
	first, err := builder.Prepare(context.Background(), request)
	if err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	unrelatedPath := filepath.Join(filepath.Dir(first.ConfigPath), "owned-by-someone-else")
	if err := os.WriteFile(unrelatedPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	request.SystemPrompt = "second\n\n"
	second, err := builder.Prepare(context.Background(), request)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if second != first {
		t.Fatalf("paths changed: first %#v, second %#v", first, second)
	}
	assertRuntimeFileContent(t, second.GuidelinesPath, "second\n")
	assertRuntimeFileContent(t, unrelatedPath, "keep me")

	entries, err := os.ReadDir(filepath.Dir(second.ConfigPath))
	if err != nil {
		t.Fatalf("read runtime directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ao-tmp-") {
			t.Fatalf("atomic write left temporary file %q", entry.Name())
		}
	}
}

func TestRuntimeFilesPrepareConcurrentCallsExposeOnlyCompleteFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows readers do not share delete access; native replacement concurrency needs separate platform conformance")
	}
	dataDir := t.TempDir()
	builder := NewRuntimeFileBuilder()
	initial, err := builder.Prepare(context.Background(), RuntimeFileRequest{
		DataDir: dataDir, SessionID: "concurrent", SystemPrompt: "initial",
	})
	if err != nil {
		t.Fatalf("initial Prepare: %v", err)
	}

	prompts := make([]string, 8)
	validGuidelines := map[string]bool{"initial\n": true}
	for i := range prompts {
		prompts[i] = strings.Repeat(string(rune('a'+i)), 128*1024)
		validGuidelines[prompts[i]+"\n"] = true
	}

	errCh := make(chan error, len(prompts))
	var writers sync.WaitGroup
	for _, prompt := range prompts {
		prompt := prompt
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 6 {
				_, prepareErr := builder.Prepare(context.Background(), RuntimeFileRequest{
					DataDir: dataDir, SessionID: "concurrent", SystemPrompt: prompt,
				})
				if prepareErr != nil {
					errCh <- prepareErr
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		writers.Wait()
		close(done)
	}()

	for {
		config, readErr := os.ReadFile(initial.ConfigPath)
		if readErr != nil {
			t.Fatalf("read config during concurrent writes: %v", readErr)
		}
		var decoded map[string]any
		if jsonErr := json.Unmarshal(config, &decoded); jsonErr != nil {
			t.Fatalf("observed partial config: %v", jsonErr)
		}

		guidelines, readErr := os.ReadFile(initial.GuidelinesPath)
		if readErr != nil {
			t.Fatalf("read guidelines during concurrent writes: %v", readErr)
		}
		if !validGuidelines[string(guidelines)] {
			t.Fatalf("observed partial guidelines: length=%d", len(guidelines))
		}

		select {
		case <-done:
			close(errCh)
			for prepareErr := range errCh {
				t.Errorf("concurrent Prepare: %v", prepareErr)
			}
			return
		default:
		}
	}
}

func TestRuntimeFilesPrepareHonorsCanceledContext(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewRuntimeFileBuilder().Prepare(ctx, RuntimeFileRequest{
		DataDir: dataDir, SessionID: "canceled", SystemPrompt: "ignored",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "agent-runtime")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled prepare created runtime tree: %v", statErr)
	}
}

func assertRuntimeConfig(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("config is invalid JSON: %q", data)
	}

	const expected = `{
  "hooks": {
    "SessionStart": [{"matcher":"startup|resume|clear","hooks":[{"type":"command","command":"ao hooks junie session-start","timeout":10}]}],
    "UserPromptSubmit": [{"hooks":[{"type":"command","command":"ao hooks junie user-prompt-submit","timeout":10}]}],
    "PreToolUse": [{"matcher":".*","hooks":[{"type":"command","command":"ao hooks junie pre-tool-use","timeout":10}]}],
    "Stop": [{"hooks":[{"type":"command","command":"ao hooks junie stop","timeout":10}]}],
    "StopFailure": [{"matcher":".*","hooks":[{"type":"command","command":"ao hooks junie stop-failure","timeout":10}]}],
    "SessionEnd": [{"matcher":"prompt_input_exit|logout|other","hooks":[{"type":"command","command":"ao hooks junie session-end","timeout":10}]}]
  }
}`
	var got, want map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatalf("decode test fixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
	if _, ok := got["hooks"].(map[string]any)["PermissionRequest"]; ok {
		t.Fatal("config enabled unproven PermissionRequest hook")
	}
}

func assertRuntimePathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	// Windows does not expose POSIX mode bits. Native ACL verification remains
	// part of the platform admission gate; the path/content tests still run.
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is a symlink", path)
	}
}

func assertRuntimeFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("content %s = %q, want %q", path, got, want)
	}
}

func mustRuntimeMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustRuntimeSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink test requires Windows symlink privileges: %v", err)
		}
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}
