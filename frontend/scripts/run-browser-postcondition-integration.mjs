import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createRequire } from "node:module";
import { build } from "vite";

const require = createRequire(import.meta.url);
const electron = require("electron");
const frontendRoot = path.resolve(import.meta.dirname, "..");
const outputDirectory = await mkdtemp(path.join(os.tmpdir(), "ao-browser-postcondition-"));
const entry = path.join(frontendRoot, "src", "main", "browser-action-postconditions.integration.ts");
const fixture = path.join(frontendRoot, "test", "fixtures", "browser-action-postconditions", "guarded-navigation.html");

async function terminateProcessTree(child) {
	if (!child.pid || child.exitCode !== null || child.signalCode !== null) return;
	if (process.platform === "win32") {
		await new Promise((resolve) => {
			const killer = spawn("taskkill", ["/pid", String(child.pid), "/t", "/f"], {
				stdio: "ignore",
				windowsHide: true,
			});
			let settled = false;
			const finish = () => {
				if (settled) return;
				settled = true;
				clearTimeout(timeout);
				resolve();
			};
			const timeout = setTimeout(() => {
				killer.kill("SIGKILL");
				child.kill("SIGKILL");
				finish();
			}, 5_000);
			killer.once("error", () => {
				child.kill("SIGKILL");
				finish();
			});
			killer.once("exit", finish);
		});
		return;
	}
	try {
		process.kill(-child.pid, "SIGKILL");
	} catch {
		child.kill("SIGKILL");
	}
}

try {
	await build({
		configFile: false,
		logLevel: "error",
		build: {
			emptyOutDir: true,
			outDir: outputDirectory,
			ssr: entry,
			target: "node20",
			rollupOptions: {
				external: ["electron"],
				output: {
					entryFileNames: "browser-postcondition-integration.cjs",
					format: "cjs",
				},
			},
		},
	});

	const bundle = path.join(outputDirectory, "browser-postcondition-integration.cjs");
	const userData = path.join(outputDirectory, "user-data");
	await mkdir(userData);
	const command = process.platform === "linux" ? "xvfb-run" : electron;
	const args = [
		...(process.platform === "linux" ? ["-a", electron] : []),
		...(process.platform === "linux" ? ["--no-sandbox", "--disable-gpu"] : []),
		bundle,
	];
	await new Promise((resolve, reject) => {
		const child = spawn(command, args, {
			cwd: frontendRoot,
			detached: process.platform !== "win32",
			env: {
				...process.env,
				AO_BROWSER_POSTCONDITION_FIXTURE: fixture,
				AO_BROWSER_POSTCONDITION_USER_DATA: userData,
			},
			stdio: ["ignore", "pipe", "pipe"],
			windowsHide: true,
		});
		let settled = false;
		let timedOut = false;
		let resultReceived = false;
		let stdoutBuffer = "";
		const finish = (error) => {
			if (settled) return;
			settled = true;
			clearTimeout(timeout);
			if (error) reject(error);
			else resolve();
		};
		const timeout = setTimeout(async () => {
			timedOut = true;
			await terminateProcessTree(child);
			finish(new Error("browser action postcondition integration timed out"));
		}, 120_000);
		child.once("error", (error) => {
			finish(error);
		});
		child.stdout.setEncoding("utf8");
		child.stderr.pipe(process.stderr);
		child.stdout.on("data", (chunk) => {
			stdoutBuffer += chunk;
			const lines = stdoutBuffer.split(/\r?\n/);
			stdoutBuffer = lines.pop() ?? "";
			for (const line of lines) {
				const match = /^AO_BROWSER_POSTCONDITION_RESULT:(\d+)$/.exec(line);
				if (!match) {
					process.stdout.write(`${line}\n`);
					continue;
				}
				if (resultReceived) continue;
				resultReceived = true;
				const resultCode = Number(match[1]);
				void terminateProcessTree(child).then(() => {
					if (resultCode === 0) finish();
					else finish(new Error(`browser action postcondition integration reported ${resultCode}`));
				});
			}
		});
		child.once("exit", (code, signal) => {
			if (resultReceived) return;
			if (timedOut) finish(new Error("browser action postcondition integration timed out"));
			else if (code === 0) finish();
			else finish(new Error(`browser action postcondition integration exited with ${code ?? signal}`));
		});
	});
} finally {
	await rm(outputDirectory, {
		recursive: true,
		force: true,
		maxRetries: 10,
		retryDelay: 100,
	});
}
