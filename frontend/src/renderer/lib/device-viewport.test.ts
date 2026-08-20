import { describe, expect, it } from "vitest";
import { isNearFrameEdge, pointerToFrame } from "./device-viewport";

const frame = { left: 100, top: 50, width: 400, height: 800 };

describe("pointerToFrame", () => {
	it("maps a pointer inside the rendered frame to framebuffer pixels", () => {
		// 400 CSS px wide maps to a 200px-wide framebuffer; 800 CSS px to 1600.
		const point = pointerToFrame(300, 250, frame, 200, 1600);
		expect(point).toEqual({ x: 100, y: 400 });
	});

	it("maps the exact corners", () => {
		expect(pointerToFrame(100, 50, frame, 200, 1600)).toEqual({ x: 0, y: 0 });
		expect(pointerToFrame(500, 850, frame, 200, 1600)).toEqual({ x: 200, y: 1600 });
	});

	it("returns null for clicks in the letterboxed margin around the frame", () => {
		expect(pointerToFrame(90, 250, frame, 200, 1600)).toBeNull(); // left margin
		expect(pointerToFrame(300, 900, frame, 200, 1600)).toBeNull(); // below frame
	});

	it("returns null when the frame or rect is degenerate", () => {
		expect(pointerToFrame(300, 250, frame, 0, 1600)).toBeNull();
		expect(pointerToFrame(300, 250, { left: 0, top: 0, width: 0, height: 0 }, 200, 1600)).toBeNull();
	});
});

describe("isNearFrameEdge", () => {
	const width = 1179;
	const height = 2556;

	it("flags points inside the device edge margin", () => {
		expect(isNearFrameEdge({ x: 0, y: 100 }, width, height)).toBe(true);
		expect(isNearFrameEdge({ x: 3, y: 100 }, width, height)).toBe(true);
		expect(isNearFrameEdge({ x: 100, y: 2 }, width, height)).toBe(true);
		expect(isNearFrameEdge({ x: 1178, y: 500 }, width, height)).toBe(true);
		expect(isNearFrameEdge({ x: 500, y: 2553 }, width, height)).toBe(true);
	});

	it("ignores points comfortably inside the frame", () => {
		expect(isNearFrameEdge({ x: 100, y: 500 }, width, height)).toBe(false);
		expect(isNearFrameEdge({ x: width / 2, y: height / 2 }, width, height)).toBe(false);
	});

	it("honors a custom margin in framebuffer points", () => {
		expect(isNearFrameEdge({ x: 20, y: 500 }, width, height, 24)).toBe(true);
		expect(isNearFrameEdge({ x: 20, y: 500 }, width, height, 4)).toBe(false);
	});

	it("returns false for degenerate frames or negative margins", () => {
		expect(isNearFrameEdge({ x: 0, y: 0 }, 0, 2556)).toBe(false);
		expect(isNearFrameEdge({ x: 0, y: 0 }, 1179, 0)).toBe(false);
		expect(isNearFrameEdge({ x: 0, y: 0 }, 1179, 2556, -1)).toBe(false);
	});
});