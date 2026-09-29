import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const frontendDir = resolve(dirname(scriptPath), "..");
const viteBin = resolve(frontendDir, "node_modules/vite/bin/vite.js");
const viteConfig = resolve(frontendDir, "vite.renderer.config.ts");

export function createDevWebLaunch(args = []) {
	return {
		command: process.execPath,
		args: [viteBin, "--config", viteConfig, ...args],
		options: {
			cwd: frontendDir,
			env: { ...process.env, VITE_NO_ELECTRON: "1" },
			stdio: "inherit",
			windowsHide: true,
		},
	};
}

export function runDevWeb(args = process.argv.slice(2)) {
	const launch = createDevWebLaunch(args);
	const child = spawn(launch.command, launch.args, launch.options);

	child.once("exit", (code, signal) => {
		if (signal && process.platform !== "win32") {
			process.kill(process.pid, signal);
			return;
		}
		process.exitCode = code ?? 1;
	});
	child.once("error", (error) => {
		console.error(error);
		process.exitCode = 1;
	});

	return child;
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
	runDevWeb();
}
