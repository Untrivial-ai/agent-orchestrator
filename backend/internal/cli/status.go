package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const probeTimeout = 2 * time.Second

type statusOptions struct {
	json bool
}

type daemonState string

const (
	stateReady     daemonState = "ready"
	stateStopped   daemonState = "stopped"
	stateStale     daemonState = "stale"
	stateUnhealthy daemonState = "unhealthy"
	stateNotReady  daemonState = "not_ready"
	// stateLockedOut is reachable only for a remote target: the per-source
	// lockout lives in the LAN listener's auth middleware, which the loopback
	// path never reaches. See remoteProbeFailure.
	stateLockedOut daemonState = "locked_out"
)

// probeHTTPError is a non-2xx probe response. Its message is deliberately
// identical to the plain error it replaces, so the local path renders
// byte-for-byte as before; it exists so the remote path can read the status code
// back out and tell a lockout apart from an unhealthy daemon.
type probeHTTPError struct {
	path   string
	status int
}

func (e probeHTTPError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.path, e.status) }

type daemonStatus struct {
	State     daemonState `json:"state"`
	PID       int         `json:"pid,omitempty"`
	Port      int         `json:"port,omitempty"`
	StartedAt *time.Time  `json:"startedAt,omitempty"`
	Uptime    string      `json:"uptime,omitempty"`
	RunFile   string      `json:"runFile,omitempty"`
	DataDir   string      `json:"dataDir,omitempty"`
	// URL is set only for a remote target, where there is no local run-file or
	// data dir to report.
	URL    string `json:"url,omitempty"`
	Health string `json:"health,omitempty"`
	Ready  string `json:"ready,omitempty"`
	Error  string `json:"error,omitempty"`
	owned  bool
}

type probeResult struct {
	Status           string `json:"status"`
	Service          string `json:"service"`
	PID              int    `json:"pid"`
	ExecutablePath   string `json:"executablePath,omitempty"`
	WorkingDirectory string `json:"workingDirectory,omitempty"`
}

func newStatusCommand(ctx *commandContext) *cobra.Command {
	var opts statusOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show AO daemon status",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := ctx.inspectDaemon(cmd.Context())
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), st)
			}
			return writeStatus(cmd, st)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output status as JSON")
	return cmd
}

func (c *commandContext) inspectDaemon(ctx context.Context) (daemonStatus, error) {
	if c.remote != nil {
		return c.inspectRemoteDaemon(ctx), nil
	}
	cfg, err := config.Load()
	if err != nil {
		return daemonStatus{}, err
	}
	st := daemonStatus{State: stateStopped, RunFile: cfg.RunFilePath, DataDir: cfg.DataDir}

	info, err := runfile.Read(cfg.RunFilePath)
	if err != nil {
		return daemonStatus{}, err
	}
	if info == nil {
		return st, nil
	}

	st.PID = info.PID
	st.Port = info.Port
	startedAt := info.StartedAt
	st.StartedAt = &startedAt
	st.Uptime = formatUptime(c.deps.Now().Sub(info.StartedAt))

	if !c.deps.ProcessAlive(info.PID) {
		st.State = stateStale
		st.Error = "run-file points to a dead process"
		return st, nil
	}

	health, err := c.readProbe(ctx, loopbackBase(info.Port), "healthz")
	if err != nil {
		st.State = stateUnhealthy
		st.Error = err.Error()
		return st, nil
	}
	if err := verifyProbeOwner(health, info.PID, "healthz"); err != nil {
		st.State = stateStale
		st.Error = err.Error()
		return st, nil
	}
	st.owned = true
	st.Health = health.Status
	if health.Status != "ok" {
		st.State = stateUnhealthy
		return st, nil
	}

	ready, err := c.readProbe(ctx, loopbackBase(info.Port), "readyz")
	if err != nil {
		st.State = stateNotReady
		st.Error = err.Error()
		return st, nil
	}
	if err := verifyProbeOwner(ready, info.PID, "readyz"); err != nil {
		st.State = stateStale
		st.owned = false
		st.Error = err.Error()
		return st, nil
	}
	st.Ready = ready.Status
	if ready.Status == string(stateReady) {
		st.State = stateReady
		return st, nil
	}
	st.State = stateNotReady
	return st, nil
}

func loopbackBase(port int) string {
	return fmt.Sprintf("http://%s:%d", config.LoopbackHost, port)
}

