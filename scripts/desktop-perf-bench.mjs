#!/usr/bin/env node
/**
 * Desktop performance benchmark for Agent Orchestrator.
 *
 * Measures on the current machine:
 *   1. Daemon cold start → /readyz + first /api/v1/projects
 *   2. Desktop cold start → interactive (Electron window + daemon ready), when --electron
 *   3. Daemon (+ renderer when Electron is up) RSS with 1 / 5 / 10 active sessions
 *   4. Terminal input → mux echo latency (PTY round-trip; local echo is separate)
 *
 * Usage (from repo root):
 *   node scripts/desktop-perf-bench.mjs
 *   node scripts/desktop-perf-bench.mjs --electron
 *   node scripts/desktop-perf-bench.mjs --label baseline --out docs/performance/desktop
 *
 * Always uses an isolated AO_DATA_DIR under the OS temp directory. Never touches
 * the developer's real ~/.ao data.
 */

import { spawn, execFileSync } from "node:child_process";
import {
	mkdirSync,
	mkdtempSync,
	writeFileSync,
	rmSync,
	existsSync,
} from "node:fs";
import { tmpdir, cpus, totalmem, freemem, platform, arch, homedir } from "node:os";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { performance } from "node:perf_hooks";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(__dirname, "..");

const args = parseArgs(process.argv.slice(2));
const LABEL = args.label ?? "sample";
const OUT_DIR = resolve(ROOT, args.out ?? "docs/performance/desktop");
const WANT_ELECTRON = Boolean(args.electron);
const SESSION_COUNTS = [1, 5, 10];
const LATENCY_SAMPLES = 30;
const SETTLE_MS = 2_500;

main().catch((err) => {
	console.error(err);
	process.exitCode = 1;
});

