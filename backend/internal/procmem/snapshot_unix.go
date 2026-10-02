//go:build !windows

package procmem

import (
	"context"
	"fmt"
	"os/exec"
)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Snapshot reads the current process table by shelling out to `ps`. A nil
// runner uses exec.
func Snapshot(ctx context.Context, run Runner) (*Table, error) {
	if run == nil {
		run = execRunner
	}
	// rss= is KiB and time= is cumulative CPU time on both Linux and macOS ps.
	// args= is the full command line, so a child reads as "sh -c go test ./..."
	// rather than "sh"; the window shows what each process is doing. macOS
	// truncates args= to the terminal width unless told otherwise, which with
	// no terminal at all is unhelpfully short; -ww lifts the cap and is a
	// silent no-op on Linux.
	out, err := run(ctx, "ps", "-axww", "-o", "pid=,ppid=,rss=,time=,args=")
	if err != nil {
		return nil, fmt.Errorf("procmem: ps: %w", err)
	}
	return Parse(string(out))
}
