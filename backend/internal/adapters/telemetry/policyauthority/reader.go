// Package policyauthority reads the desktop-owned telemetry policy file.
// Filesystem trust checks stay in this adapter; policy coordination consumes
// only the provider-neutral authority snapshot exposed by ports.
package policyauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reader reads one fixed telemetry-policy authority path.
type Reader struct{ path string }

// New constructs an authority reader for path.
func New(path string) *Reader { return &Reader{path: path} }

// ReadAgentSwitchFailureAuthority validates and reads the durable authority.
func (r *Reader) ReadAgentSwitchFailureAuthority(ctx context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, err
	}
	info, err := os.Lstat(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.AgentSwitchFailureAuthoritySnapshot{}, nil
		}
		return ports.AgentSwitchFailureAuthoritySnapshot{}, fmt.Errorf("inspect telemetry policy authority: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, errors.New("telemetry policy authority is unsafe")
	}
	file, err := os.Open(r.path)
	if err != nil {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, fmt.Errorf("open telemetry policy authority: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	var keys map[string]json.RawMessage
	if err := decoder.Decode(&keys); err != nil || len(keys) != 4 || keys["schema_version"] == nil || keys["events_enabled"] == nil || keys["consent_generation"] == nil || keys["updated_at"] == nil {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, errors.New("telemetry policy authority is malformed")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, errors.New("telemetry policy authority has trailing data")
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, fmt.Errorf("normalize telemetry policy authority: %w", err)
	}
	decoder = json.NewDecoder(io.LimitReader(bytes.NewReader(raw), 4097))
	decoder.DisallowUnknownFields()
	var record diskRecord
	if err := decoder.Decode(&record); err != nil || record.SchemaVersion != 1 || uuid.Validate(record.ConsentGeneration) != nil || !validTimestamp(record.UpdatedAt) {
		return ports.AgentSwitchFailureAuthoritySnapshot{}, errors.New("telemetry policy authority fields are invalid")
	}
	return ports.AgentSwitchFailureAuthoritySnapshot{
		Present: true, EventsEnabled: record.EventsEnabled, ConsentGeneration: record.ConsentGeneration,
	}, nil
}

type diskRecord struct {
	SchemaVersion     int    `json:"schema_version"`
	EventsEnabled     bool   `json:"events_enabled"`
	ConsentGeneration string `json:"consent_generation"`
	UpdatedAt         string `json:"updated_at"`
}

func validTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

var _ ports.AgentSwitchFailureAuthorityReader = (*Reader)(nil)