async function main() {
	mkdirSync(OUT_DIR, { recursive: true });
	const machine = collectMachineSpecs();
	const commit = gitHead();
	const stamp = new Date().toISOString();
	console.log(`desktop-perf-bench label=${LABEL} commit=${commit.slice(0, 12)}`);
	console.log(`machine: ${machine.cpuModel}, ${machine.cpus} CPUs, ${fmtMiB(machine.totalMemoryBytes)} RAM, ${machine.os}`);

	const aoBin = buildAo();
	const work = mkdtempSync(join(tmpdir(), "ao-perf-"));
	const dataDir = join(work, "data");
	const runFile = join(work, "running.json");
	const projectRepo = join(work, "project");
	mkdirSync(dataDir, { recursive: true });
	initGitRepo(projectRepo);

	const port = await freePort();
	const env = {
		...stripAoEnv(process.env),
		AO_DATA_DIR: dataDir,
		AO_RUN_FILE: runFile,
		AO_PORT: String(port),
		GH_CONFIG_DIR: join(dataDir, "gh-config"),
	};

	const results = {
		label: LABEL,
		commit,
		timestamp: stamp,
		machine,
		method: {
			sessions:
				"Default load is N attached shell terminals (same mux/PTY path as session terminals). Set AO_PERF_USE_SESSIONS=1 to spawn real claude-code sessions instead.",
			memory: "RSS via ps -o rss= on macOS/Linux for the daemon PID and Electron Helper (Renderer) PIDs when --electron.",
			latency:
				"Terminal mux WebSocket: write a unique marker to the PTY and measure time until that marker appears in a data frame (input-to-echo). Predictive local echo is not included.",
			coldStart:
				"Daemon: process spawn → /readyz 200 → GET /api/v1/projects 200. Electron (optional): forge start → daemon ready + Launched Electron app.",
		},
		daemonColdStart: null,
		electronColdStart: null,
		memoryBySessions: [],
		terminalLatency: null,
		notes: [],
	};

	let daemon = null;
	let electron = null;
	try {
		daemon = await startDaemon(aoBin, env, port);
		results.daemonColdStart = daemon.coldStart;

		const projectId = await addProject(port, projectRepo);
		const sessionMode = await detectSessionMode(port);
		results.notes.push(`sessionMode=${sessionMode}`);

		for (const n of SESSION_COUNTS) {
			console.log(`\n=== memory @ ${n} active session(s) (${sessionMode}) ===`);
			const handles = await provisionLoad(port, projectId, n, sessionMode);
			await sleep(SETTLE_MS);
			const sample = sampleMemory({
				daemonPid: daemon.pid,
				electronPid: electron?.pid,
				sessionCount: n,
				attached: handles.length,
			});
			results.memoryBySessions.push(sample);
			console.log(
				`  daemon RSS ${fmtMiB(sample.daemonRssBytes)}` +
					(sample.rendererRssBytes != null
						? ` | renderer RSS ${fmtMiB(sample.rendererRssBytes)}`
						: " | renderer n/a"),
			);
			await teardownLoad(port, handles, sessionMode);
			await sleep(500);
		}

		console.log("\n=== terminal input→echo latency ===");
		results.terminalLatency = await measureTerminalLatency(port, projectId);
		console.log(
			`  p50=${results.terminalLatency.p50Ms.toFixed(1)}ms ` +
				`p95=${results.terminalLatency.p95Ms.toFixed(1)}ms ` +
				`n=${results.terminalLatency.samples.length}`,
		);

		if (WANT_ELECTRON) {
			console.log("\n=== electron cold start ===");
			electron = await startElectron(env, port);
			results.electronColdStart = electron.coldStart;
			console.log(`  interactive in ${electron.coldStart.interactiveMs.toFixed(0)}ms`);

			// Re-sample memory with Electron up and N shell terminals open via API
			// (renderer list refresh) for the largest count.
			const n = 10;
			const handles = await provisionLoad(port, projectId, n, "shell-terminals");
			await sleep(SETTLE_MS * 2);
			const sample = sampleMemory({
				daemonPid: daemon.pid,
				electronPid: electron.pid,
				sessionCount: n,
				attached: handles.length,
				withElectron: true,
			});
			results.memoryBySessions.push({ ...sample, note: "with Electron UI running; shells via API" });
			await teardownLoad(port, handles, "shell-terminals");
		}
	} finally {
		await stopProcess(electron);
		await stopProcess(daemon);
		try {
			rmSync(work, { recursive: true, force: true });
		} catch {
			/* best effort */
		}
	}

	const outPath = join(OUT_DIR, `${LABEL}-${stamp.replace(/[:.]/g, "-")}.json`);
	writeFileSync(outPath, JSON.stringify(results, null, 2) + "\n");
	writeFileSync(join(OUT_DIR, `${LABEL}-latest.json`), JSON.stringify(results, null, 2) + "\n");
	console.log(`\nwrote ${outPath}`);
	return results;
}

function parseArgs(argv) {
	const out = {};
	for (let i = 0; i < argv.length; i += 1) {
		const a = argv[i];
		if (a === "--electron") out.electron = true;
		else if (a === "--label") out.label = argv[++i];
		else if (a === "--out") out.out = argv[++i];
		else if (a === "--help" || a === "-h") {
			console.log("Usage: node scripts/desktop-perf-bench.mjs [--electron] [--label name] [--out dir]");
			process.exit(0);
		} else throw new Error(`unknown arg: ${a}`);
	}
	return out;
}

function collectMachineSpecs() {
	const cpuModel = cpus()[0]?.model?.trim() ?? "unknown";
	let memsize = totalmem();
	let swVers = null;
	try {
		if (platform() === "darwin") {
			swVers = execFileSync("sw_vers", ["-productVersion"], { encoding: "utf8" }).trim();
		}
	} catch {
		/* ignore */
	}
	return {
		cpuModel,
		cpus: cpus().length,
		arch: arch(),
		platform: platform(),
		os: swVers ? `macOS ${swVers} (${platform()} ${arch()})` : `${platform()} ${arch()}`,
		totalMemoryBytes: memsize,
		freeMemoryBytes: freemem(),
		node: process.version,
		hostnameHint: homedir().includes("prateek") ? "developer-laptop" : "host",
	};
}

