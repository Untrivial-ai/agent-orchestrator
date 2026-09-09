// Package skillassets embeds AO's agent skills and installs them into the AO
// data dir at daemon boot. Worker sessions run in a
// worktree of whatever project they were spawned in, so a repo-relative
// skills/ path only resolves when that project happens to be the AO repo
// itself. Installing under the data dir gives every session, in any project, a
// stable absolute path to read.
//
// The embedded copy is the single source of truth. Install clobbers the
// on-disk copy on every boot, so a new daemon build always refreshes it and the
// two can never drift; there is no version marker or hash to keep in sync
// because the daemon binary already is the version.
//
// Materialize and MaterializeBrowser write an embedded tree into an arbitrary
// destination directory (used by the opencode adapter to place each skill
// where opencode discovers it under .opencode/skills/).
package skillassets

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"embed"
)

//go:embed using-ao ao-browser
var files embed.FS

const (
	// SkillName is the CLI catalog's directory name under <dataDir>/skills.
	SkillName = "using-ao"
	// BrowserSkillName is the browser skill's directory name under <dataDir>/skills.
	BrowserSkillName = "ao-browser"
)

// Dir returns the absolute directory the skill installs into for a given data
// dir. Callers building prompts use this so the path they cite always matches
// where Install writes.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "skills", SkillName)
}

// BrowserDir returns the absolute browser-skill directory for a data dir.
func BrowserDir(dataDir string) string {
	return filepath.Join(dataDir, "skills", BrowserSkillName)
}

// Install writes the embedded skills into <dataDir>/skills, replacing existing
// AO-owned copies. It runs once at daemon boot, before any session spawns, so a
// plain clobber-and-write needs no locking. A failure is non-fatal to boot.
func Install(dataDir string) error {
	if err := Materialize(Dir(dataDir)); err != nil {
		return err
	}
	return MaterializeBrowser(BrowserDir(dataDir))
}

// Materialize writes the embedded using-ao skill into destDir (the skill root
// itself, e.g. <dataDir>/skills/using-ao or <workspace>/.opencode/skills/using-ao),
// replacing any existing copy. Callers that need AO-ownership guards must apply
// them before calling Materialize.
func Materialize(destDir string) error {
	return materialize(SkillName, destDir)
}

// MaterializeBrowser writes the embedded ao-browser skill into destDir.
func MaterializeBrowser(destDir string) error {
	return materialize(BrowserSkillName, destDir)
}

func materialize(skillName, destDir string) error {
	if strings.TrimSpace(destDir) == "" {
		return fmt.Errorf("skillassets: destDir is required")
	}
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clear skill dir %q: %w", destDir, err)
	}
	// embed.FS uses forward-slash paths rooted at the skill name; strip that
	// prefix and map each entry onto destDir with the platform separator.
	return fs.WalkDir(files, skillName, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, skillName)
		rel = strings.TrimPrefix(rel, "/")
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
