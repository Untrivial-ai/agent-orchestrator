export type BrowserSurfaceRect = {
	x: number;
	y: number;
	width: number;
	height: number;
};

/**
 * A renderer measurement applied to the native browser surface.
 *
 * `sourceId` changes with each shell preload lifetime and `revision` is
 * monotonic within that lifetime. Together they prevent a delayed IPC message
 * from an older layout (or an older renderer after reload) from moving the
 * native WebContentsView after a newer layout has already won.
 */
export type BrowserSurfaceLayoutInput = {
	viewId: string;
	sourceId: string;
	revision: number;
	rect: BrowserSurfaceRect;
	visible: boolean;
};

export type BrowserSurfaceLayoutResult = {
	viewId: string;
	sourceId: string;
	revision: number;
	rect: BrowserSurfaceRect;
	visible: boolean;
	applied: boolean;
};

export type BrowserSurfaceLayoutReason =
	| "resize"
	| "move"
	| "maximize"
	| "unmaximize"
	| "enter-full-screen"
	| "leave-full-screen";

export type BrowserSurfaceLayoutSignal = {
	sequence: number;
	reason: BrowserSurfaceLayoutReason;
};

export function browserSurfaceRectsEqual(a: BrowserSurfaceRect, b: BrowserSurfaceRect): boolean {
	return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height;
}
