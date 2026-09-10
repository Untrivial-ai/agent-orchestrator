package mobilebridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The hostname of a quick tunnel is only ever available by scraping
// cloudflared's stderr — there is no API for it — so these patterns are a
// contract with a specific cloudflared version. Pin the binary, and let
// tunnel_test.go's verbatim fixtures fail loudly if an upgrade changes the
// format, rather than leaving a tunnel that silently never becomes ready.
var (
	quickTunnelURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	registeredRe     = regexp.MustCompile(`Registered tunnel connection`)
	locationRe       = regexp.MustCompile(`location=([a-z0-9]+)`)
	protocolRe       = regexp.MustCompile(`protocol=([a-z0-9]+)`)
)

// TunnelLog accumulates the signals worth extracting from cloudflared's output.
type TunnelLog struct {
	// URL is the public hostname, once printed.
	URL string
	// Connections counts registered edge connections. Quick tunnels register
	// one; named tunnels register several across colos.
	Connections int
	// Location and Protocol are the edge the connector attached to. Diagnostic
	// only — latency varies several-fold with this value.
	Location string
	Protocol string
}

// Feed consumes one line of cloudflared output.
func (t *TunnelLog) Feed(line string) {
	if t.URL == "" {
		if m := quickTunnelURLRe.FindString(line); m != "" {
			t.URL = m
		}
	}
	if registeredRe.MatchString(line) {
		t.Connections++
		if m := locationRe.FindStringSubmatch(line); m != nil {
			t.Location = m[1]
		}
		if m := protocolRe.FindStringSubmatch(line); m != nil {
			t.Protocol = m[1]
		}
	}
}

// Ready reports whether the tunnel can actually carry traffic.
//
// Both conditions are required. cloudflared prints the hostname several
// seconds before it registers a connection, and an endpoint advertised during
// that window answers HTTP 530.
func (t *TunnelLog) Ready() bool {
	return t.URL != "" && t.Connections > 0
}

// splitLines splits process output into lines, dropping a trailing empty one.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// MinCloudflaredVersion is the oldest system-installed cloudflared we will
// reuse. Older builds differ in flags and log format, and the hostname is only
// available by scraping that format. Below this we install our own managed copy
// alongside rather than touching the user's package-managed one.
var MinCloudflaredVersion = CloudflaredVersion{2025, 8, 0}

// CloudflaredVersion is cloudflared's CalVer, as {year, month, patch}.
type CloudflaredVersion [3]int

var cloudflaredVersionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseCloudflaredVersion reads the version out of `cloudflared --version`.
func ParseCloudflaredVersion(out string) (CloudflaredVersion, bool) {
	m := cloudflaredVersionRe.FindStringSubmatch(out)
	if m == nil {
		return CloudflaredVersion{}, false
	}
	var v CloudflaredVersion
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return CloudflaredVersion{}, false
		}
		v[i] = n
	}
	return v, true
}

// AtLeast reports whether v is floor or newer. Components compare numerically:
// a lexical comparison would rank 2026.10.0 below 2026.9.0.
func (v CloudflaredVersion) AtLeast(floor CloudflaredVersion) bool {
	for i := range v {
		if v[i] != floor[i] {
			return v[i] > floor[i]
		}
	}
	return true
}

// CloudflaredSource records where a resolved binary came from, for logging and
// for the desktop to explain what it is about to do.
type CloudflaredSource string

const (
	// CloudflaredFromEnv is an explicit AO_CLOUDFLARED_PATH override.
	CloudflaredFromEnv CloudflaredSource = "env"
	// CloudflaredManaged is AO's own pinned copy under ~/.ao/bin.
	CloudflaredManaged CloudflaredSource = "managed"
	// CloudflaredFromSystem is a copy the user already installed.
	CloudflaredFromSystem CloudflaredSource = "system"
	// CloudflaredAbsent means nothing usable was found.
	CloudflaredAbsent CloudflaredSource = "absent"
)

