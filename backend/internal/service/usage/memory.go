package usage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

type memorySessionStore interface {
	ListSessions(context.Context, domain.ProjectID) ([]domain.SessionRecord, error)
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
}

// MemoryReaderDeps wires the memory reader. Snapshot defaults to a real `ps`
// run; tests inject a parsed table.
type MemoryReaderDeps struct {
	Store   memorySessionStore
	Runtime ports.RuntimeProcessRootInspector
	// ChatHostPID names the provider host of a runtime-less Chat session.
	// Nil means Chat sessions are not measured.
	ChatHostPID func(sessionID domain.SessionID) (int, bool)
	Snapshot    func(context.Context) (*procmem.Table, error)
	Now         func() time.Time
	// AppRootPIDs names AO's own processes (daemon, desktop shell). Nil means
	// AppMemory counts sessions only.
	AppRootPIDs func() []int
	// CacheTTL bounds how often the process table is re-read while several
	// clients poll. Zero disables caching.
	CacheTTL time.Duration
}

// MemoryReader samples resident memory for every live session's runtime.
type MemoryReader struct {
	deps MemoryReaderDeps

	mu       sync.Mutex
	cached   *procmem.Table
	cachedAt time.Time
	// prev is the snapshot before cached, kept so CPU is a rate between two
	// samples rather than a lifetime average.
	prev   *procmem.Table
	prevAt time.Time
	// lastSystem is the previous host reading, for the swap rate.
	lastSystem   procmem.System
	lastSystemAt time.Time
	ReadSystem   func() (procmem.System, error)
}

