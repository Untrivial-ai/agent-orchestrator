package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

const mobileUnencryptedWarning = "Traffic on this connection is not encrypted. Only use it on a network you trust."

type mobileBridge interface {
	Status() MobileStatusResponse
	Enable() (MobileStatusResponse, error)
	Disable() error
	Regenerate() (MobileStatusResponse, error)
	StartRemoteAccess() (MobileStatusResponse, error)
	SetSecurePairing(on bool) (MobileStatusResponse, error)
}

// MobileController exposes the Connect Mobile bridge control endpoints
// (status/enable/disable/regenerate) over the loopback API, delegating to a
// mobileBridge and stamping the unencrypted-LAN warning onto every response.
type MobileController struct{ Bridge mobileBridge }

// withWarning stamps the constant unencrypted-LAN warning onto any bridge
// response. The warning is not bridge-specific state — it's always present —
// so the controller guarantees it here rather than trusting every mobileBridge
// implementation (including test fakes) to set it.
func withWarning(res MobileStatusResponse) MobileStatusResponse {
	res.Warning = mobileUnencryptedWarning
	return res
}

// Status returns the current bridge status.
func (c *MobileController) Status(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, withWarning(c.Bridge.Status()))
}

// StartRemoteAccess re-checks for a connector and starts it, leaving the
// connection password alone so an already-paired phone keeps working.
func (c *MobileController) StartRemoteAccess(w http.ResponseWriter, r *http.Request) {
	res, err := c.Bridge.StartRemoteAccess()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_REMOTE_ACCESS", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, withWarning(res))
}

// Enable turns the bridge on and returns the resulting status (with password).
func (c *MobileController) Enable(w http.ResponseWriter, r *http.Request) {
	res, err := c.Bridge.Enable()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_ENABLE", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, withWarning(res))
}

// Disable turns the bridge off and returns the resulting status.
func (c *MobileController) Disable(w http.ResponseWriter, r *http.Request) {
	if err := c.Bridge.Disable(); err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_DISABLE", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, withWarning(c.Bridge.Status()))
}

// Regenerate rotates the connection password and returns the resulting status.
func (c *MobileController) Regenerate(w http.ResponseWriter, r *http.Request) {
	res, err := c.Bridge.Regenerate()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_REGEN", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, withWarning(res))
}

// SecurePairing turns the TLS-over-Tailscale pairing mode on or off.
func (c *MobileController) SecurePairing(w http.ResponseWriter, r *http.Request) {
	var body SetSecurePairingRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "invalid_request", "MOBILE_SECURE_PAIRING", "invalid body", nil)
		return
	}
	res, err := c.Bridge.SetSecurePairing(body.Enabled)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "MOBILE_SECURE_PAIRING", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, withWarning(res))
}

// LANController is the runtime hook set the concrete bridge needs. httpd's
// LANManager + authState satisfy it (adapter wired in daemon.go).
type LANController interface {
	Start(port int) (int, error)
	Stop(ctx context.Context) error
	Running() bool
	BoundPort() int
	SetPasswordHash(hash string)
	PasswordHash() string
}

// TunnelController is the managed remote-access connector, as the bridge sees
// it. Start is idempotent and returns immediately; the connector takes tens of
// seconds to become advertisable, so callers must not block on it.
type TunnelController interface {
	Start(localPort int)
	Stop()
	// Endpoint is the advertisable tunnel, or nil while it is starting,
	// settling, or down.
	Endpoint() *mobilebridge.TunnelEndpoint
	Status() mobilebridge.TunnelStatus
}

