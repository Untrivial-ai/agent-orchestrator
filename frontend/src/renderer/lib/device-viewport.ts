/**
 * Emulator pointer mapping.
 *
 * The wire contract for simulator input is "device framebuffer pixels": the
 * backend maps those onto the Simulator window's screen geometry itself, so the
 * panel only ever needs to translate a pointer event into framebuffer
 * coordinates. That translation must account for the panel's own scaling and
 * letterboxing — the frame is rendered contain-fit inside a stage, and pointer
 * events land in CSS pixels, not device pixels.
 */

export type DeviceRect = {
	readonly left: number;
	readonly top: number;
	readonly width: number;
	readonly height: number;
};

export type FramePoint = {
	readonly x: number;
	readonly y: number;
};

/**
 * Map a pointer event (CSS px, relative to the viewport) into device
 * framebuffer pixels using the rendered frame's bounding rect.
 *
 * Returns null when the pointer is outside the rendered frame, so clicks in the
 * letterboxed margin around the device never reach the simulator.
 */
export function pointerToFrame(
	clientX: number,
	clientY: number,
	deviceRect: DeviceRect,
	frameWidth: number,
	frameHeight: number,
): FramePoint | null {
	if (frameWidth <= 0 || frameHeight <= 0 || deviceRect.width <= 0 || deviceRect.height <= 0) {
		return null;
	}
	const x = clientX - deviceRect.left;
	const y = clientY - deviceRect.top;
	if (x < 0 || y < 0 || x > deviceRect.width || y > deviceRect.height) {
		return null;
	}
	return {
		x: (x / deviceRect.width) * frameWidth,
		y: (y / deviceRect.height) * frameHeight,
	};
}

/**
 * Edge band from which a drag is treated as a system-gesture risk. On devices
 * without a physical Home button, swipes that begin inside this margin trigger
 * Home Indicator / Control Center / Notification Center system gestures instead
 * of being delivered to the app, so the panel warns the user before sending such
 * a swipe. The margin is expressed in device framebuffer points.
 */
export const DEVICE_EDGE_GESTURE_MARGIN_PT = 4;

/**
 * Returns true when a framebuffer point lies within the given margin of the
 * device screen edge — the zone iOS reserves for system gestures. The frame
 * dimensions are the on-screen framebuffer size (1179x2556 etc.), the same
 * space `pointerToFrame` produces.
 */
export function isNearFrameEdge(
	point: FramePoint,
	frameWidth: number,
	frameHeight: number,
	marginPt: number = DEVICE_EDGE_GESTURE_MARGIN_PT,
): boolean {
	if (frameWidth <= 0 || frameHeight <= 0 || marginPt < 0) {
		return false;
	}
	return (
		point.x <= marginPt ||
		point.y <= marginPt ||
		point.x >= frameWidth - marginPt ||
		point.y >= frameHeight - marginPt
	);
}