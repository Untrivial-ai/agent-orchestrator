package mobilebridge

import (
	"net"
	"testing"
)

func TestPrivateIPv4Candidates(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 1, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},
		{Index: 2, Name: "en0", Flags: net.FlagUp},
		{Index: 3, Name: "utun3", Flags: net.FlagUp}, // VPN — skip
		{Index: 4, Name: "en5", Flags: 0},            // down — skip
	}
	addrs := map[string][]net.Addr{
		"lo0":   {cidr("127.0.0.1/8")},
		"en0":   {cidr("192.168.1.42/24"), cidr("fe80::1/64")},
		"utun3": {cidr("10.9.9.9/24")},
		"en5":   {cidr("192.168.5.5/24")},
	}
	got := PrivateIPv4Candidates(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return addrs[i.Name], nil
	})
	if len(got) != 1 || got[0] != "192.168.1.42" {
		t.Fatalf("got %v want [192.168.1.42]", got)
	}
}

func TestTailscaleIPv4Candidates(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 1, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback}, // loopback — skip
		{Index: 2, Name: "en0", Flags: net.FlagUp},                    // carrier NAT — skip
		{Index: 3, Name: "utun1", Flags: net.FlagUp},                  // other VPN — skip
		{Index: 4, Name: "utun0", Flags: net.FlagUp},                  // Tailscale — keep
		{Index: 5, Name: "utun9", Flags: 0},                           // down — skip
	}
	addrs := map[string][]net.Addr{
		"lo0":   {cidr("127.0.0.1/8")},
		"en0":   {cidr("100.90.1.1/24")},
		"utun1": {cidr("10.9.9.9/24")},
		"utun0": {cidr("100.72.46.7/32"), cidr("fd7a:115c:a1e0::1/128")},
		"utun9": {cidr("100.80.0.1/32")},
	}
	got := TailscaleIPv4Candidates(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return addrs[i.Name], nil
	})
	if len(got) != 1 || got[0] != "100.72.46.7" {
		t.Fatalf("got %v want [100.72.46.7]", got)
	}
}

func TestTailscaleIPv4CandidatesEmptyWhenTailscaleDown(t *testing.T) {
	ifaces := []net.Interface{{Index: 1, Name: "en0", Flags: net.FlagUp}}
	addrs := map[string][]net.Addr{"en0": {cidr("192.168.1.42/24")}}
	got := TailscaleIPv4Candidates(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return addrs[i.Name], nil
	})
	if len(got) != 0 {
		t.Fatalf("got %v want []", got)
	}
}

// The LAN picker must keep ignoring Tailscale, or enabling Tailscale would
// silently change which address the LAN tab of the pairing modal advertises.
func TestPrivateIPv4CandidatesStillIgnoresTailscale(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 1, Name: "en0", Flags: net.FlagUp},
		{Index: 2, Name: "utun0", Flags: net.FlagUp},
	}
	addrs := map[string][]net.Addr{
		"en0":   {cidr("192.168.1.42/24")},
		"utun0": {cidr("100.72.46.7/32")},
	}
	got := PrivateIPv4Candidates(ifaces, func(i net.Interface) ([]net.Addr, error) {
		return addrs[i.Name], nil
	})
	if len(got) != 1 || got[0] != "192.168.1.42" {
		t.Fatalf("got %v want [192.168.1.42]", got)
	}
}

func cidr(s string) net.Addr {
	ip, ipnet, _ := net.ParseCIDR(s)
	ipnet.IP = ip
	return ipnet
}