// BridgeService is the production mobileBridge. It persists state and drives
// the LAN listener. Password plaintext exists only transiently in the response.
type BridgeService struct {
	LAN         LANController
	ConfigPath  string
	DefaultPort int
	// PickLANHosts and PickTailscaleHosts resolve every advertised address.
	// Both are nil in production (daemon.go) and fall back to the real
	// candidate scans; tests inject stubs so status output does not depend on
	// the host machine's real network interfaces.
	//
	// Lists, not single addresses: the phone races every endpoint, so a machine
	// on both Wi-Fi and Ethernet must advertise both. Host and TailscaleHost are
	// derived from the head of each list so the singular fields and the list can
	// never disagree.
	PickLANHosts       func() []string
	PickTailscaleHosts func() []string
	// Secure-pairing collaborators. All nil in production (daemon.go wires the
	// real ones); tests inject fakes so no test shells out to the tailscale CLI.
	ApplyServe  func(port int) error
	ClearServe  func() error
	QueryTS     func() mobilebridge.TailscaleInfo
	ServeTarget func() int

	// HostID is this machine's stable identity, echoed into the pairing code so
	// the phone can verify every endpoint it races against the machine it
	// actually paired with.
	HostID string

	// Tunnel is the managed remote-access connector. Nil when remote access is
	// unavailable — no usable cloudflared, or the feature switched off — in
	// which case the bridge behaves exactly as the LAN-only version did.
	Tunnel TunnelController

	// ResolveTunnel looks for a connector binary again, for the case where one
	// was installed after the daemon started. Resolution otherwise happens once
	// at boot, so a cloudflared installed from Connect Mobile stayed invisible
	// until AO was restarted. Nil in tests that set Tunnel directly.
	ResolveTunnel func() TunnelController

	// transitionMu serializes every operation that changes persisted bridge
	// state, listener state, or connector state. Status deliberately does not
	// take it, so a slow listener or connector operation cannot freeze polling.
	transitionMu sync.Mutex

	// Guards Tunnel, which ensureTunnel may replace while HTTP handlers read it.
	tunnelMu sync.RWMutex

	// stateMu protects only the short-lived in-memory status snapshot below. No
	// listener, filesystem, or connector call may run while it is held.
	stateMu sync.RWMutex
	// serveErr records the last Apply/Clear failure for Status.
	serveErr error
}

func (b *BridgeService) lanHosts() []string {
	if b.PickLANHosts != nil {
		return b.PickLANHosts()
	}
	return mobilebridge.LocalPrivateIPv4s()
}

func (b *BridgeService) tailscaleHosts() []string {
	if b.PickTailscaleHosts != nil {
		return b.PickTailscaleHosts()
	}
	return mobilebridge.LocalTailscaleIPv4s()
}

// first is the head of a candidate list, or "" when there is none. Callers use
// it for the legacy singular Host/TailscaleHost fields.
func first(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}

// Status reports the current bridge state, host, and port. The plaintext
// password is included only while the bridge is enabled (loopback route only).
func (b *BridgeService) Status() MobileStatusResponse {
	st, _ := mobilebridge.Load(b.ConfigPath)
	enabled := st.Enabled && b.LAN.Running()
	lan := b.lanHosts()
	ts := b.tailscaleHosts()
	res := MobileStatusResponse{
		Enabled:       enabled,
		Host:          first(lan),
		TailscaleHost: first(ts),
		Port:          b.LAN.BoundPort(),
		Warning:       mobileUnencryptedWarning,
		Endpoints: mobilebridge.Endpoints(mobilebridge.EndpointInputs{
			LANHosts:       lan,
			TailscaleHosts: ts,
			Port:           b.LAN.BoundPort(),
			Tunnel:         b.tunnelEndpoint(),
		}),
		Tunnel: b.tunnelStatus(),
		HostID: b.HostID,
	}
	// Only surface the password while the bridge is actually enabled. This route
	// is reachable only on the loopback listener (the LAN listener 404s
	// /api/v1/mobile via lanControlBlock), so the plaintext never reaches a phone.
	if enabled {
		res.Password = st.Password
	}
	res.SecurePairing = b.securePairingStatus(st.SecurePairing, enabled)
	return res
}

// AdvertisedEndpoints reports how this daemon can currently be reached, for
// the phone's refresh route. Same list Status carries, so the two cannot drift.
func (b *BridgeService) AdvertisedEndpoints() []mobilebridge.Endpoint {
	return mobilebridge.Endpoints(mobilebridge.EndpointInputs{
		LANHosts:       b.lanHosts(),
		TailscaleHosts: b.tailscaleHosts(),
		Port:           b.LAN.BoundPort(),
		Tunnel:         b.tunnelEndpoint(),
	})
}

// tunnel reads the current connector without resolving one.
func (b *BridgeService) tunnel() TunnelController {
	b.tunnelMu.RLock()
	defer b.tunnelMu.RUnlock()
	return b.Tunnel
}

