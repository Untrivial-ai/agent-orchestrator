package unrealagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type providerConfig struct {
	AOSessionID            string `json:"aoSessionId"`
	ProviderConversationID string `json:"providerConversationId"`
	DataDir                string `json:"dataDir"`
	WorkspacePath          string `json:"workspacePath"`
	Model                  string `json:"model,omitempty"`
	Effort                 string `json:"effort,omitempty"`
	SystemPrompt           string `json:"systemPrompt,omitempty"`
}

func writeProviderConfig(cfg providerConfig) (string, error) {
	if err := validateProviderConfig(cfg); err != nil {
		return "", err
	}
	directory := filepath.Join(cfg.DataDir, "chat-hosts", cfg.AOSessionID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create Unreal Agent host directory: %w", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encode Unreal Agent provider config: %w", err)
	}
	path := filepath.Join(directory, "unreal-provider.json")
	temporary := path + ".tmp-" + uuid.NewString()
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return "", fmt.Errorf("write Unreal Agent provider config: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", fmt.Errorf("publish Unreal Agent provider config: %w", err)
	}
	return path, nil
}

func readProviderConfig(path string) (providerConfig, error) {
	file, err := os.Open(path) //nolint:gosec // path is supplied only by AO's hidden child command
	if err != nil {
		return providerConfig{}, fmt.Errorf("open Unreal Agent provider config: %w", err)
	}
	defer func() { _ = file.Close() }()
	var cfg providerConfig
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return providerConfig{}, fmt.Errorf("decode Unreal Agent provider config: %w", err)
	}
	if err := validateProviderConfig(cfg); err != nil {
		return providerConfig{}, err
	}
	return cfg, nil
}

func validateProviderConfig(cfg providerConfig) error {
	if !filepath.IsAbs(cfg.DataDir) || !filepath.IsAbs(cfg.WorkspacePath) {
		return errors.New("unreal agent provider requires absolute data and workspace paths")
	}
	if cfg.AOSessionID == "" || filepath.Base(cfg.AOSessionID) != cfg.AOSessionID || strings.ContainsAny(cfg.AOSessionID, `/\\`) {
		return errors.New("invalid Unreal Agent AO session id")
	}
	if cfg.ProviderConversationID == "" || filepath.Base(cfg.ProviderConversationID) != cfg.ProviderConversationID || strings.ContainsAny(cfg.ProviderConversationID, `/\\`) {
		return errors.New("invalid Unreal Agent provider conversation id")
	}
	return nil
}
