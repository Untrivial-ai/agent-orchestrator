package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// selfUpdateAttemptEnv counts re-execs so a persistent hash mismatch cannot
	// spin forever. A freshly healed binary matches by construction, so one
	// re-exec is the normal ceiling; the second is slack for a torn download.
	selfUpdateAttemptEnv = "AO_WORKER_SELF_UPDATE_ATTEMPT"
	maxSelfUpdateReexec  = 2
	// A worker binary is a few megabytes; cap the download generously so a
	// misconfigured endpoint cannot stream unbounded data into the sandbox.
	maxWorkerBinaryBytes = 128 << 20
	defaultHelperPath    = "/usr/local/bin/ao"
)

// selfUpdateIfStale reconciles this worker (and the ao helper) against the exact
// binary hashes the control plane advertised in the environment. A baked
// template can therefore lag the control plane without forcing the reconciler to
// upload multi-megabyte binaries on every provision: the worker heals itself
// only when it is genuinely stale.
//
// Sandbox images bake the binaries at /usr/local/bin owned by root, which the
// unprivileged worker user cannot overwrite. So healing writes into dataDir/bin
// — a worker-owned directory already on PATH (mirrors worker.ToolingBinDir) —
// and the worker re-execs the healed copy from there. The healed copy persists
// on the sandbox filesystem, so a later restart on the same box reuses it
// without downloading again. When no expected hash is advertised (an older
// control plane, or a provider that does not bake), it is a no-op.
func selfUpdateIfStale(ctx context.Context, logger *slog.Logger, publicURL, dataDir string) error {
	expected := strings.ToLower(strings.TrimSpace(os.Getenv("AO_WORKER_EXPECTED_SHA256")))
	if expected == "" {
		return nil
	}
	binaryBase := strings.TrimRight(publicURL, "/") + "/api/cloud/v1/worker/binary/"
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create worker tooling bin directory: %w", err)
	}

	// Helper: heal into binDir/ao, which shadows the baked /usr/local/bin/ao on
	// PATH. The agent invokes ao fresh each time, so no re-exec is needed.
	if helperExpected := strings.ToLower(strings.TrimSpace(os.Getenv("AO_WORKER_HELPER_EXPECTED_SHA256"))); helperExpected != "" {
		if err := healHelper(ctx, httpClient, logger, binaryBase, helperExpected, binDir); err != nil {
			// A stale helper degrades an orchestrator's ao tooling but does not
			// stop the session, so warn and continue rather than fail the worker.
			logger.Warn("heal ao helper binary", "error", err)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve worker executable: %w", err)
	}
	selfStale, err := fileHashDiffers(self, expected)
	if err != nil {
		return fmt.Errorf("hash worker executable: %w", err)
	}
	if !selfStale {
		return nil
	}

	attempt := parseAttempt(os.Getenv(selfUpdateAttemptEnv))
	if attempt >= maxSelfUpdateReexec {
		logger.Warn("worker still stale after self-update; continuing with the current binary",
			"expected_sha256", expected, "attempts", attempt)
		return nil
	}

	override := filepath.Join(binDir, "ao-worker")
	overrideStale, err := fileHashDiffers(override, expected)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hash healed worker binary: %w", err)
	}
	if err != nil || overrideStale {
		logger.Info("worker binary is stale; healing from the control plane",
			"expected_sha256", expected, "override", override)
		if err := downloadVerifyReplace(ctx, httpClient, binaryBase+expected, expected, override); err != nil {
			return fmt.Errorf("heal worker binary: %w", err)
		}
	}

	env := append(os.Environ(), fmt.Sprintf("%s=%d", selfUpdateAttemptEnv, attempt+1))
	logger.Info("re-executing the healed worker binary", "path", override)
	if err := syscall.Exec(override, os.Args, env); err != nil {
		return fmt.Errorf("re-exec healed worker: %w", err)
	}
	return nil // syscall.Exec replaces the process image on success.
}

// healHelper ensures a correct ao helper is on PATH ahead of the baked copy. It
// downloads only when neither the previously healed copy nor the baked copy
// already matches the expected hash.
func healHelper(
	ctx context.Context,
	httpClient *http.Client,
	logger *slog.Logger,
	binaryBase, expectedHex, binDir string,
) error {
	override := filepath.Join(binDir, "ao")
	if stale, err := fileHashDiffers(override, expectedHex); err == nil && !stale {
		return nil // already healed on a prior boot
	}
	baked := strings.TrimSpace(os.Getenv("AO_WORKER_HELPER_PATH"))
	if baked == "" {
		baked = defaultHelperPath
	}
	if stale, err := fileHashDiffers(baked, expectedHex); err == nil && !stale {
		return nil // the baked helper is already correct; nothing to shadow
	}
	logger.Info("healing ao helper from the control plane", "override", override, "expected_sha256", expectedHex)
	return downloadVerifyReplace(ctx, httpClient, binaryBase+expectedHex, expectedHex, override)
}

// downloadVerifyReplace fetches a content-addressed binary, verifies its hash,
// and atomically renames it over dest. dest lives in a worker-owned directory,
// so the rename (which needs a writable parent) succeeds; renaming also works
// while a previous copy at dest is executing, since the running process keeps
// the old inode.
func downloadVerifyReplace(
	ctx context.Context,
	httpClient *http.Client,
	url, expectedHex, dest string,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("fetch %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkerBinaryBytes))
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, expectedHex) {
		return fmt.Errorf("downloaded binary hash mismatch: got %s, want %s", got, expectedHex)
	}

	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".ao-update-*")
	if err != nil {
		return fmt.Errorf("stage binary in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write staged binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged binary: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	return nil
}

func fileHashDiffers(path, expectedHex string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedHex), nil
}

func parseAttempt(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
