import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("../babel.config.js", import.meta.url), "utf8");

// CI runs `tsc --noEmit` and `vitest`, and neither bundles the app — so a Babel
// config that is wrong in a way only Metro would notice ships green. These
// assertions are the cheapest possible stand-in for a build step.

describe("babel config", () => {
	it("keeps the Expo preset", () => {
		expect(source).toContain("babel-preset-expo");
	});

	it("registers the worklets plugin that Reanimated 4 requires", () => {
		expect(source).toContain("react-native-worklets/plugin");
	});

	// Regression fence: the worklets plugin rewrites function bodies into
	// worklets, so anything that transforms those bodies has to run before it. A
	// plugin appended after this one sees code it does not expect, and the
	// failure shows up as an animation that silently runs on the JS thread.
	it("keeps the worklets plugin last", () => {
		const plugins = source.match(/plugins:\s*\[([\s\S]*?)\]/);
		expect(plugins).not.toBeNull();
		const entries = (plugins?.[1] ?? "")
			.split(",")
			.map((entry) => entry.trim())
			.filter(Boolean);
		expect(entries.at(-1)).toContain("react-native-worklets/plugin");
	});
});