// ensureTunnel returns the connector, looking for a newly installed one first.
// Called where a connector is about to be needed, not on every read: probing
// the filesystem to answer a status poll would be wasteful.
func (b *BridgeService) ensureTunnel() TunnelController {
	if t := b.tunnel(); t != nil {
		return t
	}
	if b.ResolveTunnel == nil {
		return nil
	}
	// Resolution may inspect the filesystem. Keep it outside tunnelMu so Status
	// can continue reporting the currently known connector snapshot while that
	// work is slow. transitionMu ensures only one mutator resolves at a time.
	resolved := b.ResolveTunnel()
	if resolved == nil {
		return nil
	}
	b.tunnelMu.Lock()
	defer b.tunnelMu.Unlock()
	if b.Tunnel == nil {
		b.Tunnel = resolved
	}
	return b.Tunnel
}

func (b *BridgeService) tunnelEndpoint() *mobilebridge.TunnelEndpoint {
	t := b.tunnel()
	if t == nil {
		return nil
	}
	return t.Endpoint()
}

func (b *BridgeService) tunnelStatus() mobilebridge.TunnelStatus {
	t := b.tunnel()
	if t == nil {
		return mobilebridge.TunnelStatus{} // Supported stays false: nothing to run.
	}
	st := t.Status()
	st.Supported = true
	return st
}

func (b *BridgeService) queryTS() mobilebridge.TailscaleInfo {
	if b.QueryTS != nil {
		return b.QueryTS()
	}
	return mobilebridge.QueryTailscale(context.Background())
}

func (b *BridgeService) applyServe(port int) error {
	if b.ApplyServe != nil {
		return b.ApplyServe(port)
	}
	return mobilebridge.NewServe().Apply(context.Background(), port)
}

func (b *BridgeService) clearServe() error {
	if b.ClearServe != nil {
		return b.ClearServe()
	}
	return mobilebridge.NewServe().Clear(context.Background())
}

func (b *BridgeService) serveTarget() int {
	if b.ServeTarget != nil {
		return b.ServeTarget()
	}
	return mobilebridge.NewServe().Target(context.Background())
}

func (b *BridgeService) serveError() error {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	return b.serveErr
}

func (b *BridgeService) setServeError(err error) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	b.serveErr = err
}

// securePairingStatus assembles the SecurePairing block of Status from the
// persisted mode flag and current bridge/proxy state.
func (b *BridgeService) securePairingStatus(on, bridgeUp bool) SecurePairingStatus {
	serveErr := b.serveError()
	sp := SecurePairingStatus{Enabled: on}
	if !on {
		// The mode is off, but a failed Clear may have left the proxy live —
		// report that without touching the network (no queryTS/serveTarget
		// calls when the mode is off).
		if serveErr != nil {
			sp.Reason = "clear_failed"
		}
		return sp
	}
	info := b.queryTS()
	switch {
	case info.Name == "":
		sp.Reason = "no_cli"
		return sp
	case !info.CertsEnabled:
		sp.Host = info.Name
		sp.Reason = "no_certs"
		return sp
	}
	sp.Available, sp.Host = true, info.Name
	if !bridgeUp {
		return sp
	}
	if serveErr != nil {
		sp.Reason = "serve_failed"
		return sp
	}
	if b.serveTarget() != b.LAN.BoundPort() {
		sp.Reason = "port_mismatch"
		return sp
	}
	sp.Active, sp.Port = true, 443
	return sp
}

// SetSecurePairing turns TLS-over-Tailscale pairing on or off, persisting the
// choice. Turning it on applies the proxy immediately when the bridge is
// already running; turning it off always tears the proxy down.
func (b *BridgeService) SetSecurePairing(on bool) (MobileStatusResponse, error) {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	st, _ := mobilebridge.Load(b.ConfigPath)
	st.SecurePairing = on
	if err := mobilebridge.Save(b.ConfigPath, st); err != nil {
		return MobileStatusResponse{}, err
	}
	b.setServeError(nil)
	if on {
		if b.LAN.Running() {
			b.setServeError(b.applyServe(b.LAN.BoundPort()))
		}
	} else {
		// Record rather than return: the flag is already persisted off, so a
		// failure here means the proxy may still be live and the user needs to
		// be told — the same contract the enable path uses for applyServe.
		b.setServeError(b.clearServe())
	}
	return b.Status(), nil
}

