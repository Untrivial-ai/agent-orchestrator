import { spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { createDevWebLaunch } from "./dev-web.mjs";

const scriptsDir = import.meta.dirname;
const frontendDir = path.resolve(scriptsDir, "..");

describe("dev:web launcher", () => {
	it("sets preview mode without changing Vite's development mode", () => {
		const launch = createDevWebLaunch(["--port", "5199"]);

		expect(launch.command).toBe(process.execPath);
		expect(launch.args).toEqual([
			path.resolve(frontendDir, "node_modules/vite/bin/vite.js"),
			"--config",
			path.resolve(frontendDir, "vite.renderer.config.ts"),
			"--port",
			"5199",
		]);
		expect(launch.args).not.toContain("--mode");
		expect(launch.options.cwd).toBe(frontendDir);
		expect(launch.options.env.VITE_NO_ELECTRON).toBe("1");
	});

	it("is the package script and starts Vite successfully", async () => {
		const manifest = JSON.parse(await readFile(path.join(frontendDir, "package.json"), "utf8"));
		expect(manifest.scripts["dev:web"]).toBe("node ./scripts/dev-web.mjs");

		const result = spawnSync(process.execPath, [path.join(scriptsDir, "dev-web.mjs"), "--version"], {
			cwd: path.resolve(frontendDir, ".."),
			encoding: "utf8",
			timeout: 20_000,
		});

		expect(result.error).toBeUndefined();
		expect(result.status).toBe(0);
		expect(`${result.stdout}\n${result.stderr}`).toMatch(/vite/i);
	});
});
