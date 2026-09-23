package kimi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Kimi Code shows an interactive "Trust this folder?" dialog before the
// composer on first launch in an untrusted directory. That dialog swallows the
// after-start prompt paste and blocks the readiness marker, so AO seeds Kimi's
// workspace-trust record for the session worktree before launch, the same way
// the Claude Code adapter pre-records trust in ~/.claude.json.
//
// Trust record layout (verified against Kimi Code 0.34.0): trust is a document
// named by the workdir key under $KIMI_CODE_HOME/workspace-trust/:
//
//	workdir key = "wd_" + slug(basename(root)) + "_" + sha256hex(root)[:12]
//	content     = {"root": "<abs path>", "trustedAt": <unix ms>}
//
// where root is the workspace path with backslashes converted to slashes and
// trailing slashes stripped. Kimi treats the workspace as trusted whenever the
// document exists.

const (
	kimiTrustDirName       = "workspace-trust"
	kimiWorkdirKeyPrefix   = "wd_"
	kimiWorkdirHashLength  = 12
	kimiWorkdirSlugMaxLen  = 40
	kimiWorkdirSlugDefault = "workspace"
)

var kimiWorkdirSlugInvalidRE = regexp.MustCompile(`[^a-z0-9._-]+`)

// PreLaunch seeds Kimi's workspace trust record so the interactive trust
// dialog cannot block the managed pane before the first prompt is delivered.
func (p *Plugin) PreLaunch(ctx context.Context, cfg ports.LaunchConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" || strings.TrimSpace(cfg.DataDir) == "" {
		return nil
	}
	return ensureKimiWorkspaceTrusted(kimiCodeHomeDir(cfg.DataDir), cfg.WorkspacePath)
}

// EnsureWorkspaceTrusted seeds Kimi's workspace trust record for
// workspacePath in the Kimi home the current process environment points at
// (KIMI_CODE_HOME, else ~/.kimi-code) rather than AO's isolated home. The
// daemon-owned authentication terminal deliberately runs Kimi against that
// real home so login credentials land where the user's own Kimi — and AO's
// session credential seeding — read them; pre-recording trust there keeps the
// interactive "Trust this folder?" dialog from swallowing the login flow in
// AO's private auth workspace.
func EnsureWorkspaceTrusted(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return nil
	}
	home, ok := kimiCodeHome()
	if !ok {
		return nil
	}
	return ensureKimiWorkspaceTrusted(home, workspacePath)
}

// kimiWorkspaceTrust is the on-disk shape Kimi writes when a user trusts a
// folder. AO writes the same shape so Kimi cannot distinguish seeded trust
// from a user's own decision.
type kimiWorkspaceTrust struct {
	Root      string `json:"root"`
	TrustedAt int64  `json:"trustedAt"`
}

func ensureKimiWorkspaceTrusted(kimiHome, workspacePath string) error {
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("kimi: resolve workspace: %w", err)
	}
	trustPath := filepath.Join(kimiHome, kimiTrustDirName, kimiWorkdirKey(absWorkspace))
	if _, err := os.Stat(trustPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("kimi: stat %s: %w", trustPath, err)
	}

	trust := kimiWorkspaceTrust{Root: absWorkspace, TrustedAt: time.Now().UnixMilli()}
	data, err := json.Marshal(trust)
	if err != nil {
		return fmt.Errorf("kimi: encode trust record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(trustPath), 0o750); err != nil {
		return fmt.Errorf("kimi: create trust dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(trustPath, data, 0o600); err != nil {
		return fmt.Errorf("kimi: write %s: %w", trustPath, err)
	}
	return nil
}

// kimiWorkdirKey mirrors Kimi Code's encodeWorkDirKey: backslashes become
// slashes, trailing slashes are stripped, and the key is "wd_" plus a slug of
// the last path segment plus the first 12 hex chars of the path's SHA-256.
func kimiWorkdirKey(workspacePath string) string {
	normalized := strings.TrimRight(strings.ReplaceAll(workspacePath, `\`, "/"), "/")
	base := normalized
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		base = normalized[idx+1:]
	}
	sum := sha256.Sum256([]byte(normalized))
	return kimiWorkdirKeyPrefix + kimiWorkdirSlug(base) + "_" + hex.EncodeToString(sum[:])[:kimiWorkdirHashLength]
}

// kimiWorkdirSlug mirrors Kimi Code's slugifyWorkDirName: lowercase, collapse
// runs of characters outside [a-z0-9._-] to "-", trim leading/trailing "-",
// cap at 40 chars, trim again, and fall back to "workspace" when nothing
// usable remains.
func kimiWorkdirSlug(name string) string {
	slug := kimiWorkdirSlugInvalidRE.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > kimiWorkdirSlugMaxLen {
		slug = strings.Trim(slug[:kimiWorkdirSlugMaxLen], "-")
	}
	if slug == "" || slug == "." || slug == ".." {
		return kimiWorkdirSlugDefault
	}
	return slug
}
