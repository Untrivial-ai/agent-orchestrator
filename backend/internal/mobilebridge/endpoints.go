package mobilebridge

import "net"

// EndpointKind names a way to reach this daemon. The phone races every
// advertised endpoint and prefers the lowest-latency kind that answers.
type EndpointKind string

// The advertised endpoint kinds, in the client's preference order: LAN is
// fastest, Tailscale crosses networks when it is already installed, and the
// tunnel reaches anything but is the slowest.
const (
	KindLAN       EndpointKind = "lan"
	KindTailscale EndpointKind = "tailscale"
	KindTunnel    EndpointKind = "tunnel"
)

// tunnelPort is the port a Cloudflare tunnel hostname is always reached on.
const tunnelPort = 443

// TunnelEndpoint is the current state of the managed cloudflared sidecar.
// Ready is false until the connector has registered with an edge; a hostname
// alone is not enough, because cloudflared prints one seconds before the
// tunnel actually carries traffic.
type TunnelEndpoint struct {
	Ready    bool
	Hostname string
}

// Endpoint is one advertised route to the daemon.
type Endpoint struct {
	Kind   EndpointKind `json:"kind"`
	Host   string       `json:"host"`
	Port   int          `json:"port"`
	Secure bool         `json:"secure"`
}

// SecurePairingState is what the candidate list needs to know about secure
// pairing: whether the mode is on, and — only while the proxy is verified to
// front this bridge's port — the proxy's address, the node's MagicDNS name on
// :443 with a certificate the tailnet issued (`tailscale serve`).
type SecurePairingState struct {
	Enabled bool
	// Host and Port are empty unless the proxy is serving this bridge right
	// now. A proxy that exists but points elsewhere must not be named here,
	// or the tailnet candidate is one that cannot win.
	Host string
	Port int
}

// EndpointInputs is everything Endpoints needs to build the candidate list.
type EndpointInputs struct {
	LANHosts       []string
	TailscaleHosts []string
	Port           int
	// Tunnel is nil when remote access is off.
	Tunnel *TunnelEndpoint
	// SecurePairing decides how the tailnet is advertised; see Endpoints.
	SecurePairing SecurePairingState
}

// Endpoints builds the candidate list the phone races.
//
// A zero port means the LAN listener is not bound — Connect Mobile is off, or
// still starting — so there is nothing to advertise at all. Emitting host:0
// entries would have the phone race addresses that cannot work, and the tunnel
// and the secure-pairing proxy are no exception: both forward to that same
// port, so with no listener behind it there is nothing for them to reach.
//
// The tailnet is advertised as at most one candidate, chosen by the state of
// secure pairing:
//
//   - Mode off: the plaintext 100.x address, as before.
//   - Mode on and the proxy verified: the proxy, TLS on the MagicDNS name, in
//     place of the plaintext address. iOS refuses cleartext to 100.64.0.0/10
//     (the app's NSAllowsLocalNetworking exemption covers only local ranges),
//     so a plaintext entry can never be won by an iPhone except through the
//     tunnel. Not both: the phone keeps whichever of two same-ranked answers
//     arrives first, so a plaintext and a TLS entry side by side would make the
//     winner depend on packet timing, and every race that landed on the other
//     one would tear down the streams keyed on the config.
//   - Mode on but the proxy not verified: no tailnet entry at all. The phone
//     treats a kind the list omits as unknown and keeps what it has, whereas a
//     plaintext entry would replace the stored TLS one — and "not verified"
//     includes a tailscale CLI call that timed out and the moment between the
//     listener binding and the proxy being applied on enable or boot. Handing
//     an iPhone the plaintext address in one of those windows would take away
//     the only tailnet candidate it can use, with no way to get it back until
//     it next connects over the LAN or the tunnel. While the mode is on and
//     broken for good, the desktop shows why; the plaintext address is not a
//     substitute, since the mode exists because that address fails on iOS.
func Endpoints(in EndpointInputs) []Endpoint {
	var out []Endpoint
	if in.Port <= 0 {
		return nil
	}
	for _, h := range in.LANHosts {
		out = append(out, Endpoint{Kind: KindLAN, Host: h, Port: in.Port})
	}
	switch sp := in.SecurePairing; {
	case sp.Enabled && sp.Host != "" && sp.Port > 0:
		out = append(out, Endpoint{Kind: KindTailscale, Host: sp.Host, Port: sp.Port, Secure: true})
	case sp.Enabled:
		// Unverified proxy: advertise nothing for the tailnet (see above).
	default:
		for _, h := range in.TailscaleHosts {
			out = append(out, Endpoint{Kind: KindTailscale, Host: h, Port: in.Port})
		}
	}
	if in.Tunnel != nil && in.Tunnel.Ready && in.Tunnel.Hostname != "" {
		out = append(out, Endpoint{Kind: KindTunnel, Host: in.Tunnel.Hostname, Port: tunnelPort, Secure: true})
	}
	return out
}

// LocalEndpoints builds the advertised candidate list from the machine's
// network interfaces. Unlike AutopickLANIP/AutopickTailscaleIP it keeps every
// candidate, because the phone races them and a machine on both Wi-Fi and
// Ethernet must advertise both. addrsOf is injected so callers (and tests) can
// supply the per-interface address lookup.
func LocalEndpoints(
	ifaces []net.Interface,
	addrsOf func(net.Interface) ([]net.Addr, error),
	port int,
	tunnel *TunnelEndpoint,
) []Endpoint {
	return Endpoints(EndpointInputs{
		LANHosts:       PrivateIPv4Candidates(ifaces, addrsOf),
		TailscaleHosts: TailscaleIPv4Candidates(ifaces, addrsOf),
		Port:           port,
		Tunnel:         tunnel,
	})
}

// AdvertisedEndpoints is the production entry point: LocalEndpoints against
// this machine's real interfaces. Returns an empty list when interfaces cannot
// be read, which the phone must render as "offline" rather than as an error.
func AdvertisedEndpoints(port int, tunnel *TunnelEndpoint) []Endpoint {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	return LocalEndpoints(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return i.Addrs()
	}, port, tunnel)
}