function gitHead() {
	try {
		return execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();
	} catch {
		return "unknown";
	}
}

function buildAo() {
	const out = join(tmpdir(), `ao-perf-bin-${process.pid}`);
	mkdirSync(out, { recursive: true });
	const bin = join(out, "ao");
	console.log("building ao…");
	execFileSync("go", ["build", "-o", bin, "./cmd/ao"], {
		cwd: join(ROOT, "backend"),
		stdio: "inherit",
	});
	return bin;
}

function stripAoEnv(env) {
	const next = { ...env };
	for (const key of Object.keys(next)) {
		if (key.startsWith("AO_")) delete next[key];
	}
	return next;
}

function initGitRepo(dir) {
	mkdirSync(dir, { recursive: true });
	writeFileSync(join(dir, "README.md"), "# ao-perf fixture\n");
	execFileSync("git", ["init", "-b", "main"], { cwd: dir, stdio: "ignore" });
	execFileSync("git", ["config", "user.email", "perf@ao.local"], { cwd: dir, stdio: "ignore" });
	execFileSync("git", ["config", "user.name", "AO Perf"], { cwd: dir, stdio: "ignore" });
	execFileSync("git", ["add", "."], { cwd: dir, stdio: "ignore" });
	execFileSync("git", ["commit", "-m", "init"], { cwd: dir, stdio: "ignore" });
}

async function freePort() {
	const { createServer } = await import("node:net");
	return await new Promise((resolvePort, reject) => {
		const s = createServer();
		s.listen(0, "127.0.0.1", () => {
			const addr = s.address();
			const port = typeof addr === "object" && addr ? addr.port : 0;
			s.close((err) => (err ? reject(err) : resolvePort(port)));
		});
		s.on("error", reject);
	});
}

async function startDaemon(aoBin, env, port) {
	const t0 = performance.now();
	const child = spawn(aoBin, ["daemon"], {
		env,
		stdio: ["ignore", "pipe", "pipe"],
		detached: true,
	});
	let log = "";
	child.stdout.on("data", (b) => {
		log += b.toString();
	});
	child.stderr.on("data", (b) => {
		log += b.toString();
	});
	const readyMs = await waitFor(async () => {
		const r = await fetch(`http://127.0.0.1:${port}/readyz`);
		return r.ok;
	}, 60_000, "daemon /readyz");
	const projectsMs = await waitFor(async () => {
		const r = await fetch(`http://127.0.0.1:${port}/api/v1/projects`);
		return r.ok;
	}, 30_000, "GET /api/v1/projects");
	const coldStart = {
		readyzMs: readyMs - t0,
		interactiveMs: projectsMs - t0,
		pid: child.pid,
	};
	console.log(
		`daemon cold start: readyz ${coldStart.readyzMs.toFixed(0)}ms, interactive ${coldStart.interactiveMs.toFixed(0)}ms (pid ${child.pid})`,
	);
	return { pid: child.pid, child, coldStart, log: () => log, stop: async () => stopProcess({ child, pid: child.pid }) };
}

