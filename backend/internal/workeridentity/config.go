package workeridentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ConfigVersion = 1

const (
	DefaultAccountName = "AOAgentWorker"
	ConfigFileName     = "worker-identity.json"
	SecretFileName     = "worker-account.dpapi"
)

type Config struct {
	Version            int               `json:"version"`
	AccountName        string            `json:"account_name"`
	AccountSID         string            `json:"account_sid"`
	HostSID            string            `json:"host_sid"`
	WorkspaceRoot      string            `json:"workspace_root"`
	SessionProfileRoot string            `json:"session_profile_root"`
	LauncherPath       string            `json:"launcher_path"`
	PTYHostPath        string            `json:"pty_host_path"`
	Executables        map[string]string `json:"executables"`
	ExecutableSHA256   map[string]string `json:"executable_sha256,omitempty"`
	GitMetadataRoots   []string          `json:"git_metadata_roots,omitempty"`
	ServiceName        string            `json:"service_name"`
	PipeName           string            `json:"pipe_name"`
	SecretPath         string            `json:"secret_path"`
}

func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "security", ConfigFileName)
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("worker identity: decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("worker identity: unsupported config version %d", c.Version)
	}
	for name, value := range map[string]string{
		"account_name": c.AccountName, "account_sid": c.AccountSID, "host_sid": c.HostSID,
		"workspace_root": c.WorkspaceRoot, "session_profile_root": c.SessionProfileRoot,
		"launcher_path": c.LauncherPath, "pty_host_path": c.PTYHostPath, "secret_path": c.SecretPath,
		"service_name": c.ServiceName, "pipe_name": c.PipeName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("worker identity: %s is required", name)
		}
	}
	if !strings.HasPrefix(c.AccountSID, "S-1-") {
		return errors.New("worker identity: invalid account SID")
	}
	if len(c.Executables) == 0 {
		return errors.New("worker identity: executable catalog is empty")
	}
	for id, path := range c.Executables {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(path) == "" {
			return errors.New("worker identity: invalid executable catalog entry")
		}
	}
	if len(c.ExecutableSHA256) > 0 {
		for id := range c.Executables {
			if len(c.ExecutableSHA256[id]) != 64 {
				return fmt.Errorf("worker identity: executable %q has no valid SHA256", id)
			}
		}
	}
	return nil
}
