package domain

// DevicePlatform is a public mobile virtual-device platform.
type DevicePlatform string

const (
	// DevicePlatformIOS selects Apple's iOS Simulator platform.
	DevicePlatformIOS DevicePlatform = "ios"
	// DevicePlatformAndroid selects Google's Android Emulator platform.
	DevicePlatformAndroid DevicePlatform = "android"
)

// DeviceKind distinguishes Apple's simulator from Android's emulator.
type DeviceKind string

const (
	// DeviceKindSimulator identifies an Apple simulator.
	DeviceKindSimulator DeviceKind = "simulator"
	// DeviceKindEmulator identifies an Android emulator.
	DeviceKindEmulator DeviceKind = "emulator"
)

// Device is a live inventory fact returned by the selected vendor toolchain.
type Device struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Platform DevicePlatform `json:"platform"`
	Kind     DeviceKind     `json:"kind"`
	Booted   bool           `json:"booted"`
	Busy     bool           `json:"busy"`
}

// DevicePlatformCapability explains whether one platform is usable without
// letting a missing toolchain disable the other platform.
type DevicePlatformCapability struct {
	Platform  DevicePlatform `json:"platform"`
	Available bool           `json:"available"`
	Code      string         `json:"code,omitempty"`
	Message   string         `json:"message,omitempty"`
}

// DeviceAttachment binds one AO session to one controlling virtual device.
type DeviceAttachment struct {
	SessionID string         `json:"sessionId"`
	DeviceID  string         `json:"deviceId"`
	Platform  DevicePlatform `json:"platform"`
	Name      string         `json:"name"`
}