async function startElectron(env, port) {
	const frontend = join(ROOT, "frontend");
	if (!existsSync(join(frontend, "node_modules", "electron"))) {
		throw new Error("frontend/node_modules/electron missing; run npm ci in frontend/ first for --electron");
	}
	const t0 = performance.now();
	const child = spawn("npm", ["run", "dev"], {
		cwd: frontend,
		env: {
			...env,
			AO_DEV_ELECTRON_DIR: join(env.AO_DATA_DIR, "electron"),
			// Keep forge on the same isolated daemon we already started by pointing
			// at the existing run file / port rather than spawning a second daemon.
			ELECTRON_ENABLE_LOGGING: "1",
		},
		stdio: ["ignore", "pipe", "pipe"],
		detached: true,
	});
	let log = "";
	const onData = (b) => {
		log += b.toString();
	};
	child.stdout.on("data", onData);
	child.stderr.on("data", onData);

	const launchedMs = await waitFor(() => log.includes("Launched Electron app"), 180_000, "Electron launched");
	const readyMs = await waitFor(async () => {
		const r = await fetch(`http://127.0.0.1:${port}/readyz`);
		return r.ok;
	}, 60_000, "daemon still ready under Electron");
	const interactiveMs = Math.max(launchedMs, readyMs) - t0;
	return {
		pid: child.pid,
		child,
		coldStart: {
			launchedMs: launchedMs - t0,
			interactiveMs,
			pid: child.pid,
		},
	};
}

async function addProject(port, repoPath) {
	const res = await fetch(`http://127.0.0.1:${port}/api/v1/projects`, {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify({ path: repoPath, projectId: "perf-fixture", name: "perf-fixture" }),
	});
	if (!res.ok) {
		const body = await res.text();
		throw new Error(`add project failed: ${res.status} ${body}`);
	}
	const json = await res.json();
	const id = json.project?.id ?? json.id;
	if (!id) throw new Error(`add project: missing id in ${JSON.stringify(json)}`);
	return id;
}

async function detectSessionMode(port) {
	// Default to attached shell terminals: reproducible without agent credentials
	// or model spend, and they exercise the same mux/PTY path that dominates
	// orchestrator memory under multi-session load. Pass AO_PERF_USE_SESSIONS=1
	// to prefer real claude-code sessions when the catalog reports them ready.
	if (process.env.AO_PERF_USE_SESSIONS !== "1") return "shell-terminals";
	try {
		const res = await fetch(`http://127.0.0.1:${port}/api/v1/agents`);
		if (!res.ok) return "shell-terminals";
		const json = await res.json();
		const agents = json.agents ?? json.installed ?? [];
		const list = Array.isArray(agents) ? agents : [];
		if (list.some((a) => String(a.id || a.harness || "").includes("claude"))) {
			return "sessions-claude";
		}
	} catch {
		/* fall through */
	}
	return "shell-terminals";
}

async function provisionLoad(port, projectId, n, mode) {
	if (mode === "shell-terminals") {
		const handles = [];
		for (let i = 0; i < n; i += 1) {
			const res = await fetch(`http://127.0.0.1:${port}/api/v1/shell-terminals`, {
				method: "POST",
				headers: { "content-type": "application/json" },
				body: JSON.stringify({ projectId, title: `perf-${i}` }),
			});
			if (!res.ok) throw new Error(`open shell terminal: ${res.status} ${await res.text()}`);
			const json = await res.json();
			const handleId = json.shellTerminal?.handleId ?? json.handleId;
			const ws = await attachMux(port, handleId);
			handles.push({ kind: "shell", handleId, ws });
		}
		return handles;
	}

	const handles = [];
	for (let i = 0; i < n; i += 1) {
		const res = await fetch(`http://127.0.0.1:${port}/api/v1/sessions`, {
			method: "POST",
			headers: { "content-type": "application/json" },
			body: JSON.stringify({
				projectId,
				harness: "claude-code",
				displayName: `perf${i}`,
				prompt: "Reply with the single word READY and then wait for further instructions. Do not use tools.",
				mode: "tui",
			}),
		});
		if (!res.ok) {
			const body = await res.text();
			throw new Error(`spawn session failed: ${res.status} ${body}`);
		}
		const json = await res.json();
		const session = json.session ?? json;
		const handleId = session.terminalHandleId ?? session.runtimeHandleId ?? session.id;
		let ws = null;
		if (handleId) {
			try {
				ws = await attachMux(port, handleId);
			} catch (err) {
				console.warn(`  mux attach for ${session.id}: ${err.message}`);
			}
		}
		handles.push({ kind: "session", sessionId: session.id, handleId, ws });
	}
	return handles;
}

