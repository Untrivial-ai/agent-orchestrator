package cursor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	cursorDataDirEnv     = "CURSOR_DATA_DIR"
	cursorConfigFileName = "cli-config.json"
)

var reviewerAllowedPermissions = []string{
	"Read(**)",
	"Shell(git)",
	"Shell(gh)",
	"Shell(ao)",
	"Shell(printf)",
}

var reviewerDeniedPermissions = []string{
	"Write(**)",
	"Shell(rm)",
	"Shell(mv)",
	"Shell(cp)",
}

type reviewerConfig struct {
	Version     int                 `json:"version"`
	AuthInfo    map[string]any      `json:"authInfo,omitempty"`
	Permissions reviewerPermissions `json:"permissions"`
}

type reviewerPermissions struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

func reviewerEnv(inv ports.ReviewInvocation) map[string]string {
	profileDir := reviewerProfileDir(inv)
	if profileDir == "" {
		return nil
	}
	return map[string]string{cursorDataDirEnv: profileDir}
}

func reviewerProfileDir(inv ports.ReviewInvocation) string {
	dataDir := strings.TrimSpace(inv.DataDir)
	if dataDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(inv.ReviewerID))
	return filepath.Join(dataDir, "cursor-reviewers", hex.EncodeToString(sum[:8]))
}

func installReviewerConfig(ctx context.Context, inv ports.ReviewInvocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	profileDir := reviewerProfileDir(inv)
	if profileDir == "" {
		return errors.New("cursor reviewer: AO data directory is required")
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("cursor reviewer: create profile: %w", err)
	}

	authInfo, err := hostCursorAuthInfo()
	if err != nil {
		return fmt.Errorf("cursor reviewer: read host auth info: %w", err)
	}
	config := reviewerConfig{
		Version:  1,
		AuthInfo: authInfo,
		Permissions: reviewerPermissions{
			Allow: reviewerAllowList(inv.TaskPromptRoot),
			Deny:  append([]string(nil), reviewerDeniedPermissions...),
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("cursor reviewer: encode configuration: %w", err)
	}
	data = append(data, '\n')
	configPath := filepath.Join(profileDir, cursorConfigFileName)
	if err := hookutil.AtomicWriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("cursor reviewer: write configuration: %w", err)
	}
	return nil
}

func hostCursorAuthInfo() (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err == nil {
		home = strings.TrimSpace(home)
	}
	if home == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".cursor", cursorConfigFileName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, nil
	}
	type cursorConfig struct {
		AuthInfo map[string]any `json:"authInfo"`
	}
	var configs []cursorConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		var config cursorConfig
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, err
		}
		configs = []cursorConfig{config}
	}
	for _, config := range configs {
		if cursorAuthInfoHasIdentity(config.AuthInfo) {
			return config.AuthInfo, nil
		}
	}
	return nil, nil
}

func cursorAuthInfoHasIdentity(info map[string]any) bool {
	if len(info) == 0 {
		return false
	}
	for _, key := range []string{"userId", "authId", "email", "displayName"} {
		value, ok := info[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return true
			}
		case nil:
		default:
			return true
		}
	}
	return false
}

func reviewerAllowList(taskPromptRoot string) []string {
	allow := append([]string(nil), reviewerAllowedPermissions...)
	if root := strings.TrimSpace(taskPromptRoot); root != "" {
		allow = append(allow, "Read("+filepath.ToSlash(filepath.Join(root, "**"))+")")
	}
	return allow
}

// applyReviewerMCPDisable records every MCP server Cursor would load for this
// review as disabled inside the isolated reviewer profile. Cursor only asks
// about MCP approval interactively, and a reviewer pane is unattended, so an
// unanswered approval screen would stall the review forever. The servers are
// also never spawned during review, which keeps checkout-controlled MCP
// processes out of the reviewer.
func applyReviewerMCPDisable(ctx context.Context, inv ports.ReviewInvocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := reviewerMCPConfigIDs(inv.WorkspacePath)
	if err != nil {
		return err
	}
	// Validate here, ahead of the disableMCPServers seam, so a hostile id
	// fails the review closed even where process execution is substituted.
	// execMCPServerDisable re-checks at the spawn boundary as defense in
	// depth.
	if err := validateMCPServerIDs(ids); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return disableMCPServers(ctx, inv, ids)
}

// disableMCPServers runs `cursor-agent mcp disable` for each declared server
// id against the AO-owned reviewer profile. It is a variable so tests can
// stub process execution.
var disableMCPServers = execMCPServerDisable

