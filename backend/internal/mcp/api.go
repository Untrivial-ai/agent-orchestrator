// Package mcp implements an MCP server that drives the local AO daemon over its
// loopback HTTP API. The CLI wires transport and daemon discovery; this package
// owns tool schemas and handlers.
package mcp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// maxDisplayNameLen matches the CLI/daemon spawn name limit.
const maxDisplayNameLen = 20

// DaemonAPI is the thin daemon surface the MCP tools need. The CLI implements
// this with the same loopback HTTP helpers as other product commands.
type DaemonAPI interface {
	GetJSON(ctx context.Context, path string, out any) error
	PostJSON(ctx context.Context, path string, body, out any) error
}

type projectListResponse struct {
	Projects []projectSummary `json:"projects"`
}

type projectSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	SessionPrefix string `json:"sessionPrefix"`
	ResolveError  string `json:"resolveError,omitempty"`
}

type projectResponse struct {
	Project projectDetails `json:"project"`
}

type projectDetails struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Path   string         `json:"path"`
	Config *projectConfig `json:"config,omitempty"`
}

type projectConfig struct {
	Worker       *roleOverride `json:"worker,omitempty"`
	Orchestrator *roleOverride `json:"orchestrator,omitempty"`
}

type roleOverride struct {
	Agent string `json:"agent,omitempty"`
}

type sessionListResponse struct {
	Sessions []sessionDTO `json:"sessions"`
}

type sessionResponse struct {
	Session sessionDTO `json:"session"`
}

type sessionDTO struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId"`
	Kind         string          `json:"kind"`
	Harness      string          `json:"harness,omitempty"`
	DisplayName  string          `json:"displayName,omitempty"`
	Activity     sessionActivity `json:"activity"`
	IsTerminated bool            `json:"isTerminated"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Status       string          `json:"status"`
	Branch       string          `json:"branch,omitempty"`
	PRs          []sessionPRDTO  `json:"prs"`
}

type sessionActivity struct {
	State          string    `json:"state"`
	LastActivityAt time.Time `json:"lastActivityAt"`
}

type sessionPRDTO struct {
	URL    string `json:"url"`
	Number int    `json:"number"`
	State  string `json:"state"`
	CI     string `json:"ci"`
	Review string `json:"review"`
}

type sessionPRSummaryResponse struct {
	PRs []sessionPRSummaryDTO `json:"prs"`
}

type sessionPRSummaryDTO struct {
	Number int `json:"number"`
	CI     struct {
		State string `json:"state"`
	} `json:"ci"`
	Review struct {
		Decision              string `json:"decision"`
		UnresolvedThreadCount *int   `json:"unresolvedThreadCount"`
	} `json:"review"`
	URL   string `json:"url,omitempty"`
	State string `json:"state,omitempty"`
}

type spawnRequest struct {
	ProjectID   string `json:"projectId,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Harness     string `json:"harness,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Model       string `json:"model,omitempty"`
	DisplayName string `json:"displayName"`
}

type spawnResult struct {
	Session struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		DisplayName string `json:"displayName"`
	} `json:"session"`
}

type sendAPIRequest struct {
	Message string `json:"message"`
}

type killSessionResponse struct {
	SessionID string `json:"sessionId"`
	Freed     bool   `json:"freed"`
}

type conversationSnapshot struct {
	SessionID  string                 `json:"sessionId"`
	Mode       string                 `json:"mode"`
	Controller string                 `json:"controller"`
	Messages   []conversationMessage  `json:"messages"`
	Activities []conversationActivity `json:"activities"`
}

type conversationMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Origin    string `json:"origin"`
	Text      string `json:"text"`
	Sequence  int64  `json:"sequence"`
	CreatedAt string `json:"createdAt"`
}

type conversationActivity struct {
	ID           string `json:"id"`
	ActivityKind string `json:"activityKind"`
	Status       string `json:"status"`
	Summary      string `json:"summary"`
	Sequence     int64  `json:"sequence"`
}

type ensureAgentReadinessRequest struct {
	AgentIDs []string `json:"agentIds"`
	Purpose  string   `json:"purpose"`
}

func apiPath(base string, params url.Values) string {
	if len(params) == 0 {
		return base
	}
	return base + "?" + params.Encode()
}

func resolveSpawnHarness(ctx context.Context, api DaemonAPI, explicit, projectID string, standalone bool) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit, nil
	}
	if standalone {
		return "", fmt.Errorf("agent is required for standalone spawn")
	}
	var res projectResponse
	if err := api.GetJSON(ctx, "projects/"+url.PathEscape(projectID), &res); err != nil {
		return "", err
	}
	if res.Project.Config != nil && res.Project.Config.Worker != nil {
		if agent := strings.TrimSpace(res.Project.Config.Worker.Agent); agent != "" {
			return agent, nil
		}
	}
	return "", fmt.Errorf("project %q has no worker agent; pass agent explicitly", projectID)
}

func validateDisplayName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > maxDisplayNameLen {
		return fmt.Errorf("name must be %d characters or fewer", maxDisplayNameLen)
	}
	return nil
}
