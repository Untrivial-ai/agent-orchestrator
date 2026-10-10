package cua

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

type driverIdentity struct {
	pid     int
	started time.Time
}

func (a *Adapter) binary() string {
	return filepath.Join(a.cfg.AppPath, "Contents", "MacOS", "cua-driver")
}
func (a *Adapter) socket() string  { return filepath.Join(a.controlRoot, "driver.sock") }
func (a *Adapter) pidFile() string { return filepath.Join(a.controlRoot, "driver.pid") }

func (a *Adapter) driverEnvironment() []string {
	return []string{
		"CUA_DRIVER_RS_HOME=" + filepath.Join(a.root, "home"),
		"CUA_DRIVER_TELEMETRY_HOME=" + filepath.Join(a.root, "home"),
		"CUA_DRIVER_RS_TELEMETRY_ENABLED=0",
		"CUA_DRIVER_RS_UPDATE_CHECK=0",
		"TMPDIR=" + filepath.Join(a.root, "tmp"),
	}
}

func (a *Adapter) run(ctx context.Context, executable string, args ...string) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	env := append(cleanEnvironment(), a.driverEnvironment()...)
	return a.runner.Run(ctx, executable, args, env)
}

func (a *Adapter) ensureDriver(ctx context.Context) error {
	if a.driver.pid != 0 {
		return a.checkDriver(ctx)
	}
	if a.pendingDriver.pid != 0 {
		return a.admitDriver(ctx)
	}
	if !a.launchIssued {
		if err := a.launchDriver(ctx); err != nil {
			return err
		}
	}
	if err := a.observeDriver(ctx); err != nil {
		return err
	}
	return a.admitDriver(ctx)
}

func (a *Adapter) launchDriver(ctx context.Context) error {
	for _, dir := range []string{a.root, a.controlRoot, filepath.Join(a.root, "home"), filepath.Join(a.root, "tmp"), filepath.Join(a.root, "captures")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return refuse("unsafe_storage", "driver storage directory must not be a symlink")
		}
	}
	for _, path := range []string{a.socket(), a.pidFile()} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return refuse("driver_path_busy", "refusing to adopt or remove an existing driver socket or PID file")
		}
	}
	// Check the app identity before LaunchServices can trigger a TCC prompt.
	if _, err := a.run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", a.cfg.AppPath); err != nil {
		return fmt.Errorf("verify Cua app signature: %w", err)
	}
	signature, err := a.run(ctx, "/usr/bin/codesign", "-dv", "--verbose=4", a.cfg.AppPath)
	if err != nil {
		return err
	}
	identity := string(signature.Stderr)
	if !strings.Contains(identity, "Identifier=com.trycua.driver\n") || !strings.Contains(identity, "TeamIdentifier=YCK386LBJ7\n") {
		return refuse("driver_identity", "expected the signed Cua AI Driver app identity")
	}
	version, err := a.run(ctx, a.binary(), "--version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(version.Stdout)) != "cua-driver 0.34.0" {
		return refuse("driver_version", "this adapter requires Cua Driver 0.34.0")
	}
	// open launches through launchd rather than inheriting only cmd.Env. Refuse
	// a launchd listener override as well as stripping both from child env.
	for _, name := range []string{"CUA_DRIVER_RS_MCP_HTTP_PORT", "CUA_DRIVER_ENVELOPE_HTTP_PORT"} {
		value, err := a.run(ctx, "/bin/launchctl", "getenv", name)
		if err != nil || strings.TrimSpace(string(value.Stdout)) != "" {
			return refuse("listener_environment", "cannot prove optional Cua HTTP listener is disabled: "+name)
		}
	}
	args := []string{"-n", "-g", "-a", a.cfg.AppPath}
	for _, value := range a.driverEnvironment() {
		args = append(args, "--env", value)
	}
	args = append(args, "--stdout", filepath.Join(a.root, "daemon.stdout.log"), "--stderr", filepath.Join(a.root, "daemon.stderr.log"),
		"--args", "serve", "--socket", a.socket(), "--pid-file", a.pidFile(), "--no-overlay")
	a.launchIssued = true // even a canceled open can have launched Cua
	if _, err := a.run(ctx, "/usr/bin/open", args...); err != nil {
		return fmt.Errorf("launch Cua app: %w", err)
	}
	return nil
}