func execMCPServerDisable(ctx context.Context, inv ports.ReviewInvocation, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reject before resolving the binary or spawning anything: an
	// option-shaped id must never reach the CLI, where `-h`/`--help` would
	// exit 0 having disabled nothing and silently leave the server enabled.
	if err := validateMCPServerIDs(ids); err != nil {
		return err
	}
	profileDir := reviewerProfileDir(inv)
	if profileDir == "" {
		return errors.New("cursor reviewer: AO data directory is required")
	}
	if strings.TrimSpace(inv.WorkspacePath) == "" {
		return errors.New("cursor reviewer: workspace path is required to disable MCP servers")
	}
	binary, err := workeragent.ResolveCursorBinary(ctx)
	if err != nil {
		return fmt.Errorf("cursor reviewer: resolve cursor binary: %w", err)
	}
	for _, id := range ids {
		cmd := mcpDisableCommand(ctx, binary, id, inv.WorkspacePath, profileDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			detail := strings.Join(strings.Fields(string(out)), " ")
			if detail != "" {
				return fmt.Errorf("cursor reviewer: disable MCP server %q: %w: %s", id, err, detail)
			}
			return fmt.Errorf("cursor reviewer: disable MCP server %q: %w", id, err)
		}
	}
	return nil
}

// validateMCPServerIDs rejects server ids that cannot be passed to
// `cursor-agent mcp disable` unambiguously as a positional argument. Ids
// beginning with `-` would be parsed as CLI options instead of the server id:
// `-h`/`--help` exit 0 having disabled nothing (a silent bypass that leaves
// the server enabled to stall the unattended review on approval), while other
// option-shaped ids fail the disable outright. End-of-options `--` does not
// help — verified against cursor-agent 2026.09.10, `mcp disable --
// "--evil-flag"` still errors with `unknown option '--evil-flag'`. Rejecting
// fails the review closed so the operator sees the misdeclared server instead
// of a pane stuck forever on an approval screen. Empty ids cannot name a
// server and are rejected for the same reason.
func validateMCPServerIDs(ids []string) error {
	for _, id := range ids {
		switch {
		case strings.TrimSpace(id) == "":
			return fmt.Errorf("cursor reviewer: refuse to disable MCP server %q: empty identifiers cannot name a server; remove it from .cursor/mcp.json to review this workspace", id)
		case strings.HasPrefix(id, "-"):
			return fmt.Errorf("cursor reviewer: refuse to disable MCP server %q: identifiers starting with '-' would be parsed as cursor-agent options instead of the server id; rename or remove it from .cursor/mcp.json to review this workspace", id)
		}
	}
	return nil
}

// mcpDisableCommand builds the Cursor CLI invocation that records one server
// id as disabled in the reviewer profile for the review workspace. The
// workspace working directory makes Cursor write the disable list under the
// same project slug the reviewer pane will read. The id is passed bare (no
// `--` separator): ids that would need one are rejected by
// validateMCPServerIDs because this CLI does not honor end-of-options for
// them, and everything reaching this point is a safe positional argument.
func mcpDisableCommand(ctx context.Context, binary, id, workspacePath, profileDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, "mcp", "disable", id) //nolint:gosec // binary is adapter-resolved, args are static.
	cmd.Dir = workspacePath
	prefix := cursorDataDirEnv + "="
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), strings.ToUpper(prefix)) {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, prefix+profileDir)
	cmd.Env = env
	return cmd
}

// reviewerMCPConfigIDs lists the MCP server ids Cursor would load for a
// review: the workspace's .cursor/mcp.json and the host user's
// .cursor/mcp.json, the two locations the Cursor CLI reads. Ids within each
// file are sorted and de-duplicated so the disable order is stable.
func reviewerMCPConfigIDs(workspacePath string) ([]string, error) {
	paths := make([]string, 0, 2)
	if strings.TrimSpace(workspacePath) != "" {
		paths = append(paths, filepath.Join(workspacePath, ".cursor", "mcp.json"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".cursor", "mcp.json"))
	}
	var ids []string
	seen := make(map[string]struct{})
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cursor reviewer: read MCP config %s: %w", path, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			continue
		}
		var config struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("cursor reviewer: parse MCP config %s: %w", path, err)
		}
		fileIDs := make([]string, 0, len(config.MCPServers))
		for id := range config.MCPServers {
			fileIDs = append(fileIDs, id)
		}
		sort.Strings(fileIDs)
		for _, id := range fileIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, nil
}
