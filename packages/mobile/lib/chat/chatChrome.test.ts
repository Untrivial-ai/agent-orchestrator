import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { darkTheme, lightTheme } from "../theme";
import { composerSurfaceStyle, jumpToLatestColors, userMessageSurfaceStyle } from "./chatChrome";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

describe("chat chrome theme styling", () => {
	it("themes both parts of the jump-to-latest control", () => {
		expect(jumpToLatestColors(darkTheme)).toEqual({
			backgroundColor: darkTheme.bgElevated,
			foregroundColor: darkTheme.textPrimary,
		});
		expect(jumpToLatestColors(lightTheme)).toEqual({
			backgroundColor: lightTheme.bgElevated,
			foregroundColor: lightTheme.textPrimary,
		});
	});

	it("keeps the message composer borderless", () => {
		expect(composerSurfaceStyle(darkTheme)).not.toHaveProperty("borderWidth");
		expect(composerSurfaceStyle(lightTheme)).not.toHaveProperty("borderColor");
	});

	it("uses a neutral elevated surface for user messages instead of the action color", () => {
		expect(userMessageSurfaceStyle(darkTheme)).toEqual({
			backgroundColor: darkTheme.bgElevated,
			foregroundColor: darkTheme.textPrimary,
		});
		expect(userMessageSurfaceStyle(lightTheme).backgroundColor).not.toBe(lightTheme.accent);
	});

	// The bubble is a filled shape on the page now, not a bordered one: the
	// hairline read as a second edge around something already lifted by its fill.
	it("leaves the user message bubble without a border", () => {
		expect(userMessageSurfaceStyle(darkTheme)).not.toHaveProperty("borderColor");
		expect(source("./ChatTimeline.tsx")).not.toMatch(/userBubble: \{[^}]*borderWidth/);
	});

	it("drops the beginning-of-conversation label", () => {
		expect(source("./ChatTimeline.tsx")).not.toContain("Beginning of conversation");
	});
});
