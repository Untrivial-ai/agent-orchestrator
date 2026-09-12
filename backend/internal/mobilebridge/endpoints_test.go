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
