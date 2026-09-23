import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = fileURLToPath(new URL(".", import.meta.url));
const roots = [here, `${here}../app`];

function files(dir: string): string[] {
	return readdirSync(dir).flatMap((entry) => {
		const path = `${dir}/${entry}`;
		if (statSync(path).isDirectory()) return entry === "node_modules" ? [] : files(path);
		return /\.tsx?$/.test(entry) ? [path] : [];
	});
}

describe("leaving a screen", () => {
	// `router.back()` dispatches GO_BACK, and a navigator with an empty stack does
	// not handle it: the control does nothing, the app logs "The action 'GO_BACK'
	// was not handled", and the screen has no other exit. A deep link, a
	// notification tap that cold-starts the app, and a sheet opened directly all
	// arrive with no history, so every exit goes through `backOr`.
	it("never calls back() without asking whether there is somewhere to go", () => {
		const offenders: string[] = [];
		for (const root of roots) {
			for (const path of files(root)) {
				if (path.includes("backNavigation")) continue;
				const src = readFileSync(path, "utf8");
				for (const [index, line] of src.split("\n").entries()) {
					if (!/\.(back|goBack)\(\)/.test(line)) continue;
					// A guard on the same line, or one immediately above it, is fine —
					// that is the inline form of the same rule.
					const previous = src.split("\n")[index - 1] ?? "";
					if (/canGoBack\(\)/.test(line) || /canGoBack\(\)/.test(previous)) continue;
					offenders.push(`${path.replace(here, "")}:${index + 1}`);
				}
			}
		}
		expect(offenders).toEqual([]);
	});
});
