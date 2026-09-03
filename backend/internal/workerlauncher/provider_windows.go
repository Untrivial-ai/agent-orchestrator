//go:build windows

package workerlauncher

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
)

const (
	providerTerminalInput byte = 0x02
	providerGetOutputReq  byte = 0x04
	providerGetOutputRes  byte = 0x05
	providerStatusReq     byte = 0x06
	providerStatusRes     byte = 0x07
	providerKillReq       byte = 0x08
	providerKillAck       byte = 0x0b
)

type providerStatus struct {
	Alive    bool `json:"alive"`
	ExitCode *int `json:"exitCode,omitempty"`
}

// ProviderCommandOptions contains non-secret inputs for a one-shot Provider
// execution. Provider credentials are accepted only through the caller's
// selected environment and never enter argv or the manifest.
type ProviderCommandOptions struct {
	SessionID string
	Prompt    string
}

// RunProviderCommand runs the official Provider login/status command under
// AOAgentWorker. Provider credentials are written by the Provider itself to
// the loaded AOAgentWorker Windows profile; no host credentials are copied.
func RunProviderCommand(configPath, worktree, action, provider string, opts ProviderCommandOptions, stdin io.Reader, stdout io.Writer) error {
	cfg, err := workeridentity.LoadConfig(configPath)
	if err != nil {
		return err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	action = strings.ToLower(strings.TrimSpace(action))
	if provider != ExecutableClaude && provider != ExecutableCodex {
		return errors.New("worker provider: provider must be claude or codex")
	}
	if action != "status" && action != "login" && action != "run" && action != "resume" {
		return errors.New("worker provider: action must be status, login, run, or resume")
	}
	if (action == "run" || action == "resume") && provider != ExecutableClaude {
		return errors.New("worker provider: run and resume currently require claude")
	}
	if action == "run" || action == "resume" {
		if !identifierPattern.MatchString(strings.TrimSpace(opts.SessionID)) {
			return errors.New("worker provider: a valid session ID is required")
		}
		if strings.TrimSpace(opts.Prompt) == "" {
			return errors.New("worker provider: prompt is required")
		}
	}
	if cfg.Executables[provider] == "" {
		return fmt.Errorf("worker provider: %s executable is not installed", provider)
	}
	canonicalWorktree, err := CanonicalContainedDirectory(worktree, cfg.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("worker provider: worktree: %w", err)
	}
	sessionID := fmt.Sprintf("provider-%s-%d", provider, time.Now().UTC().UnixNano())
	sessionProfile := filepath.Join(cfg.SessionProfileRoot, sessionID)
	if err := os.MkdirAll(filepath.Join(sessionProfile, "Temp"), 0o700); err != nil {
		return err
	}
	if err := workeridentity.SecureHostTree(sessionProfile); err != nil {
		return err
	}
	if err := workeridentity.SecureHostTree(canonicalWorktree); err != nil {
		return err
	}
	argv := providerArguments(action, provider, opts)
	allowedEnvironmentKeys := []string(nil)
	if action == "run" || action == "resume" {
		allowedEnvironmentKeys = providerEnvironmentKeys(provider)
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	now := time.Now().UTC()
	manifest := Manifest{Version: ManifestVersion, SessionID: sessionID, ProjectID: "worker-provider",
		CanonicalWorktree: canonicalWorktree, WorkerProfileRoot: sessionProfile, ExecutableID: provider,
		Argv: argv, Cwd: canonicalWorktree, AllowedEnvironmentKeys: allowedEnvironmentKeys, TerminalRows: 40, TerminalCols: 160,
		CreatedAt: now, ExpiresAt: now.Add(time.Minute), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
	if err := manifest.Validate(ValidationConfig{WorkspaceRoot: cfg.WorkspaceRoot, SessionProfileRoot: cfg.SessionProfileRoot,
		Executables: cfg.Executables, AllowedEnvironment: AllowedEnvironmentKeys(), Now: now}); err != nil {
		return err
	}
	manifestPath := filepath.Join(sessionProfile, "provider-manifest.json")
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		return err
	}
	if err := workeridentity.SecureHostTree(manifestPath); err != nil {
		return err
	}
	ready, err := Bootstrap(configPath, manifestPath)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = providerRequest(ready.Address, providerKillReq, nil, providerKillAck)
	}()
	return relayProviderTerminal(ready.Address, stdin, stdout)
}

func providerArguments(action, provider string, opts ProviderCommandOptions) []string {
	if provider == ExecutableClaude {
		if action == "run" {
			return []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits", "--session-id", opts.SessionID, opts.Prompt}
		}
		if action == "resume" {
			return []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits", "--resume", opts.SessionID, opts.Prompt}
		}
		return []string{"auth", action}
	}
	if action == "status" {
		return []string{"login", "status"}
	}
	return []string{"login"}
}

func providerEnvironmentKeys(provider string) []string {
	if provider != ExecutableClaude {
		return nil
	}
	return []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"CLAUDE_CODE_EFFORT_LEVEL",
	}
}

func relayProviderTerminal(address string, stdin io.Reader, stdout io.Writer) error {
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		buf := make([]byte, 1024)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				_ = providerSend(address, providerTerminalInput, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	last := ""
	for {
		output, _ := providerOutput(address)
		if output != "" && output != last {
			if strings.HasPrefix(output, last) {
				_, _ = io.WriteString(stdout, output[len(last):])
			} else {
				_, _ = io.WriteString(stdout, output)
			}
			last = output
		}
		status, err := providerProcessStatus(address)
		if err != nil {
			return err
		}
		if !status.Alive {
			if status.ExitCode != nil && *status.ExitCode != 0 {
				return fmt.Errorf("worker provider: process exited with code %d", *status.ExitCode)
			}
			return nil
		}
		select {
		case <-inputDone:
		default:
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func providerOutput(address string) (string, error) {
	payload, _ := json.Marshal(map[string]int{"lines": 500})
	response, err := providerRequest(address, providerGetOutputReq, payload, providerGetOutputRes)
	return string(response), err
}

func providerProcessStatus(address string) (providerStatus, error) {
	response, err := providerRequest(address, providerStatusReq, nil, providerStatusRes)
	if err != nil {
		return providerStatus{}, err
	}
	var status providerStatus
	return status, json.Unmarshal(response, &status)
}

func providerSend(address string, messageType byte, payload []byte) error {
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(providerFrame(messageType, payload))
	return err
}

func providerRequest(address string, requestType byte, payload []byte, responseType byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(providerFrame(requestType, payload)); err != nil {
		return nil, err
	}
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			return nil, err
		}
		body := make([]byte, binary.BigEndian.Uint32(header[1:]))
		if _, err := io.ReadFull(conn, body); err != nil {
			return nil, err
		}
		if header[0] == responseType {
			return body, nil
		}
	}
}

func providerFrame(messageType byte, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = messageType
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}
