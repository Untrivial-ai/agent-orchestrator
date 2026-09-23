import { describe, expect, it } from "vitest";

import { darkTheme, lightTheme } from "../theme";
import { composerSurfaceStyle, jumpToLatestColors, userMessageSurfaceStyle } from "./chatChrome";

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
			borderColor: darkTheme.borderDefault,
			foregroundColor: darkTheme.textPrimary,
		});
		expect(userMessageSurfaceStyle(lightTheme).backgroundColor).not.toBe(lightTheme.blue);
	});
});
