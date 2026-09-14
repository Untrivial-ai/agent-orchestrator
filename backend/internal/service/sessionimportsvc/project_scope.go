package sessionimportsvc

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

// projectScope recognizes external worktrees using local Git administrative
// files. It never invokes Git, probes a remote, or follows a source tree to
// modify it. Cache per request because many conversations share one directory.
func projectScope(projects []projectsvc.Summary, wanted domain.ProjectID) func(string) bool {
	byCommon := map[string]domain.ProjectID{}
	for _, p := range projects {
		if common := gitCommonDir(p.Path); common != "" {
			if _, ok := byCommon[common]; !ok {
				byCommon[common] = p.ID
			}
		}
	}
	var mu sync.Mutex
	matches := map[string]bool{}
	return func(cwd string) bool {
		mu.Lock()
		defer mu.Unlock()
		if value, ok := matches[cwd]; ok {
			return value
		}
		id, ok := bestProjectForDir(projects, cwd)
		if !ok {
			id, ok = byCommon[gitCommonDir(cwd)]
		}
		value := ok && id == wanted
		matches[cwd] = value
		return value
	}
}

func gitCommonDir(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if !info.IsDir() {
				raw := readGitPointer(gitPath)
				if !strings.HasPrefix(raw, "gitdir:") {
					return ""
				}
				gitPath = strings.TrimSpace(strings.TrimPrefix(raw, "gitdir:"))
				if !filepath.IsAbs(gitPath) {
					gitPath = filepath.Join(dir, gitPath)
				}
			}
			if common := readGitPointer(filepath.Join(gitPath, "commondir")); common != "" {
				if filepath.IsAbs(common) {
					gitPath = common
				} else {
					gitPath = filepath.Join(gitPath, common)
				}
			}
			resolved, err := filepath.EvalSymlinks(gitPath)
			if err != nil {
				return ""
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func readGitPointer(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return ""
	}
	return strings.TrimSpace(string(data))
}
