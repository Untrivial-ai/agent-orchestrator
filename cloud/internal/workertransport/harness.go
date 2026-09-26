package workertransport

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const (
	claudeCodeVersion      = "2.1.228"
	codexVersion           = "0.147.0"
	cursorAgentVersion     = "2026.08.11-e8db854"
	maxHarnessArchiveBytes = 256 << 20
)

var cloudHarnessBinaries = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"cursor":      "cursor-agent",
}

func inspectHarnesses(ctx context.Context, input worker.HarnessInspectRequest) (worker.HarnessInspectResponse, error) {
	response := worker.HarnessInspectResponse{Harnesses: make([]worker.HarnessStatus, 0, len(input.Harnesses))}
	for _, harness := range input.Harnesses {
		if _, ok := cloudHarnessBinaries[harness]; !ok {
			return worker.HarnessInspectResponse{}, fmt.Errorf("unsupported cloud harness %q", harness)
		}
		response.Harnesses = append(response.Harnesses, inspectHarness(ctx, harness))
	}
	return response, nil
}

func inspectHarness(ctx context.Context, harness string) worker.HarnessStatus {
	binary, err := ResolveHarnessBinary(harness)
	if err != nil {
		return worker.HarnessStatus{Harness: harness, Status: "missing"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, binary, "--version").CombinedOutput()
	if err != nil {
		return worker.HarnessStatus{Harness: harness, Status: "failed", Error: boundedHarnessError(err, output)}
	}
	return worker.HarnessStatus{Harness: harness, Status: "ready", Version: strings.TrimSpace(string(output))}
}

func installHarness(ctx context.Context, input worker.HarnessInstallRequest) (worker.HarnessStatus, error) {
	if _, ok := cloudHarnessBinaries[input.Harness]; !ok {
		return worker.HarnessStatus{}, fmt.Errorf("unsupported cloud harness %q", input.Harness)
	}
	if current := inspectHarness(ctx, input.Harness); current.Status == "ready" {
		return current, nil
	}
	root, err := harnessInstallRoot()
	if err != nil {
		return worker.HarnessStatus{}, err
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		return worker.HarnessStatus{}, fmt.Errorf("create harness bin directory: %w", err)
	}
	installCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	switch input.Harness {
	case "claude-code":
		err = installNPMHarness(installCtx, root, "@anthropic-ai/claude-code@"+claudeCodeVersion)
	case "codex":
		err = installNPMHarness(installCtx, root, "@openai/codex@"+codexVersion)
	case "cursor":
		err = installCursorHarness(installCtx, root)
	}
	if err != nil {
		return worker.HarnessStatus{Harness: input.Harness, Status: "failed", Error: err.Error()}, err
	}
	status := inspectHarness(ctx, input.Harness)
	if status.Status != "ready" {
		return status, errors.New("installed harness did not pass its version check")
	}
	return status, nil
}

func installNPMHarness(ctx context.Context, root, pkg string) error {
	command := exec.CommandContext(ctx, "npm", "install", "--global", "--prefix", root, pkg)
	command.Env = append(os.Environ(), "NPM_CONFIG_UPDATE_NOTIFIER=false", "NPM_CONFIG_FUND=false", "NPM_CONFIG_AUDIT=false")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install npm harness: %s", boundedHarnessError(err, output))
	}
	return nil
}

func installCursorHarness(ctx context.Context, root string) error {
	architecture := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if architecture == "" {
		return fmt.Errorf("cursor is unsupported on architecture %s", runtime.GOARCH)
	}
	url := fmt.Sprintf("https://downloads.cursor.com/lab/%s/linux/%s/agent-cli-package.tar.gz", cursorAgentVersion, architecture)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download cursor: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download cursor: HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxHarnessArchiveBytes+1)
	gzipReader, err := gzip.NewReader(limited)
	if err != nil {
		return fmt.Errorf("open cursor archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	destination := filepath.Join(root, "bin", "cursor-agent")
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read cursor archive: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "cursor-agent" {
			continue
		}
		file, createErr := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if createErr != nil {
			return createErr
		}
		_, copyErr := io.Copy(file, io.LimitReader(tarReader, maxHarnessArchiveBytes+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		return nil
	}
	return errors.New("cursor archive did not contain cursor-agent")
}

func harnessInstallRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("worker home directory is unavailable")
	}
	return filepath.Join(home, ".local"), nil
}

// ResolveHarnessBinary finds a baked-in or session-installed cloud harness.
func ResolveHarnessBinary(harness string) (string, error) {
	binary, ok := cloudHarnessBinaries[harness]
	if !ok {
		return "", fmt.Errorf("unsupported cloud harness %q", harness)
	}
	if root, err := harnessInstallRoot(); err == nil {
		candidate := filepath.Join(root, "bin", binary)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return exec.LookPath(binary)
}

func boundedHarnessError(err error, output []byte) string {
	const max = 2048
	message := strings.TrimSpace(string(output))
	if len(message) > max {
		message = message[len(message)-max:]
	}
	if message == "" {
		return err.Error()
	}
	return message
}
