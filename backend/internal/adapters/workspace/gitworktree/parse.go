package gitworktree

import (
	"bufio"
	"strings"
)

type worktreeRecord struct {
	Path       string
	Branch     string
	Head       string
	Bare       bool
	Detached   bool
	Locked     bool
	LockReason string
	Prunable   bool
}

func parseWorktreePorcelain(out string) ([]worktreeRecord, error) {
	var records []worktreeRecord
	var cur *worktreeRecord

	flush := func() {
		if cur != nil && cur.Path != "" {
			records = append(records, *cur)
		}
		cur = nil
	}

	s := bufio.NewScanner(strings.NewReader(out))
	for s.Scan() {
		line := strings.TrimRight(s.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, hasValue := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &worktreeRecord{}
			if hasValue {
				cur.Path = val
			}
		case "HEAD":
			if cur != nil && hasValue {
				cur.Head = val
			}
		case "branch":
			if cur != nil && hasValue {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
				if hasValue {
					cur.LockReason = val
				}
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	flush()
	return records, nil
}
