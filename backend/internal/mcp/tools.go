package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func toolResultJSON(v any) (*mcpsdk.CallToolResult, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(raw)}},
	}, nil
}

func toolError(err error) *mcpsdk.CallToolResult {
	if err == nil {
		return nil
	}
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: err.Error()}},
	}
}

type listProjectsInput struct{}

type listSessionsInput struct {
	ProjectID            string `json:"project_id,omitempty" jsonschema:"Filter by project id"`
	IncludeTerminated    bool   `json:"include_terminated,omitempty" jsonschema:"Include terminated sessions"`
	IncludeOrchestrators bool   `json:"include_orchestrators,omitempty" jsonschema:"Include orchestrator sessions"`
}

type spawnWorkerInput struct {
	Name       string `json:"name" jsonschema:"Session display name (required, max 20 characters)"`
	ProjectID  string `json:"project_id,omitempty" jsonschema:"Registered project id. Omit when standalone is true"`
	Standalone bool   `json:"standalone,omitempty" jsonschema:"Launch in an AO-managed plain directory without a project"`
	Agent      string `json:"agent,omitempty" jsonschema:"Agent harness id (required for standalone; otherwise project worker default)"`
	Prompt     string `json:"prompt,omitempty" jsonschema:"Initial prompt delivered to the worker"`
	Mode       string `json:"mode,omitempty" jsonschema:"Session mode: chat or tui"`
	Branch     string `json:"branch,omitempty" jsonschema:"Optional starting branch (project workers only)"`
	Model      string `json:"model,omitempty" jsonschema:"Optional model override"`
}

type sendMessageInput struct {
	SessionID string `json:"session_id" jsonschema:"Target session id"`
	Message   string `json:"message" jsonschema:"Message body to deliver"`
}

type readSessionOutputInput struct {
	SessionID string `json:"session_id" jsonschema:"Session id"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max conversation items to return (1-100, default 30)"`
}

type getPRStatusInput struct {
	SessionID string `json:"session_id" jsonschema:"Session id whose claimed PRs to inspect"`
}

type killSessionInput struct {
	SessionID string `json:"session_id" jsonschema:"Session id to terminate"`
}

func registerTools(server *mcpsdk.Server, api DaemonAPI) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_projects",
		Description: "List AO projects registered with the local daemon.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ listProjectsInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := listProjects(ctx, api)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_sessions",
		Description: "List AO sessions with derived status and PR/CI summary facts.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in listSessionsInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := listSessions(ctx, api, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "spawn_worker",
		Description: "Spawn a worker session in a registered project or as a standalone AO-managed directory.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in spawnWorkerInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := spawnWorker(ctx, api, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "send_message",
		Description: "Send a message to a running AO session (Terminal UI or Chat).",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in sendMessageInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := sendMessage(ctx, api, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "read_session_output",
		Description: "Read recent Chat conversation messages and activities for a session.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in readSessionOutputInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := readSessionOutput(ctx, api, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "get_pr_status",
		Description: "Get PR, CI, and review status for a session's claimed pull requests.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in getPRStatusInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := getPRStatus(ctx, api, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "kill_session",
		Description: "Terminate an AO session and free its runtime resources.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in killSessionInput) (*mcpsdk.CallToolResult, any, error) {
		res, err := killSession(ctx, api, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		out, err := toolResultJSON(res)
		return out, nil, err
	})
}

func listProjects(ctx context.Context, api DaemonAPI) (any, error) {
	var res projectListResponse
	if err := api.GetJSON(ctx, "projects", &res); err != nil {
		return nil, err
	}
	projects := make([]map[string]any, 0, len(res.Projects))
	for _, p := range res.Projects {
		entry := map[string]any{
			"id":            p.ID,
			"name":          p.Name,
			"kind":          p.Kind,
			"sessionPrefix": p.SessionPrefix,
		}
		if p.ResolveError != "" {
			entry["resolveError"] = p.ResolveError
		}
		projects = append(projects, entry)
	}
	return map[string]any{"projects": projects}, nil
}

