package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ClaudeProfile identifies existing user-owned configuration, never credentials.
type ClaudeProfile struct {
	Name      string `json:"name"`
	ConfigDir string `json:"configDir"`
}

// ClaudeProfiles is the display-safe catalog of user-owned Claude profiles.
type ClaudeProfiles struct {
	Profiles []ClaudeProfile `json:"profiles"`
}

// ClaudeProfiles discovers conventional directories without executing shell
// profiles, reading credential contents, or creating any provider-owned state.
func (s *Service) ClaudeProfiles(ctx context.Context) (ClaudeProfiles, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeProfiles{}, err
	}
	return discoverClaudeProfiles(ctx, home, os.Getenv("CLAUDE_CONFIG_DIR"))
}

func discoverClaudeProfiles(ctx context.Context, home, configured string) (ClaudeProfiles, error) {
	result := ClaudeProfiles{Profiles: []ClaudeProfile{}}
	seen := map[string]bool{}
	add := func(name, path string, explicit bool) {
		if !filepath.IsAbs(path) {
			return
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || seen[resolved] {
			return
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return
		}
		if !explicit {
			initialized := false
			for _, marker := range []string{"settings.json", ".claude.json", ".credentials.json", "projects"} {
				if _, err := os.Stat(filepath.Join(resolved, marker)); err == nil {
					initialized = true
					break
				}
			}
			if !initialized {
				return
			}
		}
		seen[resolved] = true
		result.Profiles = append(result.Profiles, ClaudeProfile{Name: name, ConfigDir: filepath.Clean(path)})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return result, err
	}
	// Real named directories take precedence over aliases, including ~/.claude
	// pointing at a named profile. A real standard-home directory in turn wins
	// over named symlink aliases pointing back to it.
	addNamed := func(entry os.DirEntry) {
		if entry.Name() == ".claude" || !strings.HasPrefix(entry.Name(), ".claude") {
			return
		}
		name := strings.TrimLeft(strings.TrimPrefix(entry.Name(), ".claude"), "-_. ")
		if name == "" {
			name = entry.Name()
		}
		add(name, filepath.Join(home, entry.Name()), false)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			addNamed(entry)
		}
	}
	beforeDefault := len(result.Profiles)
	add("Default", filepath.Join(home, ".claude"), true)
	if len(result.Profiles) > beforeDefault {
		result.Profiles[beforeDefault].ConfigDir = ""
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			addNamed(entry)
		}
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		add("Environment ("+filepath.Base(configured)+")", configured, true)
	}
	sort.SliceStable(result.Profiles, func(i, j int) bool {
		if result.Profiles[i].ConfigDir == "" || result.Profiles[j].ConfigDir == "" {
			return result.Profiles[i].ConfigDir == ""
		}
		return result.Profiles[i].Name < result.Profiles[j].Name
	})
	return result, nil
}
