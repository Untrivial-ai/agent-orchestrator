export type BrowserLiveFrame = {
	sequence: bigint;
	capturedAtMs: bigint;
	width: number;
	height: number;
	jpeg: Buffer;
};

export type BrowserLiveTab = { id: string; title: string; url: string; active: boolean };

export type BrowserLiveState = {
	url: string;
	title: string;
	loading: boolean;
	canGoBack: boolean;
	canGoForward: boolean;
	activeTabId: string;
	tabs: BrowserLiveTab[];
	width: number;
	height: number;
};

export type BrowserLiveSink = {
	frame(frame: BrowserLiveFrame): void;
	state(state: BrowserLiveState): void;
	error(code: string, message: string): void;
};

export type BrowserRemoteInput =
	| { kind: "pointer"; phase: "move" | "down" | "up"; x: number; y: number; button?: "left" | "middle" | "right" }
	| { kind: "wheel"; deltaX: number; deltaY: number }
	| { kind: "key"; phase: "down" | "up"; key: string; code: string; modifiers?: string[] }
	| { kind: "text"; value: string };

export type BrowserRemoteNavigation = {
	action: "open" | "back" | "forward" | "reload" | "stop";
	url?: string;
};

export type BrowserRemoteTabAction = {
	action: "new" | "select" | "close";
	tabId?: string;
	url?: string;
};