// CloudflaredLookup is the injected environment ResolveCloudflared inspects.
// Every filesystem and PATH touch goes through these, so resolution is
// testable without depending on what the machine happens to have installed.
type CloudflaredLookup struct {
	// EnvPath is $AO_CLOUDFLARED_PATH.
	EnvPath string
	// ManagedPath is where AO keeps its own pinned copy (~/.ao/bin/cloudflared).
	ManagedPath string
	LookPath    func(file string) (string, error)
	Exists      func(path string) bool
	Version     func(path string) (CloudflaredVersion, bool)
}

// CloudflaredResolution is which binary to run, or that one must be installed.
type CloudflaredResolution struct {
	Path         string
	Source       CloudflaredSource
	NeedsInstall bool
	// SystemPath is a system copy that was found but rejected as too old or
	// unidentifiable. Recorded so the desktop can say why it is installing its
	// own rather than appearing to ignore what the user already has.
	SystemPath string
}

// ResolveCloudflared picks the cloudflared to run.
//
// Order: explicit override, then AO's managed copy, then a system install that
// is recent enough, then install our own.
//
// Two rules matter. A user's package-managed binary is never modified or
// upgraded — that is their package manager's job — so an outdated system copy
// means we install beside it rather than over it. And the version gate applies
// only to system copies: the managed copy is one we pinned, and the override is
// an operator saying "use exactly this".
func ResolveCloudflared(l CloudflaredLookup) CloudflaredResolution {
	if l.EnvPath != "" {
		return CloudflaredResolution{Path: l.EnvPath, Source: CloudflaredFromEnv}
	}
	if l.ManagedPath != "" && l.Exists != nil && l.Exists(l.ManagedPath) {
		return CloudflaredResolution{Path: l.ManagedPath, Source: CloudflaredManaged}
	}
	if l.LookPath != nil {
		if sys, err := l.LookPath("cloudflared"); err == nil && sys != "" {
			if v, ok := l.Version(sys); ok && v.AtLeast(MinCloudflaredVersion) {
				return CloudflaredResolution{Path: sys, Source: CloudflaredFromSystem}
			}
			return CloudflaredResolution{Source: CloudflaredAbsent, NeedsInstall: true, SystemPath: sys}
		}
	}
	return CloudflaredResolution{Source: CloudflaredAbsent, NeedsInstall: true}
}

// ManagedCloudflaredPath is where AO keeps its own pinned copy.
func ManagedCloudflaredPath(dataDir string) string {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dataDir, "bin", name)
}

// LocalCloudflaredLookup builds the production lookup: the real PATH, the real
// filesystem, and `cloudflared --version` for the version gate.
func LocalCloudflaredLookup(dataDir string) CloudflaredLookup {
	return CloudflaredLookup{
		EnvPath:     os.Getenv("AO_CLOUDFLARED_PATH"),
		ManagedPath: ManagedCloudflaredPath(dataDir),
		LookPath:    exec.LookPath,
		Exists: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && !fi.IsDir()
		},
		Version: func(p string) (CloudflaredVersion, bool) {
			ctx, cancel := context.WithTimeout(context.Background(), cloudflaredVersionTimeout)
			defer cancel()
			out, err := exec.CommandContext(ctx, p, "--version").CombinedOutput()
			if err != nil {
				return CloudflaredVersion{}, false
			}
			return ParseCloudflaredVersion(string(out))
		},
	}
}

// cloudflaredVersionTimeout bounds the `--version` probe. A hung binary must
// not stall daemon startup.
const cloudflaredVersionTimeout = 5 * time.Second

// TunnelPIDPath is where the managed connector's pid is recorded
// (~/.ao/mobile/tunnel.pid).
func TunnelPIDPath(dataDir string) string {
	return filepath.Join(dataDir, "mobile", "tunnel.pid")
}

// WriteTunnelPID records the pid of the connector we just spawned, so a daemon
// that dies without stopping it can clean up on its next start.
func WriteTunnelPID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir mobile dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write tunnel pid: %w", err)
	}
	return nil
}

