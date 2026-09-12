import { describe, expect, it } from "vitest";
import {
	buildCursorColorSchemeNotification,
	cursorColorSchemeReplyForOutput,
} from "./cursor-color-scheme";

describe("buildCursorColorSchemeNotification", () => {
	it("emits cursor-agent light/dark scheme notifications", () => {
		expect(buildCursorColorSchemeNotification("light")).toBe("\x1b[?997;2n");
		expect(buildCursorColorSchemeNotification("dark")).toBe("\x1b[?997;1n");
	});
});

describe("cursorColorSchemeReplyForOutput", () => {
	it("answers cursor-agent color-scheme probes with the live theme", () => {
		expect(cursorColorSchemeReplyForOutput("\x1b[?997;2n", "light")).toBe("\x1b[?997;2n");
		expect(cursorColorSchemeReplyForOutput("\x1b[?2031h", "light")).toBe("\x1b[?997;2n");
		expect(cursorColorSchemeReplyForOutput("\x1b[?2031h", "dark")).toBe("\x1b[?997;1n");
		expect(cursorColorSchemeReplyForOutput("hello", "light")).toBeNull();
	});
});
