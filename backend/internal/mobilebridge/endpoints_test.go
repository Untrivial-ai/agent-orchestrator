package mobilebridge

import (
	"net"
	"testing"
)

func TestEndpointsListsEveryLANAddress(t *testing.T) {
	// A machine on both Wi-Fi and Ethernet has two reachable LAN addresses.
	// The phone races them, so both must be advertised — the old AutopickLANIP
	// kept only the first, so the phone could never try the other.
	got := Endpoints(EndpointInputs{
		LANHosts: []string{"192.168.1.42", "10.0.0.5"},
		Port:     3011,
	})

	want := []Endpoint{
		{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
		{Kind: KindLAN, Host: "10.0.0.5", Port: 3011, Secure: false},
	}
	assertEndpoints(t, got, want)
}

func assertEndpoints(t *testing.T, got, want []Endpoint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d endpoints %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("endpoint %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestEndpointsPutsTailscaleAfterLAN(t *testing.T) {
	// Order encodes preference: the client's tie-break is lan > tailscale >
	// tunnel, and a client that cannot race should still try the fastest first.
	got := Endpoints(EndpointInputs{
		LANHosts:       []string{"192.168.1.42"},
		TailscaleHosts: []string{"100.72.46.7"},
		Port:           3011,
	})

	want := []Endpoint{
		{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
		{Kind: KindTailscale, Host: "100.72.46.7", Port: 3011, Secure: false},
	}
	assertEndpoints(t, got, want)
}

func TestEndpointsAppendsReadyTunnelLast(t *testing.T) {
	// The tunnel reaches any network but is the slowest path, so it sorts last.
	// It carries its own host and port and is always TLS.
	got := Endpoints(EndpointInputs{
		LANHosts: []string{"192.168.1.42"},
		Port:     3011,
		Tunnel:   &TunnelEndpoint{Ready: true, Hostname: "abc.trycloudflare.com"},
	})

	want := []Endpoint{
		{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
		{Kind: KindTunnel, Host: "abc.trycloudflare.com", Port: 443, Secure: true},
	}
	assertEndpoints(t, got, want)
}

func TestEndpointsOmitsTunnelThatIsNotReady(t *testing.T) {
	// cloudflared prints a hostname several seconds before it has registered a
	// connection. Advertising it during that window hands the phone an endpoint
	// that answers 530, so readiness gates the whole entry.
	got := Endpoints(EndpointInputs{
		LANHosts: []string{"192.168.1.42"},
		Port:     3011,
		Tunnel:   &TunnelEndpoint{Ready: false, Hostname: "abc.trycloudflare.com"},
	})

	assertEndpoints(t, got, []Endpoint{
		{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
	})
}

func TestEndpointsIsEmptyWhenNothingIsReachable(t *testing.T) {
	// No network and no tunnel is a real state, not an error. The phone must
	// receive an empty list and say "offline" rather than race a stale address.
	got := Endpoints(EndpointInputs{Port: 3011})
	assertEndpoints(t, got, nil)
}

func TestEndpointsOmitsTunnelWithoutHostname(t *testing.T) {
	got := Endpoints(EndpointInputs{
		Port:   3011,
		Tunnel: &TunnelEndpoint{Ready: true, Hostname: ""},
	})
	assertEndpoints(t, got, nil)
}

func TestEndpointsAdvertisesTheSecurePairingProxyInPlaceOfTheTailscaleAddress(t *testing.T) {
	// iOS refuses cleartext to 100.64.0.0/10, so the plaintext Tailscale entry
	// can never win there. While the secure-pairing proxy is serving, the
	// tailnet candidate is the proxy — same kind, so it ranks with the tailnet
	// and ahead of the tunnel; secure, so the phone dials https on the MagicDNS
	// name — and the plaintext address is left out, so the phone has exactly
	// one tailnet candidate rather than two whose winner depends on timing.
	got := Endpoints(EndpointInputs{
		LANHosts:       []string{"192.168.1.42"},
		TailscaleHosts: []string{"100.72.46.7"},
		Port:           3011,
		SecurePairing:  SecurePairingState{Enabled: true, Host: "mbp.tail057d04.ts.net", Port: 443},
		Tunnel:         &TunnelEndpoint{Ready: true, Hostname: "abc.trycloudflare.com"},
	})

	want := []Endpoint{
		{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
		{Kind: KindTailscale, Host: "mbp.tail057d04.ts.net", Port: 443, Secure: true},
		{Kind: KindTunnel, Host: "abc.trycloudflare.com", Port: 443, Secure: true},
	}
	assertEndpoints(t, got, want)
}

func TestEndpointsOmitsTheTailnetWhileSecurePairingIsOnButUnverified(t *testing.T) {
	// The mode is on but the proxy could not be confirmed — a tailscale CLI
	// call that timed out, or the moment between the listener binding and the
	// proxy being applied. The phone keeps a kind the list omits, whereas a
	// plaintext entry would replace the TLS one it already holds and leave an
	// iPhone with no tailnet candidate it can use. The other kinds are
	// unaffected, and a half-filled proxy counts as unverified.
	for _, sp := range []SecurePairingState{
		{Enabled: true},
		{Enabled: true, Host: "", Port: 443},
		{Enabled: true, Host: "mbp.tail057d04.ts.net", Port: 0},
	} {
		got := Endpoints(EndpointInputs{
			LANHosts:       []string{"192.168.1.42"},
			TailscaleHosts: []string{"100.72.46.7"},
			Port:           3011,
			SecurePairing:  sp,
			Tunnel:         &TunnelEndpoint{Ready: true, Hostname: "abc.trycloudflare.com"},
		})
		assertEndpoints(t, got, []Endpoint{
			{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
			{Kind: KindTunnel, Host: "abc.trycloudflare.com", Port: 443, Secure: true},
		})
	}
}

func TestEndpointsAdvertisesThePlaintextTailscaleAddressWhileSecurePairingIsOff(t *testing.T) {
	// Off is the only state in which the plaintext address is the tailnet
	// candidate; a stray proxy address with the mode off is ignored.
	for _, sp := range []SecurePairingState{{}, {Host: "mbp.tail057d04.ts.net", Port: 443}} {
		got := Endpoints(EndpointInputs{
			LANHosts:       []string{"192.168.1.42"},
			TailscaleHosts: []string{"100.72.46.7"},
			Port:           3011,
			SecurePairing:  sp,
		})
		assertEndpoints(t, got, []Endpoint{
			{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
			{Kind: KindTailscale, Host: "100.72.46.7", Port: 3011, Secure: false},
		})
	}
}

func TestEndpointsOmitsTheSecurePairingProxyWhileTheBridgeIsDown(t *testing.T) {
	// The proxy forwards to the bridge port, so with no listener bound there is
	// nothing behind the MagicDNS name either.
	got := Endpoints(EndpointInputs{
		SecurePairing: SecurePairingState{Enabled: true, Host: "mbp.tail057d04.ts.net", Port: 443},
	})
	assertEndpoints(t, got, nil)
}

func TestLocalEndpointsKeepsEveryCandidateFromEveryInterface(t *testing.T) {
	// This is the regression guard for the whole feature: AutopickLANIP and
	// AutopickTailscaleIP each returned one address and dropped the rest, so a
	// machine on Wi-Fi and Ethernet advertised only one of them.
	ifaces := []net.Interface{
		{Index: 1, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},
		{Index: 2, Name: "en0", Flags: net.FlagUp},
		{Index: 3, Name: "en1", Flags: net.FlagUp},
		{Index: 4, Name: "utun0", Flags: net.FlagUp},
	}
	addrs := map[string][]net.Addr{
		"lo0":   {cidr("127.0.0.1/8")},
		"en0":   {cidr("192.168.1.42/24")},
		"en1":   {cidr("10.0.0.5/24")},
		"utun0": {cidr("100.72.46.7/32")},
	}

	got := LocalEndpoints(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return addrs[i.Name], nil
	}, 3011, &TunnelEndpoint{Ready: true, Hostname: "abc.trycloudflare.com"})

	assertEndpoints(t, got, []Endpoint{
		{Kind: KindLAN, Host: "192.168.1.42", Port: 3011, Secure: false},
		{Kind: KindLAN, Host: "10.0.0.5", Port: 3011, Secure: false},
		{Kind: KindTailscale, Host: "100.72.46.7", Port: 3011, Secure: false},
		{Kind: KindTunnel, Host: "abc.trycloudflare.com", Port: 443, Secure: true},
	})
}