// ReadTunnelPID returns the recorded pid, if there is one.
func ReadTunnelPID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// ReapStaleTunnel kills a connector left behind by a previous daemon, then
// clears the record.
//
// Without this, a daemon crash leaves a public tunnel to the machine running
// with nobody watching it.
//
// isOurs is required and must actually confirm the process: pids are reused, so
// killing a recorded pid on trust alone would eventually kill something
// unrelated. When it cannot confirm, we clear the file and kill nothing —
// leaking a tunnel until reboot is a far smaller harm than killing an arbitrary
// process.
func ReapStaleTunnel(path string, isOurs func(pid int) bool, kill func(pid int) error) error {
	pid, ok := ReadTunnelPID(path)
	if !ok {
		return nil
	}
	defer func() { _ = os.Remove(path) }()

	if isOurs == nil || !isOurs(pid) {
		return nil
	}
	if err := kill(pid); err != nil {
		return fmt.Errorf("kill stale tunnel %d: %w", pid, err)
	}
	return nil
}

// TunnelStatus is the connector's observable state, for the desktop's Connect
// Mobile panel.
type TunnelStatus struct {
	// Supported is false when this machine has no connector at all — cloudflared
	// is absent and nothing installs it. Without this, "no connector" and "not
	// started yet" were both a zero value, so the desktop showed an ordinary QR
	// and the user had no way to learn that reaching this machine from outside
	// the network is simply not available here.
	Supported bool   `json:"supported"`
	Running   bool   `json:"running"`
	Ready     bool   `json:"ready"`
	Hostname  string `json:"hostname"`
	Location  string `json:"location"`
	LastError string `json:"lastError"`
}

// TunnelRuntime tracks one managed connector across restarts. It owns the rule
// that decides when the tunnel may be advertised, and nothing else — spawning
// and restarting live in the runner above it, so this stays testable without a
// process.
//
// Safe for concurrent use: log lines arrive on the reader goroutine while
// Endpoint is read by whatever is assembling the status response.
type TunnelRuntime struct {
	mu      sync.Mutex
	log     TunnelLog
	running bool
	// settled records that this run's hostname has had time to appear in DNS.
	// A registered connection is not enough: measured against real cloudflared,
	// a brand-new quick tunnel hostname takes roughly twenty more seconds to
	// resolve, and a phone handed the address before then gets "no such host".
	settled bool
	lastErr string
}

// Started marks a fresh connector process.
//
// It resets the parsed log, which matters: quick tunnel hostnames rotate on
// every start, so carrying the previous one into a restart would advertise a
// dead address that answers HTTP 530.
func (r *TunnelRuntime) Started() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = TunnelLog{}
	r.running = true
	r.settled = false
	r.lastErr = ""
}

// Line feeds one line of connector output.
func (r *TunnelRuntime) Line(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.Feed(line)
}

// Exited marks the connector gone, with the reason if there was one.
func (r *TunnelRuntime) Exited(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
	if err != nil {
		r.lastErr = err.Error()
	}
}

// Endpoint is the advertisable tunnel, or nil when there is nothing safe to
// advertise: not started, not yet registered with an edge, or exited.
func (r *TunnelRuntime) Endpoint() *TunnelEndpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running || !r.log.Ready() || !r.settled {
		return nil
	}
	return &TunnelEndpoint{Ready: true, Hostname: hostnameOf(r.log.URL)}
}

// Settled marks this run's hostname as old enough to have propagated. Called
// by the runner once the settling window has elapsed since registration.
//
// Deliberately a delay rather than a probe. A probe from the daemon tests the
// daemon's resolver, not the phone's, and querying the name before the record
// exists caches an NXDOMAIN locally that outlives propagation — measured: after
// an early probe, curl on this machine still failed thirty seconds after dig
// had begun resolving the same name.
func (r *TunnelRuntime) Settled() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = true
}

