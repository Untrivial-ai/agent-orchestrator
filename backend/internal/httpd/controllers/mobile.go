package controllers

import (
	"context"
	"encoding/json"
	"net/http"
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

// BridgeService is the production mobileBridge. It persists state and drives
// the LAN listener. Password plaintext exists only transiently in the response.
type BridgeService struct {
	LAN         LANController
	ConfigPath  string
	DefaultPort int
	// PickLANHost and PickTailscaleHost resolve the advertised addresses. Both
	// are nil in production (daemon.go) and fall back to the real autopickers;
	// tests inject stubs so status output does not depend on the host machine's
	// real network interfaces.
	PickLANHost       func() string
	PickTailscaleHost func() string
	// Secure-pairing collaborators. All nil in production (daemon.go wires the
	// real ones); tests inject fakes so no test shells out to the tailscale CLI.
	ApplyServe  func(port int) error
	ClearServe  func() error
	QueryTS     func() mobilebridge.TailscaleInfo
	ServeTarget func() int

	// serveErr records the last Apply failure so Status can report serve_failed.
	serveErr error
}

func (b *BridgeService) currentHost() string {
	if b.PickLANHost != nil {
		return b.PickLANHost()
	}
	return mobilebridge.AutopickLANIP()
}

func (b *BridgeService) currentTailscaleHost() string {
	if b.PickTailscaleHost != nil {
		return b.PickTailscaleHost()
	}
	return mobilebridge.AutopickTailscaleIP()
}

// Status reports the current bridge state, host, and port. The plaintext
// password is included only while the bridge is enabled (loopback route only).
func (b *BridgeService) Status() MobileStatusResponse {
	st, _ := mobilebridge.Load(b.ConfigPath)
	enabled := st.Enabled && b.LAN.Running()
	res := MobileStatusResponse{
		Enabled:       enabled,
		Host:          b.currentHost(),
		TailscaleHost: b.currentTailscaleHost(),
		Port:          b.LAN.BoundPort(),
		Warning:       mobileUnencryptedWarning,
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

// securePairingStatus assembles the SecurePairing block of Status from the
// persisted mode flag and current bridge/proxy state.
func (b *BridgeService) securePairingStatus(on, bridgeUp bool) SecurePairingStatus {
	sp := SecurePairingStatus{Enabled: on}
	if !on {
		// The mode is off, but a failed Clear may have left the proxy live —
		// report that without touching the network (no queryTS/serveTarget
		// calls when the mode is off).
		if b.serveErr != nil {
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
	if b.serveErr != nil {
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
	st, _ := mobilebridge.Load(b.ConfigPath)
	st.SecurePairing = on
	if err := mobilebridge.Save(b.ConfigPath, st); err != nil {
		return MobileStatusResponse{}, err
	}
	b.serveErr = nil
	if on {
		if b.LAN.Running() {
			b.serveErr = b.applyServe(b.LAN.BoundPort())
		}
	} else {
		// Record rather than return: the flag is already persisted off, so a
		// failure here means the proxy may still be live and the user needs to
		// be told — the same contract the enable path uses for applyServe.
		b.serveErr = b.clearServe()
	}
	return b.Status(), nil
}

func (b *BridgeService) enableWithPassword(pw string) (MobileStatusResponse, error) {
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
	b.serveErr = nil
	if st, _ := mobilebridge.Load(b.ConfigPath); st.SecurePairing {
		b.serveErr = b.applyServe(port)
	}
	return b.Status(), nil
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
	b.LAN.SetPasswordHash(mobilebridge.HashPassword(state.Password))
	port, err := b.LAN.Start(state.LastPort)
	if err != nil {
		return err
	}
	b.serveErr = nil
	if state.SecurePairing {
		b.serveErr = b.applyServe(port)
	}
	return nil
}

// Enable generates a fresh password, arms the auth hash, and starts the LAN
// listener, persisting the enabled state.
func (b *BridgeService) Enable() (MobileStatusResponse, error) {
	pw, err := mobilebridge.GeneratePassword()
	if err != nil {
		return MobileStatusResponse{}, err
	}
	return b.enableWithPassword(pw)
}

// Regenerate rotates the connection password on the running listener, which
// drops the currently paired phone (it authenticates against the new hash).
func (b *BridgeService) Regenerate() (MobileStatusResponse, error) {
	pw, err := mobilebridge.GeneratePassword()
	if err != nil {
		return MobileStatusResponse{}, err
	}
	return b.enableWithPassword(pw) // rotate → drops current phone (new hash)
}

// Disable stops the LAN listener and persists the disabled state.
func (b *BridgeService) Disable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.LAN.Stop(ctx); err != nil {
		return err
	}
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
	return mobilebridge.Save(b.ConfigPath, st)
}

// ShutdownServe removes the tailnet proxy this bridge installed, for use on
// daemon shutdown. `tailscale serve --bg` state lives in tailscaled and
// outlives AO, so without this the tailnet keeps routing to a local port that
// no longer has the authenticated LAN listener behind it — and any other
// process that later binds that port would be published to the tailnet in its
// place. The persisted SecurePairing preference is deliberately left set, so
// RestoreOnBoot re-applies the proxy against the next bound port.
func (b *BridgeService) ShutdownServe() {
	st, _ := mobilebridge.Load(b.ConfigPath)
	if !st.Enabled || !st.SecurePairing {
		return
	}
	_ = b.clearServe()
}
