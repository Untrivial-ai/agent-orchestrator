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

// EndpointInputs is everything Endpoints needs to build the candidate list.
type EndpointInputs struct {
	LANHosts       []string
	TailscaleHosts []string
	Port           int
	// Tunnel is nil when remote access is off.
	Tunnel *TunnelEndpoint
}

// Endpoints builds the candidate list the phone races.
//
// A zero port means the LAN listener is not bound — Connect Mobile is off, or
// still starting — so there is nothing to advertise at all. Emitting host:0
// entries would have the phone race addresses that cannot work, and the tunnel
// is no exception: it forwards to that same port, so with no listener behind it
// there is nothing for it to reach.
func Endpoints(in EndpointInputs) []Endpoint {
	var out []Endpoint
	if in.Port <= 0 {
		return nil
	}
	for _, h := range in.LANHosts {
		out = append(out, Endpoint{Kind: KindLAN, Host: h, Port: in.Port})
	}
	for _, h := range in.TailscaleHosts {
		out = append(out, Endpoint{Kind: KindTailscale, Host: h, Port: in.Port})
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
