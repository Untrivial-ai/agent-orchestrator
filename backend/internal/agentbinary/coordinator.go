// Package agentbinary coordinates daemon-wide executable discovery. It never
// changes the daemon environment or runs provider identity checks in the shell
// probe goroutine.
package agentbinary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const snapshotTTL = 5 * time.Minute
const missTTL = time.Minute

var errInvalidated = errors.New("binary resolution invalidated")

// Config supplies immutable raw adapter callbacks and the shared shell probe.
type Config struct {
	Context   context.Context
	Specs     map[domain.AgentHarness]ports.AgentBinarySpec
	Probe     ports.AgentShellProbe
	Now       func() time.Time
	Logger    *slog.Logger
	OnChanged func([]domain.AgentHarness)
}

type fileIdentity struct {
	info   os.FileInfo
	target string
}
type cachedBinary struct {
	resolution ports.AgentBinaryResolution
	identity   fileIdentity
	launch     bool
	err        error
}
type batch struct {
	done       chan struct{}
	generation uint64
}

type missKey struct {
	harness domain.AgentHarness
	purpose ports.BinaryResolvePurpose
}

// Coordinator shares cached discovery and one shell batch across all adapters.
type Coordinator struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	specs         map[domain.AgentHarness]ports.AgentBinarySpec
	names         []string
	probe         ports.AgentShellProbe
	now           func() time.Time
	logger        *slog.Logger
	onChanged     func([]domain.AgentHarness)
	cache         map[domain.AgentHarness]cachedBinary
	misses        map[missKey]time.Time
	snapshot      ports.AgentShellSnapshot
	snapshotAt    time.Time
	snapshotValid bool
	generation    uint64
	running       *batch
	retryAt       time.Time
	failures      int
	lastErr       error
	closed        bool
}

// New constructs a lazy coordinator; no shell starts until fallback is needed.
func New(cfg Config) *Coordinator {
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	c := &Coordinator{ctx: ctx, cancel: cancel, specs: make(map[domain.AgentHarness]ports.AgentBinarySpec), probe: cfg.Probe, now: now, logger: cfg.Logger, onChanged: cfg.OnChanged, cache: make(map[domain.AgentHarness]cachedBinary), misses: make(map[missKey]time.Time)}
	seen := map[string]bool{}
	for h, s := range cfg.Specs {
		s.Names = append([]string(nil), s.Names...)
		c.specs[h] = s
		for _, n := range s.Names {
			if !seen[n] {
				c.names = append(c.names, n)
				seen[n] = true
			}
		}
	}
	sort.Strings(c.names)
	return c
}

// SetOnChanged replaces the callback invoked after a completed shell batch.
func (c *Coordinator) SetOnChanged(fn func([]domain.AgentHarness)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onChanged = fn
}

// ShellPATH returns the last successful shell PATH for child environments only.
func (c *Coordinator) ShellPATH() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot.Path
}

// Invalidate forces a local retry and makes an in-flight shell result stale.
func (c *Coordinator) Invalidate(h domain.AgentHarness) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, h)
	delete(c.misses, missKey{h, ports.BinaryResolvePresence})
	delete(c.misses, missKey{h, ports.BinaryResolveLaunch})
	c.generation++
	c.snapshotValid = false
	c.retryAt = time.Time{}
	c.lastErr = nil
	c.failures = 0
}

// Close cancels shared work and waits at most four seconds for the active probe.
func (c *Coordinator) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	b := c.running
	c.mu.Unlock()
	if b != nil {
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		select {
		case <-b.done:
		case <-timer.C:
		}
	}
}

