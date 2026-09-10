package mobilebridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
)

// MachineFingerprint records a diagnostic snapshot of this machine's hardware
// addresses. It never decides HostID: hardware changes must not silently
// unpair every phone, and copying AO_DATA_DIR intentionally copies identity.
//
// Deliberately independent of interface order (net.Interfaces() gives no
// ordering guarantee) and of link state (Wi-Fi being off must not look like a
// different machine).
func MachineFingerprint(ifaces []net.Interface) string {
	var macs []string
	for _, i := range ifaces {
		if len(i.HardwareAddr) == 0 || hasVirtualName(i.Name) {
			continue
		}
		macs = append(macs, i.HardwareAddr.String())
	}
	if len(macs) == 0 {
		return ""
	}
	sort.Strings(macs)
	h := sha256.New()
	for _, m := range macs {
		h.Write([]byte(m))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Identity is the daemon installation's stable identity as seen by paired
// phones. It is not a secret: the phone compares the HostID it paired with
// against the one /api/v1/identity reports, so a private address reused on a
// different network cannot be mistaken for this AO installation.
type Identity struct {
	HostID string `json:"hostId"`
	// Fingerprint is the aggregate MachineFingerprint this HostID was issued
	// for. Retained for identities written before Fingerprints existed.
	Fingerprint string `json:"fingerprint"`
	// Fingerprints is one hash per hardware interface.
	//
	// These are diagnostic metadata. They are updated as hardware changes but
	// never rotate HostID; identity belongs to this AO data directory.
	Fingerprints []string `json:"fingerprints,omitempty"`
}

// IdentityPath returns the identity file location under the data dir
// (~/.ao/mobile/identity.json).
func IdentityPath(dataDir string) string {
	return filepath.Join(dataDir, "mobile", "identity.json")
}

// InterfaceFingerprints hashes each hardware interface separately, skipping
// virtual ones exactly as MachineFingerprint does.
func InterfaceFingerprints(ifaces []net.Interface) []string {
	var out []string
	for _, i := range ifaces {
		if len(i.HardwareAddr) == 0 || hasVirtualName(i.Name) {
			continue
		}
		h := sha256.Sum256([]byte(i.HardwareAddr.String()))
		out = append(out, hex.EncodeToString(h[:])[:32])
	}
	sort.Strings(out)
	return out
}

// EnsureIdentityFor loads the installation identity at path. Hardware hashes
// are diagnostic metadata only: docks and NIC replacements must not silently
// unpair every phone. Copying AO_DATA_DIR therefore copies the identity too;
// resetting it is an explicit operation (remove identity.json), never an
// inference made from today's network interfaces.
func EnsureIdentityFor(path string, ifaces []net.Interface) (Identity, error) {
	current := InterfaceFingerprints(ifaces)
	aggregate := MachineFingerprint(ifaces)

	if b, err := os.ReadFile(path); err == nil {
		var existing Identity
		if json.Unmarshal(b, &existing) == nil && existing.HostID != "" {
			if !equalStrings(existing.Fingerprints, current) || existing.Fingerprint != aggregate {
				existing.Fingerprints = current
				existing.Fingerprint = aggregate
				if err := writeIdentity(path, existing); err != nil {
					return Identity{}, err
				}
			}
			return existing, nil
		}
	} else if !os.IsNotExist(err) {
		return Identity{}, fmt.Errorf("read mobile identity: %w", err)
	}

	issued := Identity{
		HostID:       "h_" + uuid.NewString(),
		Fingerprint:  aggregate,
		Fingerprints: current,
	}
	if err := writeIdentity(path, issued); err != nil {
		return Identity{}, err
	}
	return issued, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeIdentity(path string, id Identity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir mobile dir: %w", err)
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write mobile identity: %w", err)
	}
	return nil
}

// EnsureLocalIdentity is the production entry point: this machine's identity,
// bound to its real interfaces, under the given data dir. A thin wrapper over
// the tested EnsureIdentity/MachineFingerprint pair, in the style of
// AutopickLANIP.
func EnsureLocalIdentity(dataDir string) (Identity, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return Identity{}, fmt.Errorf("read interfaces: %w", err)
	}
	return EnsureIdentityFor(IdentityPath(dataDir), ifaces)
}
