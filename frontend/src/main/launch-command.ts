import { spawn, type ChildProcess, type SpawnOptions } from "node:child_process";
import path from "node:path";

type SpawnProcess = (command: string, args: readonly string[], options: SpawnOptions) => ChildProcess;

function windowsBatchArg(value: string): string {
	return `"${value.replaceAll('"', '""')}"`;
}

function spawnSpec(
	command: string,
	args: readonly string[],
	platform: NodeJS.Platform,
	env: NodeJS.ProcessEnv,
): { command: string; args: readonly string[]; windowsVerbatimArguments?: boolean } {
	const extension = path.extname(command).toLowerCase();
	if (platform !== "win32" || (extension !== ".cmd" && extension !== ".bat")) return { command, args };
	const interpreter = env.ComSpec || env.COMSPEC || "cmd.exe";
	const commandLine = [command, ...args].map(windowsBatchArg).join(" ");
	return {
		command: interpreter,
		args: ["/d", "/s", "/c", `"${commandLine}"`],
		windowsVerbatimArguments: true,
	};
}

// A successful spawn alone is not a successful handoff: a CLI shim can start
// and immediately exit non-zero. Wait briefly for that deterministic failure;
// a still-running GUI process is detached after the settling window.
export function launchCommand(
	command: string,
	args: readonly string[],
	cwd: string,
	options: {
		spawnProcess?: SpawnProcess;
		settleMs?: number;
		platform?: NodeJS.Platform;
		env?: NodeJS.ProcessEnv;
	} = {},
): Promise<void> {
	const spawnProcess = options.spawnProcess ?? spawn;
	const settleMs = options.settleMs ?? 500;
	const spec = spawnSpec(command, args, options.platform ?? process.platform, options.env ?? process.env);

	return new Promise((resolve, reject) => {
		let child: ChildProcess;
		try {
			child = spawnProcess(spec.command, spec.args, {
				cwd,
				detached: true,
				stdio: "ignore",
				windowsHide: false,
				windowsVerbatimArguments: spec.windowsVerbatimArguments,
			});
		} catch (error) {
			reject(error);
			return;
		}

		let timer: ReturnType<typeof setTimeout> | undefined;
		let settled = false;
		const finish = (error?: Error) => {
			if (settled) return;
			settled = true;
			if (timer) clearTimeout(timer);
			child.removeListener("error", onError);
			child.removeListener("exit", onExit);
			if (error) reject(error);
			else resolve();
		};
		const onError = (error: Error) => finish(error);
		const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
			if (code === 0) finish();
			else finish(new Error(`launcher exited with ${signal ? `signal ${signal}` : `code ${code ?? "unknown"}`}`));
		};

		child.once("error", onError);
		child.once("exit", onExit);
		child.once("spawn", () => {
			timer = setTimeout(() => {
				child.unref();
				finish();
			}, settleMs);
		});
	});
}
