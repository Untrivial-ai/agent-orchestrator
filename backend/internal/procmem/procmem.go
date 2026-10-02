// Package procmem measures the resident memory of a process tree. It takes one
// snapshot of the whole process table so many sessions can be costed from a
// single `ps` run, then walks each root's descendants.
package procmem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// ErrUnsupported is returned where the process table cannot be read.
var ErrUnsupported = errors.New("procmem: process memory is not supported on " + runtime.GOOS)

// Runner executes a command and returns its combined output. It matches the
// shape the runtime adapters already inject for tests. Declared here, rather
// than beside the one platform that shells out, because Snapshot's exported
// signature must be identical on every platform.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Process is one row of the process table.
type Process struct {
	PID      int
	PPID     int
	RSSBytes uint64
	// CPUSeconds is the process's total user+system CPU time so far. Two
	// snapshots apart give a rate; one alone says nothing about now.
	CPUSeconds float64
	Command    string
}

// Tree is the memory reading for one root and all of its descendants.
type Tree struct {
	RSSBytes   uint64
	CPUSeconds float64
	Processes  []Process
}

// Table is a snapshot of every process, indexed for tree walks.
type Table struct {
	byPID    map[int]Process
	children map[int][]int
}

// maxCommandLen caps a stored command line. Long node and electron argv
// lists run to kilobytes; nobody reads past the first couple of hundred.
const maxCommandLen = 200

// Parse builds a Table from `ps -axo pid=,ppid=,rss=,time=,args=` output.
// The time column is optional so a table without it still parses, and a
// bare comm= column is just a one-word command line.
func Parse(out string) (*Table, error) {
	t := &Table{byPID: map[int]Process{}, children: map[int][]int{}}
	sc := bufio.NewScanner(strings.NewReader(out))
	// A long args= column (electron/node argv) can exceed the scanner's
	// default 64 KiB token limit; grow it rather than silently truncate.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		rssKiB, err3 := strconv.ParseUint(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("procmem: malformed ps row %q", sc.Text())
		}
		p := Process{PID: pid, PPID: ppid, RSSBytes: rssKiB * 1024}
		rest := fields[3:]
		if len(rest) > 0 {
			if secs, ok := parseCPUTime(rest[0]); ok {
				p.CPUSeconds = secs
				rest = rest[1:]
			}
		}
		p.Command = strings.Join(rest, " ")
		if len(p.Command) > maxCommandLen {
			p.Command = p.Command[:maxCommandLen]
		}
		// A zombie has exited and holds no memory; it only waits for its
		// parent to collect the exit code. Listing it reads as a leak.
		if strings.Contains(p.Command, "<defunct>") {
			continue
		}
		t.byPID[pid] = p
		t.children[ppid] = append(t.children[ppid], pid)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("procmem: scan ps output: %w", err)
	}
	return t, nil
}

// All returns every process in the table, zombies already excluded by Parse.
// Used where a reading needs the whole machine rather than one root's tree,
// such as deriving host CPU on a platform with no system-wide ticks of its own.
func (t *Table) All() []Process {
	out := make([]Process, 0, len(t.byPID))
	for _, p := range t.byPID {
		out = append(out, p)
	}
	return out
}

// Tree sums the root and every descendant. A root that no longer exists
// yields an empty tree. Each pid is counted once even if several roots share
// descendants.
func (t *Table) Tree(roots ...int) Tree {
	var tree Tree
	seen := map[int]bool{}
	var walk func(pid int)
	walk = func(pid int) {
		if seen[pid] {
			return
		}
		p, ok := t.byPID[pid]
		if !ok {
			return
		}
		seen[pid] = true
		tree.RSSBytes += p.RSSBytes
		tree.CPUSeconds += p.CPUSeconds
		tree.Processes = append(tree.Processes, p)
		for _, child := range t.children[pid] {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return tree
}

// parseCPUTime reads ps's TIME column: [[dd-]hh:]mm:ss[.cc] on both Linux
// and macOS. Anything else (a command name, say) is reported as not a time.
func parseCPUTime(s string) (float64, bool) {
	days := 0.0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = float64(n), rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var secs float64
	for _, part := range parts {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return days*86400 + secs, true
}

// CPUPercent is the share of one core a set of processes used between two
// snapshots: the growth in their CPU time over the wall-clock gap. Processes
// absent from prev started inside the gap, so their whole CPU time counts.
// Without a previous snapshot there is no rate, only a lifetime total, so the
// answer is zero rather than a misleading spike.
func CPUPercent(procs []Process, prev *Table, elapsed float64) float64 {
	if prev == nil || elapsed <= 0 {
		return 0
	}
	var delta float64
	for _, p := range procs {
		before := 0.0
		if q, ok := prev.byPID[p.PID]; ok && q.CPUSeconds <= p.CPUSeconds {
			before = q.CPUSeconds
		}
		delta += p.CPUSeconds - before
	}
	return delta / elapsed * 100
}
