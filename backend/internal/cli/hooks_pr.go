package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// Only accept gh's complete stdout result, not URLs embedded in help, prose,
// stderr, or a command that merely views/prints an existing PR. The daemon
// verifies the provider facts and repository before persisting ownership.
var createdGitHubPRURL = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[1-9][0-9]*$`)

func hookCreatedPR(agent, event string, payload []byte) string {
	if agent != "claude-code" || event != "post-tool-use" {
		return ""
	}
	var p struct {
		ToolName string `json:"tool_name"`
		Input    struct {
			Command    string `json:"command"`
			Background bool   `json:"run_in_background"`
		} `json:"tool_input"`
		Response struct {
			Stdout        string `json:"stdout"`
			Interrupted   bool   `json:"interrupted"`
			ExitCode      *int   `json:"exitCode"`
			ExitCodeSnake *int   `json:"exit_code"`
		} `json:"tool_response"`
	}
	if json.Unmarshal(payload, &p) != nil || p.ToolName != "Bash" || p.Input.Background || p.Response.Interrupted {
		return ""
	}
	if p.Response.ExitCode != nil && *p.Response.ExitCode != 0 || p.Response.ExitCodeSnake != nil && *p.Response.ExitCodeSnake != 0 {
		return ""
	}
	ref := strings.TrimSpace(p.Response.Stdout)
	if !createdGitHubPRURL.MatchString(ref) || !isGHPRCreate(p.Input.Command) {
		return ""
	}
	return ref
}

// Parse, never execute, shell syntax. A successful tool result only proves the
// shell's final status: "gh pr create ... || echo <url>" must not register a PR.
// Recognize a standalone create, optionally preceded by a quiet `cd ... &&`.
// Other scripts, pipelines, wrappers and background jobs use polling instead.
func isGHPRCreate(command string) bool {
	if len(command) > maxHookInteractionLen {
		return false
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(f.Stmts) != 1 {
		return false
	}
	stmt := f.Stmts[0]
	if !plainPRHookStmt(stmt) {
		return false
	}
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		if binary.Op != syntax.AndStmt || !plainPRHookStmt(binary.X) || !plainPRHookStmt(binary.Y) {
			return false
		}
		cd, ok := binary.X.Cmd.(*syntax.CallExpr)
		if !ok || len(cd.Assigns) != 0 || len(cd.Args) != 2 || cd.Args[0].Lit() != "cd" {
			return false
		}
		// Avoid `cd -`, which prints a path, and expansions with side effects.
		if cd.Args[1].Lit() == "" || strings.HasPrefix(cd.Args[1].Lit(), "-") {
			return false
		}
		stmt = binary.Y
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	return ok && len(call.Assigns) == 0 && len(call.Args) >= 3 &&
		call.Args[0].Lit() == "gh" && call.Args[1].Lit() == "pr" && call.Args[2].Lit() == "create"
}

func plainPRHookStmt(stmt *syntax.Stmt) bool {
	return !stmt.Negated && !stmt.Background && !stmt.Coprocess && len(stmt.Redirs) == 0
}

func (c *commandContext) registerHookPR(ctx context.Context, agent, event, sessionID, ref string) {
	// Native hooks have a 30s deadline. A slow provider must not hold the
	// agent for the CLI's normal two-minute mutation timeout.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := claimPRRequest{PR: ref, AllowTakeover: false}
	if err := c.postJSON(ctx, "sessions/"+url.PathEscape(sessionID)+"/pr/claim", req, nil); err != nil {
		c.reportHookFailure(agent, event, sessionID, fmt.Errorf("register created PR: %w", err))
	}
}