// UnsettledHostname is a registered hostname still inside its settling window,
// or "" when nothing is waiting — not registered yet, or already settled.
func (r *TunnelRuntime) UnsettledHostname() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running || !r.log.Ready() || r.settled {
		return ""
	}
	return hostnameOf(r.log.URL)
}

// Snapshot is the current state, for display.
func (r *TunnelRuntime) Snapshot() TunnelStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return TunnelStatus{
		Running:   r.running,
		Ready:     r.running && r.log.Ready() && r.settled,
		Hostname:  hostnameOf(r.log.URL),
		Location:  r.log.Location,
		LastError: r.lastErr,
	}
}

// hostnameOf strips the scheme from a tunnel URL. Endpoints carry a host and a
// port separately, so the scheme would be doubled up by the client.
func hostnameOf(rawURL string) string {
	return strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
}

// Backoff bounds for connector restarts.
const (
	// BaseTunnelBackoff is the pause after the first failed start.
	BaseTunnelBackoff = 2 * time.Second
	// MaxTunnelBackoff is the ceiling, so a broken install degrades to an
	// occasional retry rather than a busy loop.
	MaxTunnelBackoff = 2 * time.Minute
)

// TunnelBackoff is how long to wait before restart attempt n (0-based).
func TunnelBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := BaseTunnelBackoff
	for range attempt {
		d *= 2
		if d >= MaxTunnelBackoff {
			return MaxTunnelBackoff
		}
	}
	return d
}

// CloudflaredArgs builds the connector's command line.
//
// The connector targets loopback: sending it at the LAN bind address would
// route tunnel traffic back out over the network for no reason. Autoupdate is
// off because the hostname is only available by scraping the log, and a
// self-update could swap the binary out from under that pinned format. The
// metrics listener is what /ready is polled on, which is a far more reliable
// readiness signal than the log alone.
func CloudflaredArgs(localPort, metricsPort int) []string {
	return []string{
		"tunnel",
		"--url", fmt.Sprintf("http://127.0.0.1:%d", localPort),
		"--metrics", fmt.Sprintf("127.0.0.1:%d", metricsPort),
		"--no-autoupdate",
	}
}

var tunnelPIDLocks = struct {
	sync.Mutex
	byPath map[string]*sync.Mutex
}{byPath: make(map[string]*sync.Mutex)}

// tunnelPIDOwner serializes the complete claim/release transaction for one PID
// path. Checking the recorded PID and removing the file are otherwise two
// filesystem operations with a TOCTOU gap in which a replacement can claim it.
// Separate runner generations get separate values but the same path lock.
type tunnelPIDOwner struct {
	path string
	mu   *sync.Mutex
}

func newTunnelPIDOwner(path string) tunnelPIDOwner {
	clean := filepath.Clean(path)
	tunnelPIDLocks.Lock()
	defer tunnelPIDLocks.Unlock()
	lock := tunnelPIDLocks.byPath[clean]
	if lock == nil {
		lock = &sync.Mutex{}
		tunnelPIDLocks.byPath[clean] = lock
	}
	return tunnelPIDOwner{path: path, mu: lock}
}

func (o tunnelPIDOwner) claim(pid int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return WriteTunnelPID(o.path, pid)
}

func (o tunnelPIDOwner) release(pid int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return RemoveOwnTunnelPIDFile(o.path, pid)
}

// RemoveOwnTunnelPIDFile clears the recorded pid, but only while it is still
// the one this runner wrote. Callers that can overlap replacement startup must
// invoke it through tunnelPIDOwner.release so the check and removal are
// serialized against the replacement claim.
func RemoveOwnTunnelPIDFile(path string, pid int) error {
	if recorded, ok := ReadTunnelPID(path); ok && recorded != pid {
		return nil // A replacement already owns the file; not ours to delete.
	}
	return RemoveTunnelPIDFile(path)
}

// RemoveTunnelPIDFile clears the recorded pid after a clean stop.
func RemoveTunnelPIDFile(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
