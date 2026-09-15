package herdr

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type activitySink interface {
	ApplyActivitySignal(context.Context, domain.SessionID, ports.ActivitySignal) error
}

type response struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params struct {
		PaneID         string `json:"pane_id"`
		Target         string `json:"target"`
		Source         string `json:"source"`
		Agent          string `json:"agent"`
		State          string `json:"state"`
		AgentSessionID string `json:"agent_session_id"`
		CustomStatus   string `json:"custom_status"`
	} `json:"params"`
}

func (s *Server) handle(ctx context.Context, line []byte) response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil || req.ID == "" {
		return response{Error: "invalid request"}
	}
	reply := response{ID: req.ID}
	pane := req.Params.PaneID
	if req.Method == "agent.rename" {
		pane = req.Params.Target
	}
	id, launchID, ok := decodePaneID(pane)
	if !ok {
		reply.Error = "invalid pane identity"
		return reply
	}
	signal := ports.ActivitySignal{LaunchID: launchID, ExpectedHarness: domain.HarnessFX, Timestamp: time.Now()}
	switch req.Method {
	case "pane.rename", "agent.rename":
		// fx omits source and agent on rename. These methods only receive an
		// acknowledgement; AO display names and lifecycle facts are untouched.
		reply.OK = true
		return reply
	case "pane.clear_agent_authority":
		// Native clear carries source only. Runtime liveness observation owns
		// process exit, so clear must never terminate a session.
		reply.OK = req.Params.Source == "custom:fx"
		if !reply.OK {
			reply.Error = "invalid source"
		}
		return reply
	case "pane.report_agent", "pane.report_agent_session":
		if req.Params.Source != "custom:fx" || req.Params.Agent != "fx" {
			reply.Error = "invalid source or agent"
			return reply
		}
	default:
		reply.Error = "unsupported method"
		return reply
	}
	if req.Method == "pane.report_agent_session" {
		signal.AgentSessionID = strings.TrimSpace(req.Params.AgentSessionID)
		if signal.AgentSessionID == "" {
			reply.Error = "missing native session id"
			return reply
		}
	} else {
		switch req.Params.State {
		case "working":
			signal.State = domain.ActivityActive
		case "idle":
			signal.State = domain.ActivityWaitingInput
		case "blocked":
			signal.State = domain.ActivityBlocked
		default:
			reply.Error = "unsupported state"
			return reply
		}
		signal.Valid = true
		if req.Params.CustomStatus != "" {
			s.logger.Debug("fx Herdr activity", "session_id", id, "launch_id", launchID,
				"state", signal.State, "custom_status", req.Params.CustomStatus)
		}
	}
	if err := s.sink.ApplyActivitySignal(ctx, id, signal); err != nil {
		s.logger.Debug("fx Herdr signal rejected", "session_id", id, "error", err)
		reply.Error = "activity signal unavailable"
		return reply
	}
	reply.OK = true
	return reply
}
