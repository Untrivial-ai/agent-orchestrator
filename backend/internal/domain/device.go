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

// DeviceSetupState is the durable lifecycle of one managed platform setup.
type DeviceSetupState string

const (
	DeviceSetupIdle           DeviceSetupState = "idle"
	DeviceSetupAwaitingAction DeviceSetupState = "awaiting_action"
	DeviceSetupQueued         DeviceSetupState = "queued"
	DeviceSetupDownloading    DeviceSetupState = "downloading"
	DeviceSetupInstalling     DeviceSetupState = "installing"
	DeviceSetupCreating       DeviceSetupState = "creating"
	DeviceSetupVerifying      DeviceSetupState = "verifying"
	DeviceSetupSucceeded      DeviceSetupState = "succeeded"
	DeviceSetupFailed         DeviceSetupState = "failed"
	DeviceSetupCanceled       DeviceSetupState = "canceled"
	DeviceSetupInterrupted    DeviceSetupState = "interrupted"
)

// DeviceSetup reports the latest durable managed-setup state for one platform.
type DeviceSetup struct {
	Platform         DevicePlatform   `json:"platform"`
	State            DeviceSetupState `json:"state"`
	Stage            string           `json:"stage,omitempty"`
	Message          string           `json:"message,omitempty"`
	Progress         int              `json:"progress" minimum:"0" maximum:"100"`
	DownloadedBytes  int64            `json:"downloadedBytes,omitempty" minimum:"0"`
	TotalBytes       int64            `json:"totalBytes,omitempty" minimum:"0"`
	RequiredBytes    int64            `json:"requiredBytes,omitempty" minimum:"0"`
	AvailableBytes   int64            `json:"availableBytes,omitempty" minimum:"0"`
	LicenseURL       string           `json:"licenseUrl,omitempty"`
	LicenseAccepted  bool             `json:"licenseAccepted"`
	ActionURL        string           `json:"actionUrl,omitempty"`
	ErrorCode        string           `json:"errorCode,omitempty"`
	Error            string           `json:"error,omitempty"`
	Cancelable       bool             `json:"cancelable"`
	Retryable        bool             `json:"retryable"`
	InstalledVersion string           `json:"installedVersion,omitempty"`
}
