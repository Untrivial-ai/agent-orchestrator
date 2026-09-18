package qodercli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// qoderAuthEnvVars are the credentials Qoder CLI accepts from the environment.
// Any one of them authenticates a run without a stored login.
var qoderAuthEnvVars = []string{
	"QODER_PERSONAL_ACCESS_TOKEN",
	"QODER_SERVICE_ACCOUNT_KEY",
	"QODER_JOB_TOKEN",
}

// AuthStatus checks Qoder CLI's local authentication state without starting a
// session, via `qodercli status -o json`.
//
// That probe reports the *stored* login only, so an environment credential
// cannot make it say "logged in" — but neither does its absence prove the run
// would fail. A configured credential env var therefore downgrades a negative
// result to Unknown rather than claiming either answer: the variable may hold a
// token the server later rejects, which only a real request can find out.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.qodercliBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := aoprocess.CommandContext(probeCtx, binary, "status", "-o", "json").CombinedOutput()
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	if status, ok := authStatusFromOutput(out); ok {
		if status == ports.AgentAuthStatusUnauthorized && hasCredentialEnv() {
			return ports.AgentAuthStatusUnknown, nil
		}
		return status, nil
	}
	// An unfamiliar non-zero result is not affirmative evidence of missing
	// credentials. Keep this advisory probe unknown and let launch report the
	// authoritative failure.
	_ = err
	return ports.AgentAuthStatusUnknown, nil
}

func hasCredentialEnv() bool {
	for _, name := range qoderAuthEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func authStatusFromOutput(out []byte) (ports.AgentAuthStatus, bool) {
	start := bytes.IndexByte(out, '{')
	end := bytes.LastIndexByte(out, '}')
	if start < 0 || end < start {
		return ports.AgentAuthStatusUnknown, false
	}
	var status struct {
		LoggedIn bool `json:"logged_in"`
	}
	if json.Unmarshal(out[start:end+1], &status) != nil {
		return ports.AgentAuthStatusUnknown, false
	}
	if status.LoggedIn {
		return ports.AgentAuthStatusAuthorized, true
	}
	return ports.AgentAuthStatusUnauthorized, true
}