// enableWithPasswordLocked performs an enable or password rotation while the
// caller owns transitionMu.
func (b *BridgeService) enableWithPasswordLocked(pw string) (MobileStatusResponse, error) {
	// Snapshot state so we can roll back the in-memory side effects (armed hash,
	// running listener) if we fail before durable state is written. Otherwise a
	// failed enable would leave a LAN listener open on 0.0.0.0 with the new
	// password while persisted state/UI still say the bridge is off.
	prevHash := b.LAN.PasswordHash()
	wasRunning := b.LAN.Running()
	prevSt, _ := mobilebridge.Load(b.ConfigPath)

	// The persisted password is plaintext; the auth hash is derived in memory.
	b.LAN.SetPasswordHash(mobilebridge.HashPassword(pw))
	port, err := b.LAN.Start(b.DefaultPort)
	if err != nil {
		b.LAN.SetPasswordHash(prevHash) // Start failed: undo the hash swap.
		return MobileStatusResponse{}, err
	}
	// Preserve the persisted SecurePairing flag — this Save is not the place a
	// user's secure-pairing choice changes, only where enabled/password/port do.
	if err := mobilebridge.Save(b.ConfigPath, mobilebridge.State{Enabled: true, Password: pw, LastPort: port, SecurePairing: prevSt.SecurePairing}); err != nil {
		// Persist failed after the listener came up. Roll back so reality matches
		// the unchanged persisted state (and the UI's "enable failed"). A rotate on
		// an already-running listener (wasRunning) keeps serving on the prior hash;
		// a fresh enable tears the listener back down.
		if !wasRunning {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.LAN.Stop(ctx)
		}
		b.LAN.SetPasswordHash(prevHash)
		return MobileStatusResponse{}, err
	}
	// Re-point the proxy at the port Start actually bound. This runs on every
	// listener start driven through this method — enable and password rotation
	// both funnel through here — which is what keeps the proxy off a dead port
	// after an ephemeral fallback. A daemon restart does NOT go through this
	// method (it has no password to rotate); see RestoreOnBoot, which mirrors
	// this same post-Start apply. A failure is recorded, never fatal: the
	// bridge stays up in plaintext mode and Status reports serve_failed.
	b.startConnectorsLocked(port, prevSt.SecurePairing)
	return b.Status(), nil
}

// startConnectorsLocked is the shared post-Start step for fresh enables,
// password rotations, and boot restoration. The caller owns transitionMu.
func (b *BridgeService) startConnectorsLocked(port int, securePairing bool) {
	b.setServeError(nil)
	if securePairing {
		b.setServeError(b.applyServe(port))
	}
	// Point the connector at the port Start actually bound, not DefaultPort:
	// Start falls back to an ephemeral port when the default is taken, and a
	// connector aimed at the wrong port tunnels nothing. Start is idempotent
	// and returns immediately — the connector needs tens of seconds to become
	// advertisable, and Status reports that progress meanwhile.
	// Resolve here rather than only at boot: this is the moment a connector
	// installed since then should start being used.
	if t := b.ensureTunnel(); t != nil {
		t.Start(port)
	}
}

// RestoreOnBoot re-arms the LAN listener from persisted state across a daemon
// restart, reusing the existing password (no rotation — an already-paired
// phone keeps working) and re-applying the secure-pairing proxy against the
// port Start actually bound, never the persisted LastPort. That distinction is
// the entire point of this method: Start falls back to an ephemeral port when
// LastPort is taken (e.g. by another AO instance), and a `tailscale serve`
// config pinned to a stale port would proxy the tailnet at this machine's
// hostname to whatever now holds that port. A failure to apply is recorded in
// serveErr, never returned — the caller (restoreMobileOnBoot) treats this as
// best-effort and must never block daemon boot on it.
func (b *BridgeService) RestoreOnBoot(state mobilebridge.State) error {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	prevHash := b.LAN.PasswordHash()
	b.LAN.SetPasswordHash(mobilebridge.HashPassword(state.Password))
	port, err := b.LAN.Start(state.LastPort)
	if err != nil {
		b.LAN.SetPasswordHash(prevHash)
		return err
	}
	b.startConnectorsLocked(port, state.SecurePairing)
	return nil
}

