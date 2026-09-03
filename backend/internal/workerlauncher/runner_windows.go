//go:build windows

package workerlauncher

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
	"golang.org/x/sys/windows"
)

var hostReadyPattern = regexp.MustCompile(`^READY:(\d+) (\d+)$`)

func ReadAndValidateManifest(path string, cfg workeridentity.Config) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("worker launcher: read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("worker launcher: decode manifest: %w", err)
	}
	if err := manifest.Validate(ValidationConfig{WorkspaceRoot: cfg.WorkspaceRoot, SessionProfileRoot: cfg.SessionProfileRoot,
		Executables: cfg.Executables, AllowedEnvironment: AllowedEnvironmentKeys(), Now: time.Now().UTC()}); err != nil {
		return Manifest{}, err
	}
	if err := validateAOAgentCommand(manifest, cfg); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func RunWorker(configPath, manifestPath, readyAddress, readyNonce, logonSID, profileRoot string) error {
	cfg, err := workeridentity.LoadConfig(configPath)
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, err)
	}
	manifest, err := ReadAndValidateManifest(manifestPath, cfg)
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, err)
	}
	if err := os.Remove(manifestPath); err != nil {
		return reportWorkerError(readyAddress, readyNonce, fmt.Errorf("worker launcher: consume manifest: %w", err))
	}
	current, err := currentTokenDetails()
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, err)
	}
	if current.SID != cfg.AccountSID {
		return reportWorkerError(readyAddress, readyNonce, errors.New("worker launcher: running under unexpected Windows identity"))
	}
	actualProfileRoot, err := userProfileDirectory(windows.GetCurrentProcessToken())
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, fmt.Errorf("worker launcher: resolve current Windows profile root: %w", err))
	}
	if strings.TrimSpace(profileRoot) == "" || !samePath(profileRoot, actualProfileRoot) {
		return reportWorkerError(readyAddress, readyNonce, errors.New("worker launcher: invalid Windows profile root"))
	}
	parsedLogonSID, err := windows.StringToSid(logonSID)
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, errors.New("worker launcher: invalid Logon SID"))
	}
	containsLogonSID, err := tokenContainsEnabledSID(windows.GetCurrentProcessToken(), parsedLogonSID)
	if err != nil || !containsLogonSID {
		return reportWorkerError(readyAddress, readyNonce, errors.New("worker launcher: Logon SID is not present in worker token"))
	}
	job, err := createSessionJob()
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, err)
	}
	defer job.Close()
	details := current
	if details.Admin || details.Elevated {
		return reportWorkerError(readyAddress, readyNonce, errors.New("worker launcher: worker token must be non-administrator and non-elevated"))
	}
	childExecutable := cfg.Executables[manifest.ExecutableID]
	if err := verifyExecutableIntegrity(childExecutable, cfg.ExecutableSHA256[manifest.ExecutableID]); err != nil {
		return reportWorkerError(readyAddress, readyNonce, err)
	}
	args := []string{"pty-host", manifest.SessionID, manifest.Cwd, childExecutable}
	args = append(args, manifest.Argv...)
	cmd := exec.Command(cfg.PTYHostPath, args...)
	cmd.Dir = manifest.Cwd
	cmd.Env = buildWorkerEnvironment(manifest, cfg, profileRoot, environmentValues(os.Environ()))
	cmd.SysProcAttr = workerSysProcAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return reportWorkerError(readyAddress, readyNonce, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return reportWorkerError(readyAddress, readyNonce, fmt.Errorf("worker launcher: start pty host: %w", err))
	}
	readyLine := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			readyLine <- strings.TrimSpace(scanner.Text())
		} else {
			readyLine <- ""
		}
	}()
	select {
	case line := <-readyLine:
		match := hostReadyPattern.FindStringSubmatch(line)
		if match == nil {
			waitErr := cmd.Wait()
			diagnostic := strings.TrimSpace(stderr.String())
			if len(diagnostic) > 2048 {
				diagnostic = diagnostic[:2048]
			}
			return reportWorkerError(readyAddress, readyNonce, fmt.Errorf("worker launcher: pty host did not report READY (exit %v): %s", waitErr, diagnostic))
		}
		ptyHostPID, _ := strconv.Atoi(match[1])
		port, _ := strconv.Atoi(match[2])
		if err := sendReady(readyAddress, ReadyMessage{Nonce: readyNonce, Address: "127.0.0.1:" + strconv.Itoa(port), LauncherPID: ptyHostPID, Token: details}); err != nil {
			_ = cmd.Process.Kill()
			return err
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return reportWorkerError(readyAddress, readyNonce, errors.New("worker launcher: pty host startup timeout"))
	}
	return cmd.Wait()
}

func verifyExecutableIntegrity(path, expected string) error {
	if expected == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("worker launcher: open trusted executable: %w", err)
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("worker launcher: hash trusted executable: %w", err)
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return errors.New("worker launcher: trusted executable integrity check failed")
	}
	return nil
}

func reportWorkerError(address, nonce string, err error) error {
	_ = sendReady(address, ReadyMessage{Nonce: nonce, Error: err.Error()})
	return err
}

func sendReady(address string, message ReadyMessage) error {
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	return json.NewEncoder(conn).Encode(message)
}

func validateAOAgentCommand(m Manifest, cfg workeridentity.Config) error {
	switch m.ExecutableID {
	case ExecutableClaude, ExecutableCodex, ExecutableShell:
		return nil
	case ExecutableAOAgent:
		if len(m.Argv) < 5 || m.Argv[0] != "agent-process" || m.Argv[1] != "supervise" {
			return errors.New("worker launcher: ao-agent must use the trusted supervisor")
		}
		separator := -1
		for i, arg := range m.Argv {
			if arg == "--" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+1 >= len(m.Argv) {
			return errors.New("worker launcher: ao-agent missing supervised executable")
		}
		actual, err := CanonicalExecutable(m.Argv[separator+1])
		if err != nil {
			return err
		}
		for _, id := range []string{ExecutableClaude, ExecutableCodex} {
			if allowed := cfg.Executables[id]; allowed != "" && samePath(actual, allowed) {
				return nil
			}
		}
		return errors.New("worker launcher: supervised executable is not Claude or Codex")
	default:
		return errors.New("worker launcher: unsupported executable_id")
	}
}

func AllowedEnvironmentKeys() map[string]struct{} {
	keys := []string{"AO_SESSION_ID", "AO_PROJECT_ID", "AO_RUNTIME_LAUNCH_ID", "AO_PORT", "AO_RUN_FILE", "AO_DAEMON_URL", "TERM", "COLORTERM", "NO_COLOR", "LANG", "LC_ALL", "CLAUDE_CONFIG_DIR", "CODEX_HOME"}
	keys = append(keys, providerEnvironmentKeys(ExecutableClaude)...)
	out := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}
	return out
}
