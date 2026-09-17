import { describe, expect, it } from "vitest";
import { formatTimeTerse } from "./format-time";

describe("formatTimeTerse", () => {
	const now = new Date("2026-08-26T12:00:00Z");

	it.each([
		["2026-08-26T11:59:30Z", "now"],
		["2026-08-26T11:55:00Z", "5m"],
		["2026-08-26T09:00:00Z", "3h"],
		["2026-08-14T12:00:00Z", "12d"],
	])("formats %s as %s", (timestamp, expected) => {
		expect(formatTimeTerse(timestamp, now)).toBe(expected);
	});
});
