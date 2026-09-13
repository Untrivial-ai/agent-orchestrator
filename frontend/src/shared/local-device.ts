export type LocalDevicePlatform = "ios" | "android";

export type LocalDevice = {
	id: string;
	name: string;
	platform: LocalDevicePlatform;
	kind: "simulator" | "emulator";
	booted: boolean;
	busy: boolean;
};

export type LocalDeviceCapability = {
	platform: LocalDevicePlatform;
	available: boolean;
	code?: string;
	message?: string;
};

export type LocalDeviceAttachment = {
	sessionId: string;
	deviceId: string;
	platform: LocalDevicePlatform;
	name: string;
};

export type LocalDeviceStatus = {
	sessionId: string;
	capabilities: LocalDeviceCapability[];
	attachment?: LocalDeviceAttachment;
};

export type LocalDeviceInventory = {
	sessionId: string;
	devices: LocalDevice[];
	errors?: LocalDeviceCapability[];
};

export type LocalDeviceCommand = {
	sessionId: string;
	action: "open" | "close" | "shutdown" | "screenshot" | "ui-tree" | "tap" | "swipe" | "fill" | "type" | "key" | "back" | "home";
	deviceId?: string;
	platform?: LocalDevicePlatform;
	interactiveOnly?: boolean;
	ref?: string;
	x?: number;
	y?: number;
	x1?: number;
	y1?: number;
	x2?: number;
	y2?: number;
	text?: string;
	key?: string;
	confirmed?: boolean;
};

export type LocalDeviceCommandResult = {
	sessionId: string;
	action: string;
	attachment?: LocalDeviceAttachment;
	result?: Record<string, unknown>;
};

export type LocalDeviceSetupState = "idle" | "awaiting_action" | "queued" | "downloading" | "installing" | "creating" | "verifying" | "succeeded" | "failed" | "canceled" | "interrupted";

export type LocalDeviceSetup = {
	platform: LocalDevicePlatform;
	state: LocalDeviceSetupState;
	stage?: string;
	message?: string;
	progress: number;
	downloadedBytes?: number;
	totalBytes?: number;
	requiredBytes?: number;
	availableBytes?: number;
	licenseUrl?: string;
	licenseAccepted: boolean;
	actionUrl?: string;
	errorCode?: string;
	error?: string;
	cancelable: boolean;
	retryable: boolean;
	installedVersion?: string;
};

export type LocalDeviceSetupStatus = { sessionId: string; setups: LocalDeviceSetup[] };
export type LocalDeviceSetupCommand = { sessionId: string; platform: LocalDevicePlatform; action: "start" | "retry" | "cancel"; licenseAccepted?: boolean };
export type LocalDeviceSetupResult = { sessionId: string; setup: LocalDeviceSetup };
