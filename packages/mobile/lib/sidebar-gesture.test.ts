import { describe, expect, it } from "vitest";
import { sidebarGestureTarget, shouldCaptureSidebarGesture } from "./sidebar-gesture";

describe("sidebar edge gesture", () => {
	it("captures an inward horizontal drag that begins at the left edge", () => {
		expect(shouldCaptureSidebarGesture({ open: false, startX: 18, dx: 14, dy: 2 })).toBe(true);
	});

	it("leaves scrolling and drags away from the edge alone while closed", () => {
		expect(shouldCaptureSidebarGesture({ open: false, startX: 70, dx: 40, dy: 2 })).toBe(false);
		expect(shouldCaptureSidebarGesture({ open: false, startX: 18, dx: 4, dy: 20 })).toBe(false);
	});

	it("supports a wider Android activation strip outside the system back edge", () => {
		expect(shouldCaptureSidebarGesture({ open: false, startX: 52, dx: 18, dy: 2, edgeWidth: 64 })).toBe(true);
	});

	it("captures a leftward horizontal drag to dismiss an open sidebar", () => {
		expect(shouldCaptureSidebarGesture({ open: true, startX: 280, dx: -14, dy: 2 })).toBe(true);
	});

	it("opens after a committed drag or quick inward flick", () => {
		expect(sidebarGestureTarget({ open: false, dx: 170, velocityX: 0.1, drawerWidth: 340 })).toBe(true);
		expect(sidebarGestureTarget({ open: false, dx: 45, velocityX: 0.8, drawerWidth: 340 })).toBe(true);
	});

	it("closes after a committed drag or quick outward flick", () => {
		expect(sidebarGestureTarget({ open: true, dx: -190, velocityX: -0.1, drawerWidth: 340 })).toBe(false);
		expect(sidebarGestureTarget({ open: true, dx: -35, velocityX: -0.8, drawerWidth: 340 })).toBe(false);
	});
});