// observeDriver completes ownership observation after the single launch request.
// Cancellation cannot discard a kernel birth receipt once its PID file exists.
func (a *Adapter) observeDriver(ctx context.Context) error {
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, readErr := os.ReadFile(a.pidFile())
		if readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				return refuse("driver_pid", "invalid driver PID file")
			}
			started, err := a.started(context.WithoutCancel(ctx), pid)
			if err != nil {
				return err
			}
			// Keep cleanup ownership without admitting an unchecked driver.
			a.pendingDriver = driverIdentity{pid: pid, started: started}
			if err := ready.Err(); err != nil {
				return err
			}
			if info, err := os.Lstat(a.socket()); err == nil && info.Mode()&os.ModeSocket != 0 {
				return nil
			}
		}
		select {
		case <-ready.Done():
			return fmt.Errorf("cua startup did not become ready; Close must reap any owned PID: %w", ready.Err())
		case <-ticker.C:
		}
	}
}

func (a *Adapter) admitDriver(ctx context.Context) error {
	if err := a.checkDriver(ctx); err != nil {
		return err
	}
	if info, err := os.Lstat(a.socket()); err != nil || info.Mode()&os.ModeSocket == 0 {
		return refuse("driver_socket", "owned driver socket is not ready")
	}
	var permissions struct {
		Accessibility   bool `json:"accessibility"`
		ScreenRecording bool `json:"screen_recording"`
	}
	if err := a.call(ctx, "check_permissions", map[string]any{"prompt": false, "probe_direct_capture": false}, &permissions); err != nil {
		return err
	}
	if !permissions.Accessibility || !permissions.ScreenRecording {
		return refuse("permissions", "Cua Driver needs its own Accessibility and Screen Recording grants")
	}
	a.driver, a.pendingDriver = a.pendingDriver, driverIdentity{}
	return nil
}

func (a *Adapter) checkDriver(ctx context.Context) error {
	driver := a.driver
	if driver.pid == 0 {
		driver = a.pendingDriver
	}
	if driver.pid <= 0 {
		return refuse("driver_missing", "driver is not running")
	}
	started, err := a.started(ctx, driver.pid)
	if err != nil || !started.Equal(driver.started) {
		return refuse("driver_changed", "driver PID or birth time changed; refusing to reconnect")
	}
	data, err := os.ReadFile(a.pidFile())
	if err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(driver.pid) {
		return refuse("driver_changed", "driver PID file no longer names the owned process")
	}
	return nil
}

// startSession creates or revives only this binding's provider session before
// observation. Inputs never revive a session or reuse its retired receipts.
func (a *Adapter) startSession(ctx context.Context, b *binding) error {
	var result struct {
		Active  bool `json:"active"`
		Revived bool `json:"revived"`
	}
	b.sessionIssued = true
	err := a.call(ctx, "start_session", map[string]any{"session": b.session}, &result)
	if err != nil || !result.Active || result.Revived {
		b.receipt, b.lastTyped = nil, nil
	}
	if err != nil {
		return err
	}
	if !result.Active {
		return &Error{Code: "provider_protocol", Detail: "call start_session did not return an active session", cause: ErrProvider}
	}
	return nil
}

