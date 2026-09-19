package cursor

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks the credential selected by this invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	key, overridden := check.Env["CURSOR_API_KEY"]
	if !overridden {
		key = os.Getenv("CURSOR_API_KEY")
	}
	for i, arg := range check.Args {
		if arg == "--" {
			break
		}
		if value, ok := strings.CutPrefix(arg, "--api-key="); ok {
			key = value
		} else if arg == "--api-key" && i+1 < len(check.Args) && !strings.HasPrefix(check.Args[i+1], "-") {
			key = check.Args[i+1]
		}
	}
	if strings.TrimSpace(key) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return cursorCLIAuthStatus(ctx, binary, check, p.authRunner)
}

func cursorCLIAuthStatus(ctx context.Context, binary string, check ports.AgentAuthCheck, run authprobe.ScopedCmdRunner) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if run == nil {
		run = authprobe.RunScopedCommand
	}
	out, err := run(probeCtx, check, binary, "status", "--format", "json")
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if err != nil || probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	var result cursorStatusResponse
	if json.Unmarshal(out, &result) != nil || result.IsAuthenticated == nil || result.HasAccessToken == nil || result.HasRefreshToken == nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	switch result.Status {
	case "authenticated":
		if *result.IsAuthenticated && *result.HasAccessToken && *result.HasRefreshToken {
			// Cursor also emits authenticated after getMe fails. Only its
			// successful getMe branch supplies userInfo (status.ts, 2026.09.15).
			if user := result.UserInfo; user != nil && (user.Email != nil || user.UserID != nil || user.FirstName != nil || user.LastName != nil || user.TeamID != nil || user.CreatedAt != nil) {
				return ports.AgentAuthStatusAuthorized, nil
			}
			return ports.AgentAuthStatusConfigured, nil
		}
	case "partially-authenticated":
		if !*result.IsAuthenticated && *result.HasAccessToken && !*result.HasRefreshToken {
			return ports.AgentAuthStatusConfigured, nil
		}
	case "unauthenticated":
		if !*result.IsAuthenticated && !*result.HasAccessToken && !*result.HasRefreshToken && result.UserInfo == nil {
			return ports.AgentAuthStatusUnauthorized, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

type cursorStatusResponse struct {
	Status          string `json:"status"`
	IsAuthenticated *bool  `json:"isAuthenticated"`
	HasAccessToken  *bool  `json:"hasAccessToken"`
	HasRefreshToken *bool  `json:"hasRefreshToken"`
	UserInfo        *struct {
		Email     *string `json:"email"`
		UserID    *int32  `json:"userId"`
		FirstName *string `json:"firstName"`
		LastName  *string `json:"lastName"`
		TeamID    *int32  `json:"teamId"`
		CreatedAt *string `json:"createdAt"`
	} `json:"userInfo"`
}
