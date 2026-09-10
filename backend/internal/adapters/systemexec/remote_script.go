package systemexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	remoteScriptDownloadTimeout = 30 * time.Second
	remoteScriptMaxBytes        = 4 << 20
	remoteScriptMaxRedirects    = 5
)

// RunInstallScript downloads a fixed HTTPS installer into private AO-owned
// storage, executes the complete file, and removes it afterward.
func (a Adapter) RunInstallScript(ctx context.Context, command ports.InstallScriptCommand, stdout, stderr io.Writer) (result ports.InstallScriptResult, err error) {
	if a.installerRoot == "" {
		return result, errors.New("installer scratch directory is not configured")
	}
	if len(command.Interpreter) == 0 {
		return result, errors.New("installer interpreter is empty")
	}
	parsed, err := url.Parse(command.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return result, fmt.Errorf("installer URL must be absolute HTTPS: %q", command.URL)
	}

	downloadCtx, cancelDownload := context.WithTimeout(ctx, remoteScriptDownloadTimeout)
	defer cancelDownload()
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, command.URL, http.NoBody)
	if err != nil {
		return result, fmt.Errorf("create installer request: %w", err)
	}
	client := *a.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > remoteScriptMaxRedirects {
			return errors.New("installer download exceeded redirect limit")
		}
		if req.URL.Scheme != "https" {
			return errors.New("installer redirect must remain HTTPS")
		}
		return nil
	}
	response, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("download installer: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, errors.Join(fmt.Errorf("download installer: HTTP %s", response.Status), response.Body.Close())
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, remoteScriptMaxBytes+1))
	closeErr := response.Body.Close()
	if err != nil {
		return result, errors.Join(fmt.Errorf("read installer: %w", err), closeErr)
	}
	if closeErr != nil {
		return result, fmt.Errorf("close installer response: %w", closeErr)
	}
	if len(body) > remoteScriptMaxBytes {
		return result, fmt.Errorf("installer exceeds %d-byte download limit", remoteScriptMaxBytes)
	}
	sum := sha256.Sum256(body)
	result.SHA256 = hex.EncodeToString(sum[:])
	cancelDownload()

	if err := os.MkdirAll(a.installerRoot, 0o700); err != nil {
		return result, fmt.Errorf("create installer scratch directory: %w", err)
	}
	if err := os.Chmod(a.installerRoot, 0o700); err != nil { //nolint:gosec // Private directories need owner traversal.
		return result, fmt.Errorf("secure installer scratch directory: %w", err)
	}
	jobDir, err := os.MkdirTemp(a.installerRoot, "job-*")
	if err != nil {
		return result, fmt.Errorf("create installer job directory: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(jobDir))
	}()
	if err := os.Chmod(jobDir, 0o700); err != nil { //nolint:gosec // Private directories need owner traversal.
		return result, fmt.Errorf("secure installer job directory: %w", err)
	}
	extension := ".sh"
	program := strings.ToLower(filepath.Base(command.Interpreter[0]))
	if strings.Contains(program, "powershell") || strings.Contains(program, "pwsh") {
		extension = ".ps1"
	}
	scriptPath := filepath.Join(jobDir, "installer"+extension)
	if err := os.WriteFile(scriptPath, body, 0o600); err != nil {
		return result, fmt.Errorf("write installer script: %w", err)
	}
	if err := os.Chmod(scriptPath, 0o600); err != nil {
		return result, fmt.Errorf("secure installer script: %w", err)
	}

	argv := append([]string(nil), command.Interpreter...)
	argv = append(argv, scriptPath)
	err = a.RunInstall(ctx, ports.InstallCommand{Argv: argv, Env: command.Env}, stdout, stderr)
	return result, err
}