// Resolve performs local discovery first and shares fallback shell work. Presence
// returns immediately while a probe runs; launch waits under the caller's context.
func (c *Coordinator) Resolve(ctx context.Context, h domain.AgentHarness, purpose ports.BinaryResolvePurpose) (ports.AgentBinaryResolution, error) {
	s, ok := c.specs[h]
	if !ok {
		return ports.AgentBinaryResolution{}, fmt.Errorf("%w: unregistered harness", ports.ErrAgentBinaryNotFound)
	}
	for {
		if err := ctx.Err(); err != nil {
			return ports.AgentBinaryResolution{}, err
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return ports.AgentBinaryResolution{}, context.Canceled
		}
		now := c.now()
		gen := c.generation
		cached, hasCached := c.cache[h]
		missAt, hasMiss := c.misses[missKey{h, purpose}]
		freshMiss := hasMiss && c.snapshotValid && now.Sub(c.snapshotAt) < snapshotTTL && now.Sub(missAt) < missTTL
		c.mu.Unlock()
		if hasCached && now.Sub(cached.resolution.CheckedAt) < snapshotTTL && (purpose == ports.BinaryResolvePresence || cached.launch) {
			if id, err := validate(cached.resolution.Executable); err == nil && sameIdentity(id, cached.identity) {
				c.mu.Lock()
				current := gen == c.generation && !c.closed
				c.mu.Unlock()
				if !current {
					continue
				}
				return cached.resolution, cached.err
			}
		}
		if freshMiss {
			c.mu.Lock()
			current := gen == c.generation && !c.closed
			c.mu.Unlock()
			if !current {
				continue
			}
			return ports.AgentBinaryResolution{}, ports.ErrAgentBinaryNotFound
		}
		raw := s.Lookup
		if purpose == ports.BinaryResolvePresence {
			raw = s.Presence
		}
		var localErr error
		if raw != nil {
			path, err := raw(ctx)
			if path != "" && (err == nil || errors.Is(err, ports.ErrAgentBinaryIdentityUnknown)) && purpose == ports.BinaryResolvePresence && s.Normalize != nil {
				var normalizeErr error
				path, normalizeErr = s.Normalize(ctx, path, purpose)
				err = errors.Join(err, normalizeErr)
			}
			if path != "" && (err == nil || errors.Is(err, ports.ErrAgentBinaryIdentityUnknown)) {
				id, validationErr := validate(path)
				if validationErr == nil {
					result, rememberedErr := c.remember(h, path, "local", purpose, err, id, gen)
					if errors.Is(rememberedErr, errInvalidated) {
						continue
					}
					return result, rememberedErr
				}
				localErr = errors.Join(unknown(err), validationErr)
			} else {
				localErr = err
			}
		}
		if err := ctx.Err(); err != nil {
			return ports.AgentBinaryResolution{}, err
		}
		if localErr != nil && !errors.Is(localErr, ports.ErrAgentBinaryNotFound) {
			return ports.AgentBinaryResolution{}, localErr
		}
		c.mu.Lock()
		snapshot := c.snapshot
		fresh := c.snapshotValid && c.now().Sub(c.snapshotAt) < snapshotTTL
		c.mu.Unlock()
		if fresh {
			r, err := c.fromSnapshot(ctx, h, s, snapshot, purpose, gen)
			if errors.Is(err, errInvalidated) {
				continue
			}
			if err == nil || !errors.Is(err, ports.ErrAgentBinaryNotFound) {
				return r, err
			}
			c.mu.Lock()
			if gen != c.generation {
				c.mu.Unlock()
				continue
			}
			c.misses[missKey{h, purpose}] = c.now()
			c.mu.Unlock()
			return ports.AgentBinaryResolution{}, ports.ErrAgentBinaryNotFound
		}
		// An empty allowlist is an explicit adapter opt-out (for example a CLI
		// unsupported on Windows). It must never trigger a login shell.
		if len(s.Names) == 0 {
			if localErr == nil {
				localErr = ports.ErrAgentBinaryNotFound
			}
			return ports.AgentBinaryResolution{}, localErr
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return ports.AgentBinaryResolution{}, context.Canceled
		}
		if gen != c.generation {
			c.mu.Unlock()
			continue
		}
		b := c.running
		if b == nil && c.now().Before(c.retryAt) {
			err := c.lastErr
			c.mu.Unlock()
			return ports.AgentBinaryResolution{}, errors.Join(ports.ErrAgentBinaryChecking, err, unknown(localErr))
		}
		if b == nil {
			if c.probe == nil {
				c.mu.Unlock()
				return ports.AgentBinaryResolution{}, errors.Join(ports.ErrAgentBinaryChecking, unknown(localErr))
			}
			b = &batch{done: make(chan struct{}), generation: c.generation}
			c.running = b
			go c.runBatch(b)
		}
		c.mu.Unlock()
		if purpose == ports.BinaryResolvePresence {
			return ports.AgentBinaryResolution{}, errors.Join(ports.ErrAgentBinaryChecking, unknown(localErr))
		}
		select {
		case <-ctx.Done():
			return ports.AgentBinaryResolution{}, ctx.Err()
		case <-c.ctx.Done():
			return ports.AgentBinaryResolution{}, c.ctx.Err()
		case <-b.done:
		}
	}
}