// NewMemoryReader constructs a memory reader.
func NewMemoryReader(deps MemoryReaderDeps) *MemoryReader {
	if deps.Snapshot == nil {
		deps.Snapshot = func(ctx context.Context) (*procmem.Table, error) { return procmem.Snapshot(ctx, nil) }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &MemoryReader{deps: deps, ReadSystem: procmem.ReadSystem}
}

// ListMemory returns one reading per live session that has a runtime. Sessions
// that are terminated, have no runtime handle, or whose runtime cannot name a
// root pid are omitted rather than reported as zero.
func (r *MemoryReader) ListMemory(ctx context.Context, projectID domain.ProjectID) ([]domain.SessionMemory, error) {
	if r == nil || r.deps.Store == nil || r.deps.Runtime == nil {
		return nil, fmt.Errorf("session memory reader is unavailable")
	}
	var (
		recs []domain.SessionRecord
		err  error
	)
	if projectID == "" {
		recs, err = r.deps.Store.ListAllSessions(ctx)
	} else {
		recs, err = r.deps.Store.ListSessions(ctx, projectID)
	}
	if err != nil {
		return nil, err
	}
	table, prev, elapsed, err := r.table(ctx)
	if err != nil {
		return nil, err
	}
	sampledAt := r.deps.Now()
	out := make([]domain.SessionMemory, 0, len(recs))
	for _, rec := range recs {
		if rec.IsTerminated {
			continue
		}
		roots := r.rootPIDs(ctx, rec)
		if len(roots) == 0 {
			continue
		}
		tree := table.Tree(roots...)
		if len(tree.Processes) == 0 {
			continue
		}
		reading := treeReading(tree, prev, elapsed)
		reading.SessionID, reading.SampledAt = rec.ID, sampledAt
		out = append(out, reading)
	}
	return out, nil
}

// treeReading costs one process tree against the previous snapshot.
func treeReading(tree procmem.Tree, prev *procmem.Table, elapsed float64) domain.SessionMemory {
	procs := make([]domain.SessionMemoryProcess, 0, len(tree.Processes))
	for _, p := range tree.Processes {
		procs = append(procs, domain.SessionMemoryProcess{
			PID: p.PID, PPID: p.PPID, RSSBytes: p.RSSBytes, Command: p.Command,
			CPUPercent: procmem.CPUPercent([]procmem.Process{p}, prev, elapsed),
		})
	}
	return domain.SessionMemory{
		RSSBytes: tree.RSSBytes, ProcessCount: len(procs),
		CPUPercent: procmem.CPUPercent(tree.Processes, prev, elapsed), Processes: procs,
	}
}

// SystemMemory reports the host's headroom: RAM, swap, swapping rate since
// the last call, and load. ErrUnsupported on platforms procmem can't read.
func (r *MemoryReader) SystemMemory(ctx context.Context) (domain.SystemMemory, error) {
	sys, err := r.ReadSystem()
	if err != nil {
		return domain.SystemMemory{}, err
	}
	now := r.deps.Now()
	r.mu.Lock()
	last, lastAt := r.lastSystem, r.lastSystemAt
	r.lastSystem, r.lastSystemAt = sys, now
	r.mu.Unlock()
	out := domain.SystemMemory{
		TotalBytes: sys.TotalBytes, AvailableBytes: sys.AvailableBytes,
		SwapTotalBytes: sys.SwapTotalBytes, SwapUsedBytes: sys.SwapUsedBytes,
		CPUCount: sys.CPUCount, Load1: sys.Load1,
		PressureRaw: sys.PressureRaw, PressureSource: sys.PressureSource,
	}
	if gap := now.Sub(lastAt).Seconds(); !lastAt.IsZero() && gap > 0 && sys.SwapPages >= last.SwapPages {
		out.SwapBytesPerSec = float64(sys.SwapPages-last.SwapPages) * float64(sys.SwapPageBytes) / gap
	}
	switch {
	case !lastAt.IsZero() && sys.CPUTotalTicks > last.CPUTotalTicks && sys.CPUBusyTicks >= last.CPUBusyTicks:
		out.CPUPercent = 100 * float64(sys.CPUBusyTicks-last.CPUBusyTicks) / float64(sys.CPUTotalTicks-last.CPUTotalTicks)
	case sys.CPUTotalTicks == 0:
		// No system-wide ticks on this platform (macOS today). The daemon
		// already samples every process for the session rows, so the same
		// table gives the machine's busy share: total CPU-seconds used by
		// everything, divided by elapsed time and core count. This slightly
		// undercounts processes that start and exit between two samples,
		// which a whole-machine tick counter would not miss.
		if table, prev, elapsed, err := r.table(ctx); err == nil && sys.CPUCount > 0 {
			out.CPUPercent = min(100, procmem.CPUPercent(table.All(), prev, elapsed)/float64(sys.CPUCount))
		}
	}
	return out, nil
}

// cpuRateMaxGap is the longest gap between samples a CPU rate is trusted over.
const cpuRateMaxGap = 60 * time.Second

// AppMemory sums AO's own processes and every live session tree. Roots are
// deduplicated by Table.Tree, so a session that happens to be a daemon
// descendant is counted once.
func (r *MemoryReader) AppMemory(ctx context.Context) (domain.AppMemory, error) {
	if r == nil || r.deps.Store == nil || r.deps.Runtime == nil {
		return domain.AppMemory{}, fmt.Errorf("session memory reader is unavailable")
	}
	recs, err := r.deps.Store.ListAllSessions(ctx)
	if err != nil {
		return domain.AppMemory{}, err
	}
	table, prev, elapsed, err := r.table(ctx)
	if err != nil {
		return domain.AppMemory{}, err
	}
	var own []int
	if r.deps.AppRootPIDs != nil {
		own = append(own, r.deps.AppRootPIDs()...)
	}
	roots := append([]int(nil), own...)
	inSession := map[int]bool{}
	for _, rec := range recs {
		if rec.IsTerminated {
			continue
		}
		sessionRoots := r.rootPIDs(ctx, rec)
		roots = append(roots, sessionRoots...)
		for _, p := range table.Tree(sessionRoots...).Processes {
			inSession[p.PID] = true
		}
	}
	tree := table.Tree(roots...)
	// The daemon starts tmux, so every session is a descendant of the daemon
	// and a plain walk from its pid would count them as AO's own. Own is the
	// daemon tree with the session trees cut out, so the rows stay disjoint
	// and add up to the total.
	ownTree := table.Tree(own...)
	ownOnly := procmem.Tree{}
	for _, p := range ownTree.Processes {
		if inSession[p.PID] {
			continue
		}
		ownOnly.RSSBytes += p.RSSBytes
		ownOnly.CPUSeconds += p.CPUSeconds
		ownOnly.Processes = append(ownOnly.Processes, p)
	}
	ownReading := treeReading(ownOnly, prev, elapsed)
	ownReading.SampledAt = r.deps.Now()
	return domain.AppMemory{
		RSSBytes: tree.RSSBytes, ProcessCount: len(tree.Processes),
		CPUPercent: procmem.CPUPercent(tree.Processes, prev, elapsed), Own: ownReading,
	}, nil
}

// rootPIDs names where a session's process tree starts: the runtime handle
// for terminal sessions, the provider host for Chat sessions. Probe failures
// yield no roots so the session is omitted rather than reported as zero.
func (r *MemoryReader) rootPIDs(ctx context.Context, rec domain.SessionRecord) []int {
	if rec.Mode == domain.SessionModeChat {
		if r.deps.ChatHostPID == nil {
			return nil
		}
		pid, ok := r.deps.ChatHostPID(rec.ID)
		if !ok {
			return nil
		}
		return []int{pid}
	}
	if rec.Metadata.RuntimeHandleID == "" {
		return nil
	}
	roots, err := r.deps.Runtime.ProcessRootPIDs(ctx, ports.RuntimeHandle{ID: rec.Metadata.RuntimeHandleID})
	if err != nil {
		return nil
	}
	return roots
}

// table returns the current snapshot, the one before it, and the seconds
// between them, so callers can turn CPU time into a rate.
func (r *MemoryReader) table(ctx context.Context) (cur, prev *procmem.Table, elapsed float64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.deps.Now()
	if r.cached == nil || r.deps.CacheTTL == 0 || now.Sub(r.cachedAt) >= r.deps.CacheTTL {
		table, err := r.deps.Snapshot(ctx)
		if err != nil {
			if errors.Is(err, procmem.ErrUnsupported) {
				return nil, nil, 0, err
			}
			return nil, nil, 0, fmt.Errorf("sample process memory: %w", err)
		}
		r.prev, r.prevAt = r.cached, r.cachedAt
		r.cached, r.cachedAt = table, now
	}
	// Across a sleep the CPU deltas are garbage; a gap over a minute means the
	// first sample after it reports no rate rather than a wrong one.
	if r.prev != nil && r.cachedAt.Sub(r.prevAt) <= cpuRateMaxGap {
		elapsed = r.cachedAt.Sub(r.prevAt).Seconds()
	}
	return r.cached, r.prev, elapsed, nil
}
