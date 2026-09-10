package mobilebridge

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// iface builds an interface fixture. The MAC is parsed here rather than via hw
// so these tests read as hardware, not as plumbing.
func iface(name, mac string) net.Interface {
	a, err := net.ParseMAC(mac)
	if err != nil {
		panic("bad MAC in test fixture: " + mac)
	}
	return net.Interface{Name: name, HardwareAddr: a}
}

func hw(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	a, err := net.ParseMAC(s)
	if err != nil {
		t.Fatalf("bad MAC %q: %v", s, err)
	}
	return a
}

func TestMachineFingerprintIgnoresInterfaceOrder(t *testing.T) {
	// net.Interfaces() makes no ordering guarantee across reboots. If the
	// fingerprint depended on order, a reboot would look like a different
	// machine and every paired phone would be told the host identity changed.
	a := []net.Interface{
		{Name: "en0", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:01")},
		{Name: "en1", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:02")},
	}
	b := []net.Interface{
		{Name: "en1", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:02")},
		{Name: "en0", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:01")},
	}

	if got, want := MachineFingerprint(b), MachineFingerprint(a); got != want {
		t.Fatalf("reordering interfaces changed the fingerprint: %q vs %q", got, want)
	}
	if MachineFingerprint(a) == "" {
		t.Fatal("fingerprint is empty for a machine with real interfaces")
	}
}

func TestMachineFingerprintDiffersBetweenMachines(t *testing.T) {
	a := MachineFingerprint([]net.Interface{{Name: "en0", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:01")}})
	b := MachineFingerprint([]net.Interface{{Name: "en0", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:02")}})
	if a == b {
		t.Fatalf("two machines share a fingerprint: %q", a)
	}
}

func TestMachineFingerprintIgnoresVirtualInterfaces(t *testing.T) {
	// Docker, VM, and VPN interfaces come and go and get fresh MACs each time.
	// Counting them would make the fingerprint change whenever Docker starts,
	// which would look like the config had been copied to another machine.
	physical := []net.Interface{{Name: "en0", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:01")}}
	withVirtual := []net.Interface{
		{Name: "en0", HardwareAddr: hw(t, "aa:bb:cc:dd:ee:01")},
		{Name: "docker0", HardwareAddr: hw(t, "02:42:9a:00:00:01")},
		{Name: "vmnet1", HardwareAddr: hw(t, "00:50:56:c0:00:01")},
		{Name: "bridge100", HardwareAddr: hw(t, "36:7d:da:80:00:64")},
		{Name: "utun4", HardwareAddr: hw(t, "aa:00:00:00:00:99")},
	}

	if got, want := MachineFingerprint(withVirtual), MachineFingerprint(physical); got != want {
		t.Fatalf("virtual interfaces changed the fingerprint: %q vs %q", got, want)
	}
}

func TestMachineFingerprintEmptyWhenNoHardwareAddresses(t *testing.T) {
	// A container with only loopback. Callers must treat "" as "cannot bind an
	// identity to this machine" rather than as a valid fingerprint.
	got := MachineFingerprint([]net.Interface{{Name: "lo0", Flags: net.FlagLoopback}})
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestEnsureIdentityCreatesAndPersistsAHostID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile", "identity.json")

	got, err := EnsureIdentityFor(path, []net.Interface{iface("en0", "aa:bb:cc:dd:ee:01")})
	if err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	if got.HostID == "" {
		t.Fatal("no host id generated")
	}
	if !strings.HasPrefix(got.HostID, "h_") {
		t.Fatalf("host id %q lacks the h_ prefix", got.HostID)
	}

	// It must survive a daemon restart, or every restart would invalidate every
	// paired phone's stored host identity.
	again, err := EnsureIdentityFor(path, []net.Interface{iface("en0", "aa:bb:cc:dd:ee:01")})
	if err != nil {
		t.Fatalf("second EnsureIdentity: %v", err)
	}
	if again.HostID != got.HostID {
		t.Fatalf("host id changed across restart: %q then %q", got.HostID, again.HostID)
	}
}

func TestEnsureIdentityWritesOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile", "identity.json")
	if _, err := EnsureIdentityFor(path, []net.Interface{iface("en0", "aa:bb:cc:dd:ee:01")}); err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("identity file mode %o, want 600", perm)
	}
}

// Identity belongs to the AO installation. Hardware fingerprints remain useful
// diagnostics, but plugging in a dock must not reissue the host id and silently
// unpair every phone.
func TestEnsureIdentityKeepsTheHostIDWhenAnInterfaceIsAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile", "identity.json")
	laptop := []net.Interface{iface("en0", "aa:bb:cc:dd:ee:01")}

	original, err := EnsureIdentityFor(path, laptop)
	if err != nil {
		t.Fatalf("EnsureIdentityFor: %v", err)
	}

	// Same laptop, now with a dock attached.
	docked := append(laptop, iface("en5", "aa:bb:cc:dd:ee:99"))
	got, err := EnsureIdentityFor(path, docked)
	if err != nil {
		t.Fatalf("EnsureIdentityFor docked: %v", err)
	}

	if got.HostID != original.HostID {
		t.Fatalf("host id changed when a NIC was added: %q -> %q", original.HostID, got.HostID)
	}
}

func TestEnsureIdentityKeepsTheHostIDWhenAnInterfaceIsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile", "identity.json")
	docked := []net.Interface{
		iface("en0", "aa:bb:cc:dd:ee:01"),
		iface("en5", "aa:bb:cc:dd:ee:99"),
	}

	original, err := EnsureIdentityFor(path, docked)
	if err != nil {
		t.Fatalf("EnsureIdentityFor: %v", err)
	}

	undocked, err := EnsureIdentityFor(path, docked[:1])
	if err != nil {
		t.Fatalf("EnsureIdentityFor undocked: %v", err)
	}

	if undocked.HostID != original.HostID {
		t.Fatalf("host id changed when a NIC was removed: %q -> %q", original.HostID, undocked.HostID)
	}
}

// Host identity belongs to the AO installation, not to today's network card.
// Replacing the only NIC must not silently unpair every phone.
func TestEnsureIdentityKeepsTheHostIDWhenTheOnlyInterfaceIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile", "identity.json")

	original, err := EnsureIdentityFor(path, []net.Interface{iface("en0", "aa:bb:cc:dd:ee:01")})
	if err != nil {
		t.Fatalf("EnsureIdentityFor: %v", err)
	}

	moved, err := EnsureIdentityFor(path, []net.Interface{iface("en0", "11:22:33:44:55:66")})
	if err != nil {
		t.Fatalf("EnsureIdentityFor moved: %v", err)
	}

	if moved.HostID != original.HostID {
		t.Fatalf("host id changed after NIC replacement: %q -> %q", original.HostID, moved.HostID)
	}
}

// An installation with no hardware addresses must not reissue its id on every
// start.
func TestEnsureIdentityIsStableWithoutHardwareAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile", "identity.json")

	first, err := EnsureIdentityFor(path, nil)
	if err != nil {
		t.Fatalf("EnsureIdentityFor: %v", err)
	}
	second, err := EnsureIdentityFor(path, nil)
	if err != nil {
		t.Fatalf("EnsureIdentityFor again: %v", err)
	}

	if first.HostID != second.HostID {
		t.Fatalf("host id churned with no interfaces: %q -> %q", first.HostID, second.HostID)
	}
}
