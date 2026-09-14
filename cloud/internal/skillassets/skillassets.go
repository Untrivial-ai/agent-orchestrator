// Package skillassets embeds the cloud using-ao skill (the catalog of the
// in-sandbox `ao` CLI) and installs it into the worker data dir at ao-worker
// boot. The cloud `ao` is a different, smaller CLI than the desktop one
// (control-plane-mediated spawn/list/send/report/kill only), so this content is
// written for the cloud grammar and must never be replaced with the desktop
// skill from backend/internal/skillassets.
//
// The embedded copy is the single source of truth. Install clobbers the
// on-disk copy on every worker boot, so a new ao-worker binary always
// refreshes it and the two can never drift; the binary already is the version.
package skillassets

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"embed"
)

//go:embed using-ao
var files embed.FS

// SkillName is the installed skill's directory name under <dataDir>/skills.
const SkillName = "using-ao"

// Dir returns the absolute directory the skill installs into for a given data
// dir. Callers building prompts use this so the path they cite always matches
// where Install writes.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "skills", SkillName)
}

// Install writes the embedded using-ao skill into <dataDir>/skills/using-ao,
// replacing any existing copy. It runs at ao-worker boot, before the agent
// terminal starts, so a plain clobber-and-write needs no locking. A failure is
// returned but is non-fatal to the worker (the skill enhances the prompts, it
// is not load-bearing).
func Install(dataDir string) error {
	destDir := Dir(dataDir)
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("skillassets.Install: dataDir is required")
	}
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clear skill dir %q: %w", destDir, err)
	}
	// embed.FS always uses forward-slash paths rooted at "using-ao"; strip that
	// prefix and map each entry onto destDir with the platform separator.
	return fs.WalkDir(files, SkillName, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, SkillName), "/")
		target := destDir
		if rel != "" {
			target = filepath.Join(destDir, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %q: %w", p, err)
		}
		if err := os.WriteFile(target, b, 0o600); err != nil {
			return fmt.Errorf("write %q: %w", target, err)
		}
		return nil
	})
}