async function teardownLoad(port, handles, mode) {
	for (const h of handles) {
		try {
			h.ws?.close?.();
		} catch {
			/* ignore */
		}
		if (h.kind === "shell") {
			await fetch(`http://127.0.0.1:${port}/api/v1/shell-terminals/${encodeURIComponent(h.handleId)}`, {
				method: "DELETE",
			}).catch(() => undefined);
		} else if (h.kind === "session" && h.sessionId) {
			await fetch(`http://127.0.0.1:${port}/api/v1/sessions/${encodeURIComponent(h.sessionId)}/kill`, {
				method: "POST",
				headers: { "content-type": "application/json" },
				body: "{}",
			}).catch(() => undefined);
		}
	}
	void mode;
}

async function attachMux(port, id) {
	const ws = new WebSocket(`ws://127.0.0.1:${port}/mux`);
	await new Promise((resolveOpen, reject) => {
		const t = setTimeout(() => reject(new Error("mux open timeout")), 15_000);
		ws.addEventListener("open", () => {
			clearTimeout(t);
			resolveOpen();
		});
		ws.addEventListener("error", (e) => {
			clearTimeout(t);
			reject(e);
		});
	});
	ws.send(JSON.stringify({ ch: "terminal", type: "open", id, cols: 80, rows: 24 }));
	// Drain until opened or a short settle.
	await sleep(200);
	return ws;
}

async function measureTerminalLatency(port, projectId) {
	const res = await fetch(`http://127.0.0.1:${port}/api/v1/shell-terminals`, {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify({ projectId, title: "perf-latency" }),
	});
	if (!res.ok) throw new Error(`latency shell open: ${res.status} ${await res.text()}`);
	const json = await res.json();
	const id = json.shellTerminal?.handleId ?? json.handleId;
	const ws = new WebSocket(`ws://127.0.0.1:${port}/mux`);
	await new Promise((resolveOpen, reject) => {
		ws.addEventListener("open", resolveOpen);
		ws.addEventListener("error", reject);
	});
	ws.send(JSON.stringify({ ch: "terminal", type: "open", id, cols: 80, rows: 24 }));

	let buf = "";
	ws.addEventListener("message", (ev) => {
		if (typeof ev.data !== "string") return;
		let frame;
		try {
			frame = JSON.parse(ev.data);
		} catch {
			return;
		}
		if (frame.ch === "terminal" && frame.type === "data" && frame.data) {
			buf += Buffer.from(frame.data, "base64").toString("utf8");
		}
	});

	await waitFor(() => buf.length > 0 || true, 1_000, "shell settle").catch(() => undefined);
	await sleep(800);

	const samples = [];
	for (let i = 0; i < LATENCY_SAMPLES; i += 1) {
		const marker = `AO_PERF_${i}_${Math.random().toString(16).slice(2, 10)}`;
		buf = "";
		const payload = Buffer.from(`printf '%s\\n' '${marker}'\n`, "utf8").toString("base64");
		const t0 = performance.now();
		ws.send(JSON.stringify({ ch: "terminal", type: "data", id, data: payload }));
		const ok = await waitFor(() => buf.includes(marker), 5_000, `echo ${marker}`).then(() => true).catch(() => false);
		if (ok) samples.push(performance.now() - t0);
		await sleep(50);
	}

	ws.close();
	await fetch(`http://127.0.0.1:${port}/api/v1/shell-terminals/${encodeURIComponent(id)}`, {
		method: "DELETE",
	}).catch(() => undefined);

	samples.sort((a, b) => a - b);
	return {
		samples,
		p50Ms: percentile(samples, 0.5),
		p95Ms: percentile(samples, 0.95),
		minMs: samples[0] ?? null,
		maxMs: samples[samples.length - 1] ?? null,
	};
}