func listSessions(ctx context.Context, api DaemonAPI, in listSessionsInput) (any, error) {
	params := url.Values{}
	if project := strings.TrimSpace(in.ProjectID); project != "" {
		params.Set("project", project)
	}
	if !in.IncludeTerminated {
		params.Set("active", "true")
	}
	var res sessionListResponse
	if err := api.GetJSON(ctx, apiPath("sessions", params), &res); err != nil {
		return nil, err
	}
	sessions := make([]sessionDTO, 0, len(res.Sessions))
	for _, sess := range res.Sessions {
		if !in.IncludeOrchestrators && sess.Kind == "orchestrator" {
			continue
		}
		sessions = append(sessions, sess)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	out := make([]map[string]any, 0, len(sessions))
	for _, sess := range sessions {
		entry := map[string]any{
			"id":           sess.ID,
			"projectId":    sess.ProjectID,
			"kind":         sess.Kind,
			"status":       sess.Status,
			"activity":     sess.Activity.State,
			"harness":      sess.Harness,
			"displayName":  sess.DisplayName,
			"branch":       sess.Branch,
			"isTerminated": sess.IsTerminated,
			"updatedAt":    sess.UpdatedAt,
			"prs":          sess.PRs,
		}
		if len(sess.PRs) > 0 {
			var summary sessionPRSummaryResponse
			if err := api.GetJSON(ctx, "sessions/"+url.PathEscape(sess.ID)+"/pr", &summary); err == nil && len(summary.PRs) > 0 {
				entry["prDetails"] = summary.PRs
			}
		}
		out = append(out, entry)
	}
	return map[string]any{"sessions": out}, nil
}

func spawnWorker(ctx context.Context, api DaemonAPI, in spawnWorkerInput) (any, error) {
	if err := validateDisplayName(in.Name); err != nil {
		return nil, err
	}
	standalone := in.Standalone
	projectID := strings.TrimSpace(in.ProjectID)
	if standalone && projectID != "" {
		return nil, fmt.Errorf("standalone and project_id cannot be used together")
	}
	if !standalone && projectID == "" {
		return nil, fmt.Errorf("project_id is required unless standalone is true")
	}
	mode := strings.TrimSpace(in.Mode)
	if mode != "" && mode != "chat" && mode != "tui" {
		return nil, fmt.Errorf(`mode must be "chat" or "tui"`)
	}
	if standalone && strings.TrimSpace(in.Branch) != "" {
		return nil, fmt.Errorf("standalone sessions do not support branch")
	}

	harness, err := resolveSpawnHarness(ctx, api, in.Agent, projectID, standalone)
	if err != nil {
		return nil, err
	}
	// Best-effort readiness ensure; spawn still validates at runtime.
	_ = api.PostJSON(ctx, "agents/readiness/ensure", ensureAgentReadinessRequest{
		AgentIDs: []string{harness},
		Purpose:  "launch",
	}, nil)

	req := spawnRequest{
		ProjectID:   projectID,
		Kind:        "worker",
		Mode:        mode,
		Harness:     harness,
		Branch:      strings.TrimSpace(in.Branch),
		Prompt:      in.Prompt,
		Model:       strings.TrimSpace(in.Model),
		DisplayName: strings.TrimSpace(in.Name),
	}
	var res spawnResult
	if err := api.PostJSON(ctx, "sessions", req, &res); err != nil {
		return nil, err
	}
	if strings.TrimSpace(res.Session.ID) == "" {
		return nil, fmt.Errorf("daemon returned empty session id for spawn")
	}
	return map[string]any{
		"sessionId":   res.Session.ID,
		"status":      res.Session.Status,
		"displayName": res.Session.DisplayName,
		"harness":     harness,
		"projectId":   projectID,
		"standalone":  standalone,
	}, nil
}

func sendMessage(ctx context.Context, api DaemonAPI, in sendMessageInput) (any, error) {
	sessionID := strings.TrimSpace(in.SessionID)
	message := strings.TrimSpace(in.Message)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	if err := api.PostJSON(ctx, "sessions/"+url.PathEscape(sessionID)+"/send", sendAPIRequest{Message: message}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "sessionId": sessionID}, nil
}

func readSessionOutput(ctx context.Context, api DaemonAPI, in readSessionOutputInput) (any, error) {
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", limit))
	var snap conversationSnapshot
	if err := api.GetJSON(ctx, apiPath("sessions/"+url.PathEscape(sessionID)+"/conversation", params), &snap); err != nil {
		return nil, err
	}

	messages := make([]map[string]any, 0, len(snap.Messages))
	for _, msg := range snap.Messages {
		messages = append(messages, map[string]any{
			"id":        msg.ID,
			"role":      msg.Role,
			"origin":    msg.Origin,
			"text":      msg.Text,
			"sequence":  msg.Sequence,
			"createdAt": msg.CreatedAt,
		})
	}
	activities := make([]map[string]any, 0, len(snap.Activities))
	for _, act := range snap.Activities {
		activities = append(activities, map[string]any{
			"id":           act.ID,
			"activityKind": act.ActivityKind,
			"status":       act.Status,
			"summary":      act.Summary,
			"sequence":     act.Sequence,
		})
	}
	return map[string]any{
		"sessionId":  snap.SessionID,
		"mode":       snap.Mode,
		"controller": snap.Controller,
		"messages":   messages,
		"activities": activities,
	}, nil
}

func getPRStatus(ctx context.Context, api DaemonAPI, in getPRStatusInput) (any, error) {
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	var sess sessionResponse
	if err := api.GetJSON(ctx, "sessions/"+url.PathEscape(sessionID), &sess); err != nil {
		return nil, err
	}
	prs := any(sess.Session.PRs)
	var summary sessionPRSummaryResponse
	if err := api.GetJSON(ctx, "sessions/"+url.PathEscape(sessionID)+"/pr", &summary); err == nil {
		prs = summary.PRs
	}
	return map[string]any{
		"sessionId": sessionID,
		"status":    sess.Session.Status,
		"branch":    sess.Session.Branch,
		"prs":       prs,
	}, nil
}

func killSession(ctx context.Context, api DaemonAPI, in killSessionInput) (any, error) {
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	var res killSessionResponse
	if err := api.PostJSON(ctx, "sessions/"+url.PathEscape(sessionID)+"/kill", struct{}{}, &res); err != nil {
		return nil, err
	}
	return map[string]any{
		"sessionId": res.SessionID,
		"freed":     res.Freed,
	}, nil
}