// Enable generates a fresh password, arms the auth hash, and starts the LAN
// listener, persisting the enabled state.
func (b *BridgeService) Enable() (MobileStatusResponse, error) {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	pw, err := mobilebridge.GeneratePassword()
	if err != nil {
		return MobileStatusResponse{}, err
	}
	return b.enableWithPasswordLocked(pw)
}

// StartRemoteAccess looks for a connector again and starts it against the port
// already bound, without touching the connection password.
//
// Exists because the obvious alternative is wrong: re-enabling would make the
// daemon re-resolve, but Enable mints a fresh password, so installing
// cloudflared would silently invalidate the phone that was already paired —
// the user sets up remote access and loses the connection they were setting it
// up for.
//
// A no-op while the bridge is disabled: there is no bound port to tunnel to,
// and enabling is the user's decision to make, not a side effect of installing
// a binary.
func (b *BridgeService) StartRemoteAccess() (MobileStatusResponse, error) {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	st, err := mobilebridge.Load(b.ConfigPath)
	if err != nil {
		return MobileStatusResponse{}, err
	}
	if !st.Enabled || !b.LAN.Running() {
		return b.Status(), nil
	}
	if t := b.ensureTunnel(); t != nil {
		t.Start(b.LAN.BoundPort())
	}
	return b.Status(), nil
}

// Regenerate rotates the connection password on the running listener, which
// drops the currently paired phone (it authenticates against the new hash).
func (b *BridgeService) Regenerate() (MobileStatusResponse, error) {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	pw, err := mobilebridge.GeneratePassword()
	if err != nil {
		return MobileStatusResponse{}, err
	}
	return b.enableWithPasswordLocked(pw) // rotate → drops current phone (new hash)
}

// Disable stops the LAN listener and persists the disabled state.
func (b *BridgeService) Disable() error {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Stop the connector first. Leaving it up after the user turned Connect
	// Mobile off would keep the machine reachable from the internet while the
	// UI says it is not.
	if t := b.tunnel(); t != nil {
		t.Stop()
	}
	stopErr := b.LAN.Stop(ctx)
	if stopErr != nil && b.LAN.Running() {
		return stopErr
	}
	// Forced cleanup can finish after the graceful-shutdown deadline. Persist
	// the disabled preference once stopped, while still returning that error.
	st, _ := mobilebridge.Load(b.ConfigPath)
	// Only touch the tailnet proxy when this bridge actually installed one.
	// `tailscale serve --https=443 off` is node-global state: clearing it
	// unconditionally would destroy a serve route the user configured for
	// themselves, or one owned by another AO instance, for someone who never
	// enabled secure pairing at all.
	if st.SecurePairing {
		_ = b.clearServe()
	}
	st.Enabled = false
	return errors.Join(stopErr, mobilebridge.Save(b.ConfigPath, st))
}

// ShutdownTunnel stops the managed connector on the way out.
//
// The same reasoning as ShutdownServe: a cloudflared process outlives this
// daemon, so leaving it running would keep a public hostname resolving to a
// port that no longer has the authenticated LAN listener behind it. Reaping on
// the next boot recovers from a crash, but a clean quit should not depend on
// it — until the user happens to reopen the app the process keeps running and
// the hostname stays registered.
//
// Deliberately not touching persisted state: the bridge stays enabled, so boot
// restore brings the connector back on the next start. This ends the process,
// not the user's preference.
func (b *BridgeService) ShutdownTunnel() {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	t := b.tunnel()
	if t == nil {
		return // Remote access is optional; nothing to stop without cloudflared.
	}
	t.Stop()
}

// ShutdownServe removes the tailnet proxy this bridge installed, for use on
// daemon shutdown. `tailscale serve --bg` state lives in tailscaled and
// outlives AO, so without this the tailnet keeps routing to a local port that
// no longer has the authenticated LAN listener behind it — and any other
// process that later binds that port would be published to the tailnet in its
// place. The persisted SecurePairing preference is deliberately left set, so
// RestoreOnBoot re-applies the proxy against the next bound port.
func (b *BridgeService) ShutdownServe() {
	b.transitionMu.Lock()
	defer b.transitionMu.Unlock()

	st, _ := mobilebridge.Load(b.ConfigPath)
	if !st.Enabled || !st.SecurePairing {
		return
	}
	_ = b.clearServe()
}