func unknown(err error) error {
	if errors.Is(err, ports.ErrAgentBinaryNotFound) {
		return nil
	}
	return err
}
func (c *Coordinator) remember(h domain.AgentHarness, path, source string, purpose ports.BinaryResolvePurpose, err error, id fileIdentity, gen uint64) (ports.AgentBinaryResolution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.generation {
		return ports.AgentBinaryResolution{}, errInvalidated
	}
	if cached, ok := c.cache[h]; ok && cached.launch && purpose == ports.BinaryResolvePresence && sameIdentity(cached.identity, id) {
		return cached.resolution, cached.err
	}
	r := ports.AgentBinaryResolution{Executable: path, Source: source, CheckedAt: c.now()}
	if gen == c.generation {
		delete(c.misses, missKey{h, ports.BinaryResolvePresence})
		delete(c.misses, missKey{h, ports.BinaryResolveLaunch})
		c.cache[h] = cachedBinary{resolution: r, identity: id, launch: purpose == ports.BinaryResolveLaunch, err: err}
	}
	return r, err
}
func (c *Coordinator) fromSnapshot(ctx context.Context, h domain.AgentHarness, s ports.AgentBinarySpec, snapshot ports.AgentShellSnapshot, purpose ports.BinaryResolvePurpose, gen uint64) (ports.AgentBinaryResolution, error) {
	var candidates []string
	for _, name := range s.Names {
		if p := snapshot.Paths[name]; p != "" {
			candidates = append(candidates, p)
		}
		for _, dir := range filepath.SplitList(snapshot.Path) {
			if filepath.IsAbs(dir) {
				candidates = append(candidates, filepath.Join(dir, name))
				if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
					for _, ext := range []string{".exe", ".cmd", ".bat"} {
						candidates = append(candidates, filepath.Join(dir, name+ext))
					}
				}
			}
		}
	}
	var candidateErr error
	for _, p := range candidates {
		id, err := validate(p)
		if err != nil {
			if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
				candidateErr = errors.Join(candidateErr, err)
			}
			continue
		}
		if s.Normalize != nil {
			originalPath, originalIdentity := p, id
			env := map[string]string{}
			agentlaunch.AugmentDiscoveredRuntimeEnv(ctx, env, []string{p}, "", snapshot.Path)
			p, err = s.Normalize(aoprocess.WithCommandEnvironment(ctx, env), p, purpose)
			if err != nil && !errors.Is(err, ports.ErrAgentBinaryIdentityUnknown) {
				if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
					candidateErr = errors.Join(candidateErr, err)
				}
				continue
			}
			// A successful identity probe belongs to the executable it started.
			// A vendor replacement during that probe must be validated again.
			currentIdentity, inputErr := validate(originalPath)
			if inputErr != nil || !sameIdentity(originalIdentity, currentIdentity) {
				return ports.AgentBinaryResolution{}, errInvalidated
			}
			var validationErr error
			id, validationErr = validate(p)
			if validationErr != nil {
				candidateErr = errors.Join(candidateErr, unknown(err), unknown(validationErr))
				continue
			}
		}
		return c.remember(h, p, "shell", purpose, err, id, gen)
	}
	if candidateErr != nil {
		return ports.AgentBinaryResolution{}, errors.Join(ports.ErrAgentBinaryChecking, candidateErr)
	}
	return ports.AgentBinaryResolution{}, ports.ErrAgentBinaryNotFound
}

