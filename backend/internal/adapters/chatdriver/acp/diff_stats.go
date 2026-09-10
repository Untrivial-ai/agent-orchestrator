package acp

import (
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func lineDelta(oldText, newText string) (additions, deletions int) {
	if oldText == newText {
		return 0, 0
	}
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	if len(oldLines) == 0 {
		return len(newLines), 0
	}
	if len(newLines) == 0 {
		return 0, len(oldLines)
	}
	lcs := longestCommonSubsequenceLen(oldLines, newLines)
	return len(newLines) - lcs, len(oldLines) - lcs
}

func splitLines(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	if strings.HasSuffix(value, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func longestCommonSubsequenceLen(a, b []string) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
			} else if prev[j] >= curr[j-1] {
				curr[j] = prev[j]
			} else {
				curr[j] = curr[j-1]
			}
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func diffFileFromSnapshot(path string, oldText *string, newText string) ports.ChatDiffFile {
	old := ""
	status := "modified"
	if oldText == nil {
		status = "added"
	} else {
		old = *oldText
		if newText == "" {
			status = "deleted"
		}
	}
	additions, deletions := lineDelta(old, newText)
	return ports.ChatDiffFile{Path: path, Status: status, Additions: additions, Deletions: deletions}
}

type turnDiffAccumulator struct {
	toolOrder []string
	byTool    map[string][]ports.ChatDiffFile
}

func (a *turnDiffAccumulator) replaceTool(toolID string, files []ports.ChatDiffFile) {
	if a.byTool == nil {
		a.byTool = make(map[string][]ports.ChatDiffFile)
	}
	if _, exists := a.byTool[toolID]; !exists {
		a.toolOrder = append(a.toolOrder, toolID)
	}
	a.byTool[toolID] = files
}

func (a *turnDiffAccumulator) aggregate() []ports.ChatDiffFile {
	if len(a.byTool) == 0 {
		return nil
	}
	type aggregate struct {
		path                 string
		status               string
		additions, deletions int
	}
	byPath := make(map[string]*aggregate)
	var pathOrder []string
	for _, toolID := range a.toolOrder {
		for _, file := range a.byTool[toolID] {
			current := byPath[file.Path]
			if current == nil {
				current = &aggregate{path: file.Path}
				byPath[file.Path] = current
				pathOrder = append(pathOrder, file.Path)
			}
			current.additions += file.Additions
			current.deletions += file.Deletions
			current.status = file.Status
		}
	}
	files := make([]ports.ChatDiffFile, 0, len(pathOrder))
	for _, path := range pathOrder {
		current := byPath[path]
		files = append(files, ports.ChatDiffFile{
			Path: path, Status: current.status,
			Additions: current.additions, Deletions: current.deletions,
		})
	}
	return files
}