function sampleMemory({ daemonPid, electronPid, sessionCount, attached, withElectron }) {
	const daemonRssBytes = rssBytes(daemonPid);
	let rendererRssBytes = null;
	let electronTreeRssBytes = null;
	if (electronPid) {
		const tree = processTreeRss(electronPid);
		electronTreeRssBytes = tree.totalBytes;
		rendererRssBytes = tree.rendererBytes;
	}
	// Do not scavenge unrelated Electron Helper processes from the host when this
	// bench did not launch Electron — that would mis-attribute other apps' RSS.
	return {
		sessionCount,
		attached,
		withElectron: Boolean(withElectron || electronPid),
		daemonRssBytes,
		rendererRssBytes,
		electronTreeRssBytes,
		sampledAt: new Date().toISOString(),
	};
}

function rssBytes(pid) {
	if (!pid) return null;
	try {
		const out = execFileSync("ps", ["-o", "rss=", "-p", String(pid)], { encoding: "utf8" }).trim();
		const kb = Number(out);
		return Number.isFinite(kb) ? kb * 1024 : null;
	} catch {
		return null;
	}
}

function processTreeRss(rootPid) {
	try {
		const out = execFileSync("ps", ["-axo", "pid=,ppid=,rss=,command="], { encoding: "utf8" });
		const rows = out
			.split("\n")
			.map((line) => line.trim())
			.filter(Boolean)
			.map((line) => {
				const m = line.match(/^(\d+)\s+(\d+)\s+(\d+)\s+(.*)$/);
				if (!m) return null;
				return { pid: Number(m[1]), ppid: Number(m[2]), rssKb: Number(m[3]), cmd: m[4] };
			})
			.filter(Boolean);
		const children = new Map();
		for (const row of rows) {
			if (!children.has(row.ppid)) children.set(row.ppid, []);
			children.get(row.ppid).push(row);
		}
		const keep = new Set();
		const walk = (pid) => {
			keep.add(pid);
			for (const child of children.get(pid) ?? []) walk(child.pid);
		};
		walk(rootPid);
		let totalBytes = 0;
		let rendererBytes = 0;
		for (const row of rows) {
			if (!keep.has(row.pid)) continue;
			totalBytes += row.rssKb * 1024;
			if (/Helper \(Renderer\)|type=renderer/i.test(row.cmd)) {
				rendererBytes += row.rssKb * 1024;
			}
		}
		return { totalBytes, rendererBytes: rendererBytes || null };
	} catch {
		return { totalBytes: null, rendererBytes: null };
	}
}

async function stopProcess(proc) {
	if (!proc?.child && !proc?.pid) return;
	const pid = proc.pid ?? proc.child?.pid;
	try {
		if (pid) process.kill(-pid, "SIGTERM");
	} catch {
		try {
			if (pid) process.kill(pid, "SIGTERM");
		} catch {
			/* ignore */
		}
	}
	await sleep(500);
	try {
		if (pid) process.kill(-pid, "SIGKILL");
	} catch {
		/* ignore */
	}
}

async function waitFor(predicate, timeoutMs, label) {
	const start = performance.now();
	const deadline = start + timeoutMs;
	let lastErr;
	while (performance.now() < deadline) {
		try {
			if (await predicate()) return performance.now();
		} catch (err) {
			lastErr = err;
		}
		await sleep(50);
	}
	throw new Error(`timeout waiting for ${label}${lastErr ? `: ${lastErr}` : ""}`);
}

function sleep(ms) {
	return new Promise((r) => setTimeout(r, ms));
}

function percentile(sorted, p) {
	if (sorted.length === 0) return NaN;
	const idx = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * p) - 1));
	return sorted[idx];
}

function fmtMiB(bytes) {
	if (bytes == null) return "n/a";
	return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