func (c *Coordinator) runBatch(b *batch) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	snapshot, err := c.probe.ProbeAgentShell(ctx, append([]string(nil), c.names...))
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	// Snapshot callbacks are deliberately absent here: normalizing for launch
	// may start a provider to inspect identity. Background discovery only stats.
	valid := make(map[string]string)
	for name, path := range snapshot.Paths {
		if _, validationErr := validate(path); validationErr == nil {
			valid[name] = path
		} else if !errors.Is(validationErr, ports.ErrAgentBinaryNotFound) {
			err = errors.Join(err, validationErr)
		}
	}
	snapshot.Paths = valid
	c.mu.Lock()
	stale := b.generation != c.generation || c.closed
	var changed []domain.AgentHarness
	if !stale {
		if err == nil {
			clear(c.misses)
			for h, s := range c.specs {
				if !c.snapshotValid || snapshot.Path != c.snapshot.Path || selection(s, snapshot) != selection(s, c.snapshot) {
					changed = append(changed, h)
					if cached, ok := c.cache[h]; ok && cached.resolution.Source == "shell" {
						delete(c.cache, h)
					}
				}
			}
			c.snapshot = snapshot
			c.snapshotAt = c.now()
			c.snapshotValid = true
			c.retryAt = time.Time{}
			c.lastErr = nil
			c.failures = 0
		} else {
			c.failures++
			delays := []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}
			i := c.failures - 1
			if i >= len(delays) {
				i = len(delays) - 1
			}
			c.retryAt = c.now().Add(delays[i])
			c.lastErr = err
			for h := range c.specs {
				changed = append(changed, h)
			}
		}
	}
	c.running = nil
	callback := c.onChanged
	close(b.done)
	c.mu.Unlock()
	if c.logger != nil {
		c.logger.Debug("agent binary discovery completed", "names", len(c.names), "found", len(valid), "duration", time.Since(start), "success", err == nil, "stale", stale, "source", "shell")
	}
	if callback != nil && len(changed) > 0 {
		sort.Slice(changed, func(i, j int) bool { return changed[i] < changed[j] })
		callback(changed)
	}
}
func selection(s ports.AgentBinarySpec, snapshot ports.AgentShellSnapshot) string {
	for _, n := range s.Names {
		if p := snapshot.Paths[n]; p != "" {
			return p
		}
	}
	return ""
}
func validate(path string) (fileIdentity, error) {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return fileIdentity{}, ports.ErrAgentBinaryNotFound
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileIdentity{}, ports.ErrAgentBinaryNotFound
		}
		return fileIdentity{}, err
	}
	if !info.Mode().IsRegular() {
		return fileIdentity{}, ports.ErrAgentBinaryNotFound
	}
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".exe", ".cmd", ".bat":
		default:
			return fileIdentity{}, ports.ErrAgentBinaryNotFound
		}
	} else if info.Mode().Perm()&0o111 == 0 {
		return fileIdentity{}, ports.ErrAgentBinaryNotFound
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{info: info, target: target}, nil
}
func sameIdentity(a, b fileIdentity) bool {
	return a.target == b.target && os.SameFile(a.info, b.info) && a.info.Mode() == b.info.Mode() && a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime())
}