// inspectRemoteDaemon reports the state of a daemon reached over the network.
// There is no run-file and no local PID to gate on, so liveness comes entirely
// from the authenticated /healthz and /readyz probes.
func (c *commandContext) inspectRemoteDaemon(ctx context.Context) daemonStatus {
	st := daemonStatus{State: stateStopped, URL: c.remote.baseURL}

	health, err := c.readProbe(ctx, c.remote.baseURL, "healthz")
	if err != nil {
		return remoteProbeFailure(st, err, stateUnhealthy)
	}
	if health.Service != daemonmeta.ServiceName {
		st.State = stateUnhealthy
		st.Error = "healthz: response is not from AO daemon"
		return st
	}
	st.PID = health.PID
	st.Health = health.Status
	if health.Status != "ok" {
		st.State = stateUnhealthy
		return st
	}

	ready, err := c.readProbe(ctx, c.remote.baseURL, "readyz")
	if err != nil {
		return remoteProbeFailure(st, err, stateNotReady)
	}
	st.Ready = ready.Status
	if ready.Status == string(stateReady) {
		st.State = stateReady
		return st
	}
	st.State = stateNotReady
	return st
}

// remoteProbeFailure classifies a failed probe against a remote daemon. A 429 is
// the LAN listener's per-source lockout after repeated bad connection passwords:
// the daemon is healthy, so reporting "unhealthy" inverts the diagnosis and
// sends a user who fat-fingered a password off to investigate a daemon that is
// fine. Remote-only by construction — the lockout lives in the authenticated
// listener's middleware, which the loopback path never reaches — so local status
// is untouched.
func remoteProbeFailure(st daemonStatus, err error, fallback daemonState) daemonStatus {
	var httpErr probeHTTPError
	if errors.As(err, &httpErr) && httpErr.status == http.StatusTooManyRequests {
		st.State = stateLockedOut
		st.Error = "too many failed connection passwords from this machine; it is temporarily locked out — " +
			"wait for the lockout to clear, then retry with the correct password. The daemon itself is fine."
		return st
	}
	st.State = fallback
	st.Error = err.Error()
	return st
}

func (c *commandContext) readProbe(ctx context.Context, base, path string) (probeResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/"+path, http.NoBody) // #nosec G704 -- base is loopback or an explicitly configured daemon URL.
	if err != nil {
		return probeResult{}, err
	}
	c.authorize(req)
	resp, err := c.deps.HTTPClient.Do(req)
	if err != nil {
		return probeResult{}, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return probeResult{}, probeHTTPError{path: path, status: resp.StatusCode}
	}
	var body probeResult
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return probeResult{}, fmt.Errorf("%s: decode response: %w", path, err)
	}
	if body.Status == "" {
		return probeResult{}, fmt.Errorf("%s: missing status", path)
	}
	return body, nil
}

func verifyProbeOwner(probe probeResult, wantPID int, path string) error {
	if probe.Service != daemonmeta.ServiceName {
		return fmt.Errorf("%s: response is not from AO daemon", path)
	}
	if probe.PID != wantPID {
		return fmt.Errorf("%s: daemon pid %d does not match run-file pid %d", path, probe.PID, wantPID)
	}
	return nil
}

func writeStatus(cmd *cobra.Command, st daemonStatus) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "AO daemon: %s\n", st.State); err != nil {
		return err
	}
	if st.PID != 0 {
		if _, err := fmt.Fprintf(out, "  pid: %d\n", st.PID); err != nil {
			return err
		}
	}
	if st.Port != 0 {
		if _, err := fmt.Fprintf(out, "  port: %d\n", st.Port); err != nil {
			return err
		}
	}
	if st.StartedAt != nil && !st.StartedAt.IsZero() {
		if _, err := fmt.Fprintf(out, "  started: %s\n", st.StartedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if st.Uptime != "" {
		if _, err := fmt.Fprintf(out, "  uptime: %s\n", st.Uptime); err != nil {
			return err
		}
	}
	if st.URL != "" {
		if _, err := fmt.Fprintf(out, "  url: %s\n", st.URL); err != nil {
			return err
		}
	}
	if st.RunFile != "" {
		if _, err := fmt.Fprintf(out, "  run file: %s\n", st.RunFile); err != nil {
			return err
		}
	}
	if st.DataDir != "" {
		if _, err := fmt.Fprintf(out, "  data dir: %s\n", st.DataDir); err != nil {
			return err
		}
	}
	if st.Health != "" {
		if _, err := fmt.Fprintf(out, "  healthz: %s\n", st.Health); err != nil {
			return err
		}
	}
	if st.Ready != "" {
		if _, err := fmt.Fprintf(out, "  readyz: %s\n", st.Ready); err != nil {
			return err
		}
	}
	if st.Error != "" {
		if _, err := fmt.Fprintf(out, "  error: %s\n", st.Error); err != nil {
			return err
		}
	}
	return nil
}

func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}
