import { readFile } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";

const repositoryRoot = path.resolve(import.meta.dirname, "..");

// npm runs scripts through cmd.exe on Windows, which has no `VAR=value command`
// syntax: such a script dies with "'VAR' is not recognized as an internal or
// external command" before the tool it prefixes ever starts. The prefix works on
// macOS and Linux and CI runs Linux only, so nothing else catches it — `dev:web`
// carried one for months and took `npm run test:e2e` down with it on Windows.
//
// Pass the value through the tool's own interface instead (vite reads `--mode`,
// and the renderer config turns that into the define), or read it from a config
// file. Matching only at a command boundary keeps flag values such as
// `--path-mode=abs` and `-o src/api/schema.ts` out of the net.
const POSIX_ENV_PREFIX = /(?:^|&&\s*|\|\|\s*|;\s*|\|\s*)[A-Za-z_][A-Za-z0-9_]*=/;

describe("npm scripts stay cross-platform", () => {
	for (const manifest of ["package.json", "frontend/package.json"]) {
		it(`${manifest} sets no environment variable with a POSIX prefix`, async () => {
			const contents = await readFile(path.join(repositoryRoot, manifest), "utf8");
			const scripts: Record<string, string> = JSON.parse(contents).scripts ?? {};

			const offenders = Object.entries(scripts)
				.filter(([, command]) => POSIX_ENV_PREFIX.test(command))
				.map(([name, command]) => `${name}: ${command}`);

			expect(offenders).toEqual([]);
		});
	}
});
