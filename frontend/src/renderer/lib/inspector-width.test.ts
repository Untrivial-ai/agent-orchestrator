import { describe, expect, it } from "vitest";
import {
	INSPECTOR_SEPARATOR_RESERVE_PX,
	inspectorMaxWidthCss,
	inspectorMaxWidthPx,
	WORKSPACE_ABSOLUTE_MIN_PX,
} from "./inspector-width";

describe("inspectorMaxWidthPx", () => {
	it("caps by percent, chat floor, and available width", () => {
		// available 1000: min(1000, 550, max(300, 440)) = 440
		expect(inspectorMaxWidthPx(1000, 55, 560)).toBe(440);
	});

	it("uses browser-mode percent and chat floor", () => {
		// available 1000: min(1000, 680, max(300, 560)) = 560
		expect(inspectorMaxWidthPx(1000, 68, 440)).toBe(560);
	});

	it("returns undefined for empty available width", () => {
		expect(inspectorMaxWidthPx(0)).toBeUndefined();
		expect(inspectorMaxWidthPx(undefined)).toBeUndefined();
	});
});

describe("inspectorMaxWidthCss", () => {
	it("serialises the CSS expression SessionView binds on the split", () => {
		expect(inspectorMaxWidthCss(55, 560)).toBe(
			`min(55%, max(${WORKSPACE_ABSOLUTE_MIN_PX}px, calc(100% - 560px)))`,
		);
	});
});

describe("INSPECTOR_SEPARATOR_RESERVE_PX", () => {
	it("stays the reserved separator width used by live max callbacks", () => {
		expect(INSPECTOR_SEPARATOR_RESERVE_PX).toBe(8);
	});
});
