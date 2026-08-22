#!/usr/bin/env node

import { constants as fsConstants } from "node:fs";
import { access, mkdir, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { delimiter, dirname, isAbsolute, join, resolve } from "node:path";
import { performance } from "node:perf_hooks";
import { spawn } from "node:child_process";

const SPAWN_ID = /^spawned session ([A-Za-z0-9_-]+) \(/m;
const DEFAULT_PROMPT = "Create RESTORE_TEST.md, then wait for further instructions.";

function printHelp() {
	console.log(`Usage: run_restore_test.mjs [options]

Spawn, kill, restore, and verify AO sessions; print a batch report.

Options:
  --count NUMBER             agents to test (default: 3, range: 1-50)
  --project ID              project to test (default: agent-orchestrator)
  --ao PATH                 path or command name for the AO CLI
  --run-file PATH           AO_RUN_FILE override
  --data-dir PATH           AO_DATA_DIR override
  --name-prefix TEXT        session name prefix (default: restore-test)
  --prompt TEXT             agent prompt
  --settle-seconds NUMBER   delay after each spawn before kill (default: 5)
  --timeout-seconds NUMBER  timeout for each AO command (default: 120)
  --daemon-start-timeout-seconds NUMBER
                           wait for AO readiness after start (default: 60)
  --report PATH             write the full JSON report
  --format markdown|json    stdout report format (default: markdown)
  -h, --help                show this help`);
}

function parseArgs(argv) {
	const options = {
		count: 3,
		project: "agent-orchestrator",
		ao: "",
		runFile: "",
		dataDir: "",
		namePrefix: "restore-test",
		prompt: DEFAULT_PROMPT,
		settleSeconds: 5,
		timeoutSeconds: 120,
		daemonStartTimeoutSeconds: 60,
		report: "",
		format: "markdown",
	};
	const names = {
		"--count": ["count", Number],
		"--project": ["project", String],
		"--ao": ["ao", String],
		"--run-file": ["runFile", String],
		"--data-dir": ["dataDir", String],
		"--name-prefix": ["namePrefix", String],
		"--prompt": ["prompt", String],
		"--settle-seconds": ["settleSeconds", Number],
		"--timeout-seconds": ["timeoutSeconds", Number],
		"--daemon-start-timeout-seconds": ["daemonStartTimeoutSeconds", Number],
		"--report": ["report", String],
		"--format": ["format", String],
	};

	for (let index = 0; index < argv.length; index += 1) {
		const argument = argv[index];
		if (argument === "-h" || argument === "--help") {
			options.help = true;
			continue;
		}
		const definition = names[argument];
		if (!definition) {
			throw new Error(`unknown option: ${argument}`);
		}
		const value = argv[index + 1];
		if (value === undefined) {
			throw new Error(`missing value for ${argument}`);
		}
		const [key, convert] = definition;
		options[key] = convert(value);
		index += 1;
	}
	return options;
}

function expandHome(path) {
	if (path === "~") {
		return homedir();
	}
	if (path.startsWith("~/")) {
		return join(homedir(), path.slice(2));
	}
	return path;
}

async function isExecutable(path) {
	try {
		await access(path, fsConstants.X_OK);
		return true;
	} catch {
		return false;
	}
}

async function findExecutable(candidate) {
	const expanded = expandHome(candidate);
	if (expanded.includes("/") || isAbsolute(expanded)) {
		const absolute = resolve(expanded);
		return (await isExecutable(absolute)) ? absolute : "";
	}
	for (const directory of (process.env.PATH ?? "").split(delimiter)) {
		if (!directory) {
			continue;
		}
		const path = join(directory, expanded);
		if (await isExecutable(path)) {
			return path;
		}
	}
	return "";
}

async function resolveAo(explicit) {
	let candidate = explicit || process.env.AO_BIN || "";
	if (!candidate) {
		candidate = (await isExecutable("/tmp/ao")) ? "/tmp/ao" : "ao";
	}
	const found = await findExecutable(candidate);
	if (!found) {
		throw new Error(`AO CLI not found: ${JSON.stringify(candidate)}; pass --ao or set AO_BIN`);
	}
	return found;
}

function buildName(prefix, index, count) {
	const width = Math.max(2, String(count).length);
	const suffix = `-${String(index).padStart(width, "0")}`;
	const cleanPrefix = prefix.trim().replace(/\s+/g, " ") || "restore-test";
	return `${cleanPrefix.slice(0, 20 - suffix.length)}${suffix}`;
}

function sleep(seconds) {
	return new Promise((done) => {
		setTimeout(done, seconds * 1000);
	});
}

function runCommand(argv, env, timeoutSeconds) {
	return new Promise((done) => {
		const started = performance.now();
		let stdout = "";
		let stderr = "";
		let timedOut = false;
		let finished = false;
		let child;

		const finish = (exitCode, extraError = "") => {
			if (finished) {
				return;
			}
			finished = true;
			clearTimeout(timer);
			if (extraError) {
				stderr = [stderr, extraError].filter(Boolean).join("\n");
			}
			done({
				argv,
				exit_code: exitCode,
				stdout: stdout.trim(),
				stderr: stderr.trim(),
				duration_seconds: Number(((performance.now() - started) / 1000).toFixed(3)),
				timed_out: timedOut,
			});
		};

		try {
			child = spawn(argv[0], argv.slice(1), {
				env,
				stdio: ["ignore", "pipe", "pipe"],
			});
		} catch (error) {
			done({
				argv,
				exit_code: null,
				stdout: "",
				stderr: error instanceof Error ? error.message : String(error),
				duration_seconds: Number(((performance.now() - started) / 1000).toFixed(3)),
				timed_out: false,
			});
			return;
		}

		const timer = setTimeout(() => {
			timedOut = true;
			child.kill("SIGTERM");
		}, timeoutSeconds * 1000);

		child.stdout.on("data", (chunk) => {
			stdout += chunk.toString();
		});
		child.stderr.on("data", (chunk) => {
			stderr += chunk.toString();
		});
		child.once("error", (error) => finish(null, error.message));
		child.once("close", (code) => finish(code));
	});
}

function commandError(result) {
	if (result.timed_out) {
		return `command timed out after ${result.duration_seconds.toFixed(3)}s`;
	}
	const detail = result.stderr || result.stdout || "no error output";
	const code = result.exit_code === null ? "not started" : `exit ${result.exit_code}`;
	return `${code}: ${detail}`;
}

function parseSession(result) {
	if (result.exit_code !== 0) {
		return { session: null, error: commandError(result) };
	}
	let payload;
	try {
		payload = JSON.parse(result.stdout);
	} catch (error) {
		return {
			session: null,
			error: `invalid session JSON: ${error.message}: ${result.stdout || "<empty>"}`,
		};
	}
	if (!payload?.session || typeof payload.session !== "object" || Array.isArray(payload.session)) {
		return {
			session: null,
			error: `session JSON has no object at 'session': ${result.stdout}`,
		};
	}
	return { session: payload.session, error: "" };
}

function parseDaemonStatus(result) {
	if (result.exit_code !== 0) {
		return { ready: false, state: "", error: commandError(result) };
	}
	let payload;
	try {
		payload = JSON.parse(result.stdout);
	} catch (error) {
		return {
			ready: false,
			state: "",
			error: `invalid daemon status JSON: ${error.message}: ${result.stdout || "<empty>"}`,
		};
	}
	const state = typeof payload?.state === "string" ? payload.state : "unknown";
	if (state === "ready") {
		return { ready: true, state, error: "" };
	}
	return {
		ready: false,
		state,
		error: payload?.error ? `state ${state}: ${payload.error}` : `state ${state}`,
	};
}

async function ensureDaemon({ ao, env, timeoutSeconds, startTimeoutSeconds }) {
	const initial = await runCommand([ao, "status", "--json"], env, timeoutSeconds);
	const initialStatus = parseDaemonStatus(initial);
	if (initialStatus.ready) {
		return { ready: true, started: false, initial, final: initial };
	}

	console.error(`AO daemon is unavailable (${initialStatus.error}); running ao start.`);
	const start = await runCommand([ao, "start"], env, timeoutSeconds);
	if (start.exit_code !== 0) {
		return {
			ready: false,
			started: true,
			initial,
			start,
			final: null,
			error: `ao start failed: ${commandError(start)}`,
		};
	}

	const deadline = performance.now() + startTimeoutSeconds * 1000;
	let final = initial;
	let finalStatus = initialStatus;
	while (performance.now() < deadline) {
		await sleep(1);
		final = await runCommand([ao, "status", "--json"], env, timeoutSeconds);
		finalStatus = parseDaemonStatus(final);
		if (finalStatus.ready) {
			return { ready: true, started: true, initial, start, final };
		}
	}
	return {
		ready: false,
		started: true,
		initial,
		start,
		final,
		error: `AO daemon did not become ready within ${startTimeoutSeconds}s (${finalStatus.error})`,
	};
}

function fail(agent, stage, reason) {
	agent.failed_stage = stage;
	agent.reason = reason;
	return agent;
}

async function testAgent({ ao, project, name, prompt, index, env, settleSeconds, timeoutSeconds }) {
	const agent = {
		index,
		name,
		session_id: "",
		restored: false,
		failed_stage: "",
		reason: "",
		commands: {},
		killed_state: null,
		restored_state: null,
	};

	const spawnResult = await runCommand(
		[ao, "spawn", "--project", project, "--name", name, "--prompt", prompt],
		env,
		timeoutSeconds,
	);
	agent.commands.spawn = spawnResult;
	if (spawnResult.exit_code !== 0) {
		return fail(agent, "spawn", commandError(spawnResult));
	}

	const match = SPAWN_ID.exec(spawnResult.stdout);
	if (!match) {
		return fail(agent, "spawn-parse", `could not find session ID in output: ${spawnResult.stdout || "<empty>"}`);
	}
	agent.session_id = match[1];
	if (settleSeconds) {
		await sleep(settleSeconds);
	}

	const base = [ao, "session"];
	const scope = ["--project", project];
	const killResult = await runCommand([...base, "kill", agent.session_id, ...scope], env, timeoutSeconds);
	agent.commands.kill = killResult;
	if (killResult.exit_code !== 0) {
		return fail(agent, "kill", commandError(killResult));
	}

	const verifyKilled = await runCommand([...base, "get", agent.session_id, ...scope, "--json"], env, timeoutSeconds);
	agent.commands.verify_killed = verifyKilled;
	const killed = parseSession(verifyKilled);
	agent.killed_state = killed.session;
	if (killed.error) {
		return fail(agent, "verify-killed", killed.error);
	}
	if (killed.session.id !== agent.session_id) {
		return fail(agent, "verify-killed", `session ID mismatch: got ${JSON.stringify(killed.session.id)}`);
	}
	if (killed.session.projectId !== project) {
		return fail(agent, "verify-killed", `project mismatch: got ${JSON.stringify(killed.session.projectId)}`);
	}
	if (killed.session.isTerminated !== true) {
		return fail(
			agent,
			"verify-killed",
			`expected isTerminated=true, got ${JSON.stringify(killed.session.isTerminated)}`,
		);
	}

	const restoreResult = await runCommand([...base, "restore", agent.session_id, ...scope], env, timeoutSeconds);
	agent.commands.restore = restoreResult;
	if (restoreResult.exit_code !== 0) {
		return fail(agent, "restore", commandError(restoreResult));
	}

	const verifyRestored = await runCommand([...base, "get", agent.session_id, ...scope, "--json"], env, timeoutSeconds);
	agent.commands.verify_restored = verifyRestored;
	const restored = parseSession(verifyRestored);
	agent.restored_state = restored.session;
	if (restored.error) {
		return fail(agent, "verify-restored", restored.error);
	}
	if (restored.session.id !== agent.session_id) {
		return fail(agent, "verify-restored", `session ID mismatch: got ${JSON.stringify(restored.session.id)}`);
	}
	if (restored.session.projectId !== project) {
		return fail(agent, "verify-restored", `project mismatch: got ${JSON.stringify(restored.session.projectId)}`);
	}
	if (restored.session.isTerminated !== false) {
		return fail(
			agent,
			"verify-restored",
			`expected isTerminated=false, got ${JSON.stringify(restored.session.isTerminated)}`,
		);
	}
	agent.restored = true;
	return agent;
}

function safeCell(value) {
	return String(value).replaceAll("|", "\\|").replaceAll("\r", " ").replaceAll("\n", " ");
}

function markdownReport(report) {
	const { summary } = report;
	const lines = [
		"# Agent Restore Test Report",
		"",
		`- Project: \`${safeCell(report.project)}\``,
		`- Requested: ${summary.requested}`,
		`- Restored: ${summary.restored}`,
		`- Not restored: ${summary.not_restored}`,
		`- Started: ${report.started_at}`,
		`- Duration: ${report.duration_seconds.toFixed(3)}s`,
		"",
		"| # | Name | Session ID | Result | Failed stage | Reason |",
		"|---:|---|---|---|---|---|",
	];
	for (const agent of report.agents) {
		lines.push(
			`| ${agent.index} | ${safeCell(agent.name)} | ${safeCell(agent.session_id || "—")} | ${
				agent.restored ? "restored" : "not restored"
			} | ${safeCell(agent.failed_stage || "—")} | ${safeCell(agent.reason || "—")} |`,
		);
	}
	lines.push("", "Successfully restored sessions remain running for inspection.");
	if (report.report_path) {
		lines.push(`Full JSON report: \`${safeCell(report.report_path)}\``);
	}
	return lines.join("\n");
}

async function main() {
	let options;
	try {
		options = parseArgs(process.argv.slice(2));
	} catch (error) {
		console.error(`error: ${error.message}`);
		return 2;
	}
	if (options.help) {
		printHelp();
		return 0;
	}
	if (!Number.isInteger(options.count) || options.count < 1 || options.count > 50) {
		console.error("error: --count must be an integer between 1 and 50");
		return 2;
	}
	if (!Number.isFinite(options.settleSeconds) || options.settleSeconds < 0) {
		console.error("error: --settle-seconds must be non-negative");
		return 2;
	}
	if (!Number.isFinite(options.timeoutSeconds) || options.timeoutSeconds <= 0) {
		console.error("error: --timeout-seconds must be positive");
		return 2;
	}
	if (!Number.isFinite(options.daemonStartTimeoutSeconds) || options.daemonStartTimeoutSeconds <= 0) {
		console.error("error: --daemon-start-timeout-seconds must be positive");
		return 2;
	}
	if (!["markdown", "json"].includes(options.format)) {
		console.error("error: --format must be markdown or json");
		return 2;
	}
	const project = options.project.trim();
	if (!project) {
		console.error("error: --project must not be empty");
		return 2;
	}

	let ao;
	try {
		ao = await resolveAo(options.ao);
	} catch (error) {
		console.error(`error: ${error.message}`);
		return 2;
	}

	const runFile = resolve(expandHome(options.runFile || process.env.AO_RUN_FILE || "~/.ao/dev/running.json"));
	const dataDir = resolve(expandHome(options.dataDir || process.env.AO_DATA_DIR || "~/.ao/dev/data"));
	const env = {
		...process.env,
		AO_RUN_FILE: runFile,
		AO_DATA_DIR: dataDir,
	};

	const daemon = await ensureDaemon({
		ao,
		env,
		timeoutSeconds: options.timeoutSeconds,
		startTimeoutSeconds: options.daemonStartTimeoutSeconds,
	});
	if (!daemon.ready) {
		console.error(`error: ${daemon.error}`);
		return 2;
	}
	console.error(daemon.started ? "AO daemon is ready after start." : "AO daemon is ready.");

	const startedAt = new Date();
	const started = performance.now();
	const agents = [];
	for (let index = 1; index <= options.count; index += 1) {
		const name = buildName(options.namePrefix, index, options.count);
		console.error(`[${index}/${options.count}] testing ${name}`);
		agents.push(
			await testAgent({
				ao,
				project,
				name,
				prompt: options.prompt,
				index,
				env,
				settleSeconds: options.settleSeconds,
				timeoutSeconds: options.timeoutSeconds,
			}),
		);
	}

	const restored = agents.filter((agent) => agent.restored).length;
	const reportPath = options.report ? resolve(expandHome(options.report)) : "";
	const report = {
		schema_version: 1,
		project,
		ao,
		run_file: runFile,
		data_dir: dataDir,
		daemon,
		prompt: options.prompt,
		started_at: startedAt.toISOString(),
		finished_at: new Date().toISOString(),
		duration_seconds: Number(((performance.now() - started) / 1000).toFixed(3)),
		summary: {
			requested: options.count,
			restored,
			not_restored: options.count - restored,
		},
		agents,
		report_path: reportPath,
	};

	if (reportPath) {
		try {
			await mkdir(dirname(reportPath), { recursive: true });
			await writeFile(reportPath, `${JSON.stringify(report, null, 2)}\n`, "utf8");
		} catch (error) {
			console.error(`error: could not write report ${reportPath}: ${error.message}`);
			return 2;
		}
	}

	console.log(options.format === "json" ? JSON.stringify(report, null, 2) : markdownReport(report));
	return restored === options.count ? 0 : 1;
}

process.exitCode = await main();