// Release revokes one attempt and verifies driver shutdown after the last
// binding, without closing the reusable adapter. The target owns Electron.
func (a *Adapter) Release(ctx context.Context, target domain.TestTargetIdentity) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bindings[target.ID]
	if b == nil {
		if prior := a.released[target.ID]; prior != nil && sameTarget(prior.target, target) {
			return prior.releaseErr
		}
		return refuse("target_not_bound", "release target does not match binding")
	}
	if !sameTarget(b.target, target) {
		return refuse("target_not_bound", "release target does not match binding")
	}
	b.receipt = nil
	var cleanup error
	if b.recording != nil {
		if _, err := a.stopRecording(ctx, b); err != nil {
			cleanup = err
			select {
			case <-b.recording.process.done:
				// A movie gap must not leave an exited recorder's Driver alive.
			default:
				return err
			}
		}
	}
	if b.sessionIssued {
		if err := a.checkDriver(ctx); err == nil {
			if err := a.call(ctx, "end_session", map[string]any{"session": b.session}, nil); err != nil {
				return errors.Join(cleanup, err)
			}
		} else if len(a.bindings) != 1 {
			return errors.Join(cleanup, err)
		}
	}
	if len(a.bindings) == 1 {
		if err := a.stopDriver(ctx); err != nil {
			return errors.Join(cleanup, err) // retain the binding for cleanup retry
		}
	}
	b.releaseErr = cleanup
	a.released[target.ID] = b
	delete(a.bindings, target.ID)
	return cleanup
}

// Close revokes sessions and stops only the daemon this instance launched,
// fenced by its PID, birth timestamp, custom socket and --expected-pid. It
// retains storage/logs and reports failed cleanup rather than killing broadly.
func (a *Adapter) Close(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	var cleanup error
	pending := false
	for _, b := range a.bindings {
		if b.recording != nil {
			_, err := a.stopRecording(ctx, b)
			cleanup = errors.Join(cleanup, err)
			select {
			case <-b.recording.process.done:
			default:
				pending = true
			}
		}
	}
	if pending {
		return cleanup // retain ownership for a later cleanup retry
	}
	if err := a.checkDriver(ctx); err == nil {
		for _, b := range a.bindings {
			if b.sessionIssued {
				cleanup = errors.Join(cleanup, a.call(ctx, "end_session", map[string]any{"session": b.session}, nil))
			}
		}
	}
	cleanup = errors.Join(cleanup, a.stopDriver(ctx))
	if !a.launchIssued && a.driver.pid == 0 && a.pendingDriver.pid == 0 {
		a.bindings = make(map[string]*binding)
	}
	return cleanup
}

// stopDriver is called under a.mu. A failed stop keeps its launch receipt.
func (a *Adapter) stopDriver(ctx context.Context) error {
	if a.launchIssued && a.driver.pid == 0 && a.pendingDriver.pid == 0 {
		if err := a.observeDriver(ctx); err != nil {
			return fmt.Errorf("owned Cua launch cleanup cannot be proved: %w", err)
		}
	}
	driver := a.driver
	if driver.pid == 0 {
		driver = a.pendingDriver
	}
	if driver.pid == 0 {
		return nil
	}
	if err := a.checkDriver(ctx); err != nil {
		started, probeErr := a.started(ctx, driver.pid)
		if errors.Is(probeErr, process.ErrNotRunning) || (probeErr == nil && !started.Equal(driver.started)) {
			_, pidErr := os.Lstat(a.pidFile())
			_, socketErr := os.Lstat(a.socket())
			if errors.Is(pidErr, os.ErrNotExist) && errors.Is(socketErr, os.ErrNotExist) {
				a.driver, a.pendingDriver = driverIdentity{}, driverIdentity{}
				a.launchIssued = false
				return nil
			}
		}
		return err
	}
	_, err := a.run(ctx, a.binary(), "--socket", a.socket(), "--pid-file", a.pidFile(), "stop", "--expected-pid", strconv.Itoa(driver.pid))
	if err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		started, probeErr := a.started(ctx, driver.pid)
		if errors.Is(probeErr, process.ErrNotRunning) || (probeErr == nil && !started.Equal(driver.started)) {
			// A failed probe is not enough: the daemon must also remove its own
			// PID file and socket before cleanup is reported complete.
			_, pidErr := os.Lstat(a.pidFile())
			_, socketErr := os.Lstat(a.socket())
			if errors.Is(pidErr, os.ErrNotExist) && errors.Is(socketErr, os.ErrNotExist) {
				a.driver, a.pendingDriver = driverIdentity{}, driverIdentity{}
				a.launchIssued = false
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("owned Cua daemon cleanup incomplete: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}
