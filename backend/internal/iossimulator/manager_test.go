package iossimulator

import "testing"

func TestSessionRegistryKeepsOwnershipPerSession(t *testing.T) {
	created := 0
	registry := NewSessionRegistry(func() *Manager {
		created++
		return NewWithRunner(func(string, ...string) ([]byte, error) { return nil, nil })
	})
	first := registry.For("session-a")
	if first != registry.For("session-a") {
		t.Fatal("same session must reuse its manager")
	}
	if first == registry.For("session-b") {
		t.Fatal("different sessions must not share a manager")
	}
	if created != 2 {
		t.Fatalf("created %d managers, want 2", created)
	}
}

func TestManagerStartCreatesAndBootsDevice(t *testing.T) {
	var calls []string
	m := NewWithRunner(func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+join(args))
		switch join(args) {
		case "simctl list devicetypes -j":
			return []byte(`{"devicetypes":[{"identifier":"iphone.old","name":"iPhone 14"},{"identifier":"iphone.new","name":"iPhone 15"}]}`), nil
		case "simctl list runtimes -j":
			return []byte(`{"runtimes":[{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-17-0","isAvailable":true},{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-18-0","isAvailable":true}]}`), nil
		case "simctl create AO iPhone iphone.new com.apple.CoreSimulator.SimRuntime.iOS-18-0":
			return []byte("device-1\n"), nil
		case "simctl list devices -j":
			return []byte(`{"devices":{"runtime":[{"udid":"device-1","state":"Shutdown"}]}}`), nil
		default:
			return nil, nil
		}
	})
	status, err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	if status.DeviceID != "device-1" || status.State != "Booted" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if len(calls) == 0 {
		t.Fatal("expected simctl calls")
	}
}

func TestManagerStartDeviceUsesExplicitUDID(t *testing.T) {
	var booted string
	m := NewWithRunner(func(name string, args ...string) ([]byte, error) {
		if join(args) == "simctl list devices -j" {
			return []byte(`{"devices":{"runtime":[{"udid":"chosen","state":"Shutdown"}]}}`), nil
		}
		if join(args) == "simctl boot chosen" {
			booted = "chosen"
		}
		return nil, nil
	})
	status, err := m.StartDevice("chosen")
	if err != nil {
		t.Fatal(err)
	}
	if booted != "chosen" || status.DeviceID != "chosen" || status.State != "Booted" {
		t.Fatalf("unexpected explicit start: %+v booted=%q", status, booted)
	}
}

func join(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += " "
		}
		result += part
	}
	return result
}
