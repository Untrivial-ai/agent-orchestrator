package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

// staleProbeTimeout bounds the startup ownership probe so a run-file pointing at
// an unreachable port cannot stall daemon startup.
const staleProbeTimeout = 2 * time.Second

// conventionalDataDirOwner checks the running.json that older AO releases
// place beside their data/ directory. Those releases predate daemon.lock, so
// probing this conventional handshake closes the upgrade window where a new
// daemon uses a different run file but points at an older live daemon's data.
func conventionalDataDirOwner(client *http.Client, host, dataDir, configuredRunFile string) (*runfile.Info, error) {
	ownerRunFile := filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "running.json")
	if filepath.Clean(ownerRunFile) == filepath.Clean(configuredRunFile) {
		return nil, nil
	}
	live, err := runfile.CheckStale(ownerRunFile)
	if err != nil {
		return nil, err
	}
	if live == nil || !runFileOwnerServing(client, host, live) {
		return nil, nil
	}
	return live, nil
}

// runFileOwnerServing reports whether an AO daemon matching info is actually
// serving on the recorded loopback port.
//
// runfile.CheckStale only confirms the recorded PID is alive, which is not
// enough to conclude a predecessor still owns the port. On Windows the desktop
// supervisor can only TerminateProcess the daemon (no POSIX signal reaches it),
// so the daemon's graceful shutdown never runs and running.json is never
// removed; the leaked file then survives into the next launch. Because Windows
// reuses PIDs aggressively, the recorded PID routinely belongs to an unrelated
// process, making the PID-only check report "alive" for a daemon that is long
// gone — which is what made the daemon refuse to start (issue #256).
//
// Probing /healthz and matching both the service name and the PID is the ground
// truth that a real predecessor is still listening. When it is not, the
// run-file is stale and the caller should overwrite it instead of refusing.
func runFileOwnerServing(client *http.Client, host string, info *runfile.Info) bool {
	if info == nil || info.Port <= 0 {
		return false
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s:%d/healthz", host, info.Port), http.NoBody)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	var body struct {
		Service string `json:"service"`
		PID     int    `json:"pid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Service == daemonmeta.ServiceName && body.PID == info.PID
}
