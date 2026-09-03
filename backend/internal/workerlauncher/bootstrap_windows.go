//go:build windows

package workerlauncher

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
)

type ReadyMessage struct {
	Nonce       string       `json:"nonce"`
	Address     string       `json:"address,omitempty"`
	LauncherPID int          `json:"launcher_pid,omitempty"`
	Token       TokenDetails `json:"token"`
	Error       string       `json:"error,omitempty"`
}

type ServiceLaunchRequest struct {
	ManifestPath string            `json:"manifest_path"`
	ReadyAddress string            `json:"ready_address"`
	ReadyNonce   string            `json:"ready_nonce"`
	Password     []byte            `json:"password"`
	Environment  map[string]string `json:"environment"`
}

type ServiceLaunchResponse struct {
	Error string `json:"error,omitempty"`
}

func Bootstrap(configPath, manifestPath string) (ReadyMessage, error) {
	cfg, err := workeridentity.LoadConfig(configPath)
	if err != nil {
		return ReadyMessage{}, err
	}
	manifest, err := ReadAndValidateManifest(manifestPath, cfg)
	if err != nil {
		return ReadyMessage{}, err
	}
	password, err := workeridentity.LoadPassword(cfg)
	if err != nil {
		return ReadyMessage{}, err
	}
	defer zeroBytes(password)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ReadyMessage{}, err
	}
	defer listener.Close()
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return ReadyMessage{}, err
	}
	readyNonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	selectedEnvironment := map[string]string{}
	for _, key := range manifest.AllowedEnvironmentKeys {
		if value := os.Getenv(key); value != "" {
			selectedEnvironment[key] = value
		}
	}
	request := ServiceLaunchRequest{ManifestPath: manifestPath, ReadyAddress: listener.Addr().String(), ReadyNonce: readyNonce,
		Password: append([]byte(nil), password...), Environment: selectedEnvironment}
	if err := requestServiceLaunch(cfg.PipeName, request); err != nil {
		return ReadyMessage{}, err
	}
	deadline := time.Now().Add(15 * time.Second)
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(deadline)
	}
	conn, err := listener.Accept()
	if err != nil {
		return ReadyMessage{}, fmt.Errorf("worker launcher: wait for worker readiness: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	var ready ReadyMessage
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&ready); err != nil {
		return ReadyMessage{}, err
	}
	if ready.Nonce != readyNonce {
		return ReadyMessage{}, errors.New("worker launcher: invalid readiness nonce")
	}
	if ready.Error != "" {
		return ready, errors.New(ready.Error)
	}
	return ready, nil
}

func requestServiceLaunch(pipeName string, request ServiceLaunchRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, pipeName)
	if err != nil {
		return fmt.Errorf("worker launcher: connect launcher service: %w", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	zeroBytes(request.Password)
	var response ServiceLaunchResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func buildWorkerEnvironment(m Manifest, cfg workeridentity.Config, providerProfileRoot string, selected map[string]string) []string {
	values := map[string]string{
		"SYSTEMROOT": os.Getenv("SYSTEMROOT"), "WINDIR": os.Getenv("WINDIR"),
		"COMSPEC": filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "cmd.exe"),
		"HOME":    m.WorkerProfileRoot, "USERPROFILE": m.WorkerProfileRoot,
		"APPDATA":      filepath.Join(providerProfileRoot, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(providerProfileRoot, "AppData", "Local"),
		"TEMP":         filepath.Join(m.WorkerProfileRoot, "Temp"), "TMP": filepath.Join(m.WorkerProfileRoot, "Temp"),
		"AO_WORKER_IDENTITY":  cfg.AccountSID,
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "NUL",
		"GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "",
		"GIT_CONFIG_KEY_1": "safe.directory", "GIT_CONFIG_VALUE_1": m.CanonicalWorktree,
		"GIT_TERMINAL_PROMPT": "0", "GCM_INTERACTIVE": "Never",
		"GH_CONFIG_DIR":     filepath.Join(m.WorkerProfileRoot, ".config", "gh"),
		"GLAB_CONFIG_DIR":   filepath.Join(m.WorkerProfileRoot, ".config", "glab-cli"),
		"CLAUDE_CONFIG_DIR": filepath.Join(m.WorkerProfileRoot, ".claude"),
		"CODEX_HOME":        filepath.Join(m.WorkerProfileRoot, ".codex"),
		"SSH_AUTH_SOCK":     "", "GIT_AUTHOR_NAME": "Agent Orchestrator Worker",
		"GIT_AUTHOR_EMAIL": "ao-worker@localhost", "GIT_COMMITTER_NAME": "Agent Orchestrator Worker",
		"GIT_COMMITTER_EMAIL": "ao-worker@localhost",
	}
	pathDirs := []string{filepath.Join(os.Getenv("SYSTEMROOT"), "System32"), filepath.Dir(cfg.PTYHostPath), filepath.Dir(cfg.LauncherPath)}
	for _, path := range cfg.Executables {
		pathDirs = append(pathDirs, filepath.Dir(path))
	}
	values["PATH"] = strings.Join(uniqueFold(pathDirs), string(os.PathListSeparator))
	for _, key := range m.AllowedEnvironmentKeys {
		if key == "CLAUDE_CONFIG_DIR" || key == "CODEX_HOME" {
			continue
		}
		if value := selected[key]; value != "" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	allowEmpty := map[string]struct{}{"GIT_CONFIG_VALUE_0": {}}
	for _, key := range keys {
		if values[key] != "" {
			out = append(out, key+"="+values[key])
		} else if _, ok := allowEmpty[key]; ok {
			out = append(out, key+"=")
		}
	}
	return out
}

func uniqueFold(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean := filepath.Clean(value)
		key := strings.ToUpper(clean)
		if clean != "." {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				out = append(out, clean)
			}
		}
	}
	return out
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
