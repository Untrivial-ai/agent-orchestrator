#!/usr/bin/env node

import { access, mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { delimiter, dirname, isAbsolute, join, resolve } from "node:path";
import { homedir } from "node:os";
import { spawn } from "node:child_process";

const DEFAULT_TASK = "Change the background color of the notification icon to red. Implement the change, run the relevant checks, open a PR, and wait for review.";
const HARNESSES = new Set(["claude-code", "codex", "aider", "opencode", "grok", "droid", "amp", "agy", "crush", "cursor", "qwen", "copilot", "goose", "auggie", "continue", "devin", "cline", "kimi", "muse", "kiro", "kilocode", "vibe", "pi", "kimchi", "prime-agent", "autohand"]);
const CORE_STAGES = ["preflight", "roles", "kanban-activity", "reviewer-testing"];
const STAGES = [...CORE_STAGES, "lifecycle"];
const STAGE_ALIASES = new Map([
  ["orchestrator", "roles"],
  ["delegation", "roles"],
  ["work-pr", "kanban-activity"],
  ["reviewer", "reviewer-testing"],
  ["kill-restore", "lifecycle"],
]);

function help() {
  console.log(`Usage: run_agent_e2e.mjs --project ID --harness NAME [options]
  --orchestrator-harness NAME  role-specific orchestrator harness
  --reviewer-harness NAME      expected reviewer harness
  --task TEXT                  task brief
  --stages LIST                comma stages or all (default all)
  --orchestrator-session ID    inspect existing orchestrator instead of spawning
  --worker-session ID          inspect existing worker for work/reviewer stages
  --reviewer-session ID        expected reviewer tmux/session id
  --lifecycle-session ID       session to kill and restore
  --mode tui|chat              session interface (default tui)
  --ao PATH                    AO binary (AO_BIN, /tmp/ao, or PATH)
  --report PATH                write JSON evidence
  --poll-timeout-seconds N     timeout per stage (default 180)
  --command-timeout-seconds N timeout per command (default 120)
  --poll-interval-seconds N    poll interval (default 3)
  --tmux-lines N               pane lines to capture per session (default 160)
  --allow-unobservable         exit 0 when only observation gaps remain
  --cleanup                    kill only sessions created by this run
  -h, --help`);
}

function parseArgs(argv) {
  const o = { project: "", harness: "", orchestratorHarness: "", reviewerHarness: "", task: DEFAULT_TASK, stages: "all", orchestratorSession: "", workerSession: "", reviewerSession: "", lifecycleSession: "", mode: "tui", ao: "", report: "", pollTimeoutSeconds: 180, commandTimeoutSeconds: 120, pollIntervalSeconds: 3, tmuxLines: 160, allowUnobservable: false, cleanup: false };
  const flags = { "--project": "project", "--harness": "harness", "--orchestrator-harness": "orchestratorHarness", "--reviewer-harness": "reviewerHarness", "--task": "task", "--stages": "stages", "--orchestrator-session": "orchestratorSession", "--worker-session": "workerSession", "--reviewer-session": "reviewerSession", "--lifecycle-session": "lifecycleSession", "--mode": "mode", "--ao": "ao", "--report": "report", "--poll-timeout-seconds": "pollTimeoutSeconds", "--command-timeout-seconds": "commandTimeoutSeconds", "--poll-interval-seconds": "pollIntervalSeconds", "--tmux-lines": "tmuxLines" };
  for (let i = 0; i < argv.length; i += 1) {
    if (argv[i] === "-h" || argv[i] === "--help") return { ...o, help: true };
    if (argv[i] === "--cleanup") { o.cleanup = true; continue; }
    if (argv[i] === "--allow-unobservable") { o.allowUnobservable = true; continue; }
    const key = flags[argv[i]];
    if (!key || argv[i + 1] === undefined) throw new Error(key ? `missing value for ${argv[i]}` : `unknown option: ${argv[i]}`);
    o[key] = ["pollTimeoutSeconds", "commandTimeoutSeconds", "pollIntervalSeconds", "tmuxLines"].includes(key) ? Number(argv[++i]) : argv[++i];
  }
  if (!o.project || !o.harness) throw new Error("--project and --harness are required");
  if (o.mode !== "tui" && o.mode !== "chat") throw new Error("--mode must be tui or chat");
  if (!HARNESSES.has(o.harness)) throw new Error(`unknown harness: ${o.harness}`);
  for (const key of ["orchestratorHarness", "reviewerHarness"]) if (o[key] && !HARNESSES.has(o[key])) throw new Error(`unknown ${key}: ${o[key]}`);
  o.selectedStages = parseStages(o.stages);
  for (const key of ["pollTimeoutSeconds", "commandTimeoutSeconds", "pollIntervalSeconds", "tmuxLines"]) if (!Number.isFinite(o[key]) || o[key] <= 0) throw new Error(`${key} must be positive`);
  return o;
}

function parseStages(value) {
  const requested = String(value).split(",").map((x) => x.trim()).filter(Boolean);
  if (requested.length === 1 && requested[0] === "all") return CORE_STAGES;
  const selected = [];
  for (const item of requested) {
    if (item === "all") selected.push(...CORE_STAGES);
    else selected.push(STAGE_ALIASES.get(item) || item);
  }
  if (selected.length === 0) throw new Error("--stages must not be empty");
  for (const stage of selected) if (!STAGES.includes(stage)) throw new Error(`unknown stage: ${stage}`);
  return [...new Set(selected)];
}
function wants(options, stage) { return options.selectedStages.includes(stage); }
function expandHome(value) { return value === "~" ? homedir() : value.startsWith("~/") ? join(homedir(), value.slice(2)) : value; }
async function findExecutable(candidate) {
  const expanded = expandHome(candidate);
  if (isAbsolute(expanded) || expanded.includes("/")) { try { await access(resolve(expanded), fsConstants.X_OK); return resolve(expanded); } catch { return ""; } }
  for (const dir of (process.env.PATH ?? "").split(delimiter)) { if (!dir) continue; const path = join(dir, expanded); try { await access(path, fsConstants.X_OK); return path; } catch {} }
  return "";
}
async function resolveAo(explicit) { const candidate = explicit || process.env.AO_BIN || (await findExecutable("/tmp/ao") ? "/tmp/ao" : "ao"); const found = await findExecutable(candidate); if (!found) throw new Error(`AO CLI not found: ${candidate}`); return found; }

function runCommand(argv, timeoutSeconds) {
  return new Promise((done) => {
    let out = "", err = "", timedOut = false, finished = false;
    const started = Date.now(); const child = spawn(argv[0], argv.slice(1), { env: process.env, stdio: ["ignore", "pipe", "pipe"] });
    const timer = setTimeout(() => { timedOut = true; child.kill("SIGTERM"); }, timeoutSeconds * 1000);
    const finish = (code, extra = "") => { if (finished) return; finished = true; clearTimeout(timer); if (extra) err += `\n${extra}`; done({ argv, code, stdout: out.trim(), stderr: err.trim(), timedOut, seconds: Number(((Date.now() - started) / 1000).toFixed(3)) }); };
    child.stdout.on("data", (x) => { out += x; }); child.stderr.on("data", (x) => { err += x; }); child.once("error", (x) => finish(null, x.message)); child.once("close", (code) => finish(code));
  });
}
function json(text) { try { return JSON.parse(text); } catch { return null; } }
function strings(value, result = []) { if (typeof value === "string") result.push(value); else if (Array.isArray(value)) value.forEach((x) => strings(x, result)); else if (value && typeof value === "object") Object.values(value).forEach((x) => strings(x, result)); return result; }
function sessionFrom(payload) { return payload?.session ?? null; }
function itemsFrom(payload) { return payload?.data ?? payload?.sessions ?? []; }
function agentInfoByID(infos, id) { return Array.isArray(infos) ? infos.find((info) => info?.id === id) ?? null : null; }
function readinessForHarness(inventory, harness, roles) {
  const supported = agentInfoByID(inventory?.supported, harness);
  const installed = agentInfoByID(inventory?.installed, harness);
  const authorized = agentInfoByID(inventory?.authorized, harness);
  const authStatus = authorized?.authStatus || installed?.authStatus || "unknown";
  const readiness = {
    harness,
    roles,
    supported: Boolean(supported),
    installed: Boolean(installed || authorized),
    authorization: authStatus,
    probe: "ao agent ls --refresh --json",
  };
  if (!readiness.supported) readiness.reason = "harness is not supported by this AO daemon";
  else if (!readiness.installed) readiness.reason = "agent CLI is not installed according to AO's fresh local probe";
  else if (authStatus === "authorized") readiness.reason = "AO's fresh local auth probe confirmed authorization";
  else if (authStatus === "unauthorized") readiness.reason = "AO's fresh local auth probe found missing or invalid credentials; authentication is required";
  else readiness.reason = "authorization could not be verified by AO's fresh local probe";
  readiness.ready = readiness.supported && readiness.installed && readiness.authorization === "authorized";
  return readiness;
}
function requiredRoleHarnesses(options) {
  const required = new Map();
  const add = (harness, role) => {
    const current = required.get(harness) ?? [];
    current.push(role);
    required.set(harness, current);
  };
  add(options.orchestratorHarness || options.harness, "orchestrator");
  add(options.harness, "worker");
  if (wants(options, "reviewer-testing") && options.reviewerHarness) add(options.reviewerHarness, "reviewer");
  return [...required].map(([harness, roles]) => ({ harness, roles }));
}
function hasTaskEvidence(payload, task) { return strings(payload).some((x) => x.includes(task)); }
function classifyTaskEvidence(payload, task) { return hasTaskEvidence(payload, task) ? "observed" : "unobservable"; }
function firstPR(session) { return Array.isArray(session?.prs) && session.prs.length ? session.prs[0] : null; }
function reviewCompleted(latest) { return latest && (latest.status === "delivered" || latest.status === "submitted" || latest.verdict === "approved" || latest.verdict === "changes_requested"); }
function markUnobservable(record, reason) {
  record.status = "unobservable";
  record.reason = reason;
}
function visibleBlockingPrompt(output) {
  if (!output) return false;
  const normalized = output.toLowerCase();
  return (
    normalized.includes("waiting for approval") ||
    normalized.includes("run this command?") ||
    normalized.includes("allow this command") ||
    normalized.includes("approve") ||
    normalized.includes("permission required") ||
    normalized.includes("permission prompt") ||
    normalized.includes("do you want to proceed") ||
    normalized.includes("press enter") ||
    normalized.includes("hit enter") ||
    /\[(y|yes)\/(n|no)\]/i.test(output) ||
    /\((y|yes)\)/i.test(output)
  );
}
function paneSignals(output) {
  if (!output) return ["empty-pane"];
  const normalized = output.toLowerCase();
  const signals = [];
  if (visibleBlockingPrompt(output)) signals.push("blocking-prompt");
  if (normalized.includes("login") || normalized.includes("auth") || normalized.includes("api key") || normalized.includes("oauth")) signals.push("auth-or-login");
  if (normalized.includes("rate limit") || normalized.includes("quota") || normalized.includes("too many requests")) signals.push("rate-limit-or-quota");
  if (normalized.includes("error") || normalized.includes("failed") || normalized.includes("panic") || normalized.includes("exception")) signals.push("error-text");
  if (normalized.includes("permission denied") || normalized.includes("not allowed")) signals.push("permission-denied");
  if (normalized.includes("merge conflict") || normalized.includes("conflict")) signals.push("conflict");
  if (normalized.includes("compiling") || normalized.includes("running") || normalized.includes("installing") || normalized.includes("building")) signals.push("work-in-progress");
  if (/[>$#]\s*$/.test(output.trim()) || normalized.includes("press enter")) signals.push("input-or-shell-prompt");
  return signals.length ? signals : ["no-known-signal"];
}
async function readPromptArtifact(dataDir, sessionID) {
  if (!dataDir) return { status: "unobservable", reason: "ao status did not expose dataDir" };
  const path = join(resolve(expandHome(dataDir)), "prompts", sessionID, "system.md");
  try {
    const body = await readFile(path, "utf8");
    return { status: body.trim() ? "observed" : "failed", path, bytes: Buffer.byteLength(body), body };
  } catch (error) {
    return { status: "unobservable", path, reason: error.message };
  }
}
async function apiJSON(port, method, apiPath, body) {
  if (!port) throw new Error("ao status did not expose daemon port");
  const url = `http://127.0.0.1:${port}/api/v1${apiPath}`;
  const res = await fetch(url, {
    method,
    headers: { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let parsed = {};
  if (text.trim()) {
    try { parsed = JSON.parse(text); } catch { parsed = { raw: text }; }
  }
  if (!res.ok) throw new Error(`${method} ${apiPath} failed with HTTP ${res.status}: ${text}`);
  return parsed;
}

async function save(report, path) {
  if (!path) return;
  const target = resolve(expandHome(path)); await mkdir(dirname(target), { recursive: true }); const tmp = `${target}.tmp-${process.pid}`;
  await writeFile(tmp, `${JSON.stringify(report, null, 2)}\n`); await rename(tmp, target);
}

async function main() {
  let options; try { options = parseArgs(process.argv.slice(2)); } catch (error) { console.error(`configuration error: ${error.message}`); return 2; }
  if (options.help) { help(); return 0; }
  const report = { startedAt: new Date().toISOString(), options, stages: [], sessions: [], roleReadiness: null, cleanup: { requested: options.cleanup, results: [] } };
  let ao; try { ao = await resolveAo(options.ao); } catch (error) { report.failure = { stage: "preflight", reason: error.message }; await save(report, options.report); console.error(error.message); return 2; }
  const created = [];
  const command = (args) => runCommand([ao, ...args], options.commandTimeoutSeconds);
  async function run(record, args, parse = false) { const result = await command(args); record.evidence.push({ command: [ao, ...args], result }); if (result.code !== 0) throw new Error(`${args.join(" ")}: ${result.stderr || result.stdout || `exit ${result.code}`}`); return parse ? json(result.stdout) : result.stdout; }
  async function captureTmux(record, sessionID, label = "tmux") {
    const target = `${sessionID}:0.0`;
    const has = await runCommand(["tmux", "has-session", "-t", sessionID], options.commandTimeoutSeconds);
    const evidence = { label, target, hasSession: has.code === 0, hasSessionResult: has };
    if (has.code === 0) {
      const pane = await runCommand(["tmux", "capture-pane", "-t", target, "-p", "-S", `-${options.tmuxLines}`], options.commandTimeoutSeconds);
      evidence.captureResult = pane;
      evidence.visibleBlockingPrompt = visibleBlockingPrompt(pane.stdout);
    }
    record.evidence.push({ tmux: evidence });
    return evidence;
  }
  async function getSessionDetail(record, sessionID) {
    const status = report.stages[0]?.observed?.status ?? {};
    if (status.port) {
      try {
        const payload = await apiJSON(status.port, "GET", `/sessions/${encodeURIComponent(sessionID)}`);
        record.evidence.push({ api: { method: "GET", path: `/sessions/${sessionID}` }, observed: payload });
        return payload;
      } catch (error) {
        record.evidence.push({ api: { method: "GET", path: `/sessions/${sessionID}` }, error: error.message });
      }
    }
    return run(record, ["session", "get", sessionID, "--json"], true);
  }
  async function poll(record, label, read, predicate) { const deadline = Date.now() + options.pollTimeoutSeconds * 1000; let last; while (Date.now() < deadline) { last = await read(); record.evidence.push({ poll: label, observed: last }); if (predicate(last)) return last; await new Promise((done) => setTimeout(done, options.pollIntervalSeconds * 1000)); } throw new Error(`${label} timed out; last observed: ${JSON.stringify(last)}`); }
  async function stage(name, fn) { const record = { name, status: "running", startedAt: new Date().toISOString(), evidence: [] }; report.stages.push(record); try { await fn(record); if (record.status === "running") record.status = "passed"; } catch (error) { record.status = "failed"; record.reason = error.message; report.failure ??= { stage: name, reason: error.message }; } record.finishedAt = new Date().toISOString(); return record; }

  await stage("preflight", async (r) => { r.observed = { ao, version: await run(r, ["version"]), status: await run(r, ["status", "--json"], true), project: await run(r, ["project", "get", options.project, "--json"], true), harness: options.harness, baselineSessions: await run(r, ["session", "ls", "--project", options.project, "--all", "--json"], true) }; });
  if (report.failure) return finish(report, options, created, command);
  const baseline = new Set(itemsFrom(report.stages[0]?.observed?.baselineSessions).map((x) => x.id));
  if (wants(options, "roles")) await stage("role-readiness", async (r) => {
    r.observed = { harnesses: [], inventoryProbe: "ao agent ls --refresh --json" };
    report.roleReadiness = r.observed;
    const inventory = await run(r, ["agent", "ls", "--refresh", "--json"], true);
    const harnesses = requiredRoleHarnesses(options).map(({ harness, roles }) => readinessForHarness(inventory, harness, roles));
    r.observed.harnesses = harnesses;
    const blocked = harnesses.filter((harness) => !harness.ready);
    if (blocked.length) throw new Error(blocked.map((harness) => `${harness.harness}: ${harness.reason}`).join("; "));
  });
  if (report.failure) return finish(report, options, created, command);
  if (options.orchestratorSession) report.sessions.push({ role: "orchestrator", id: options.orchestratorSession, harness: options.orchestratorHarness || options.harness, mode: options.mode, external: true });
  if (options.workerSession) report.sessions.push({ role: "worker", id: options.workerSession, harness: options.harness, external: true });
  if (options.reviewerSession) report.sessions.push({ role: "reviewer", id: options.reviewerSession, harness: options.reviewerHarness || "configured-by-AO", external: true });

  if (wants(options, "roles")) await stage("orchestrator", async (r) => { let sessionID = options.orchestratorSession; let text = ""; if (!sessionID) { text = await run(r, ["spawn", "--project", options.project, "--kind", "orchestrator", "--mode", options.mode, "--harness", options.orchestratorHarness || options.harness, "--name", "e2e-orchestrator", "--prompt", options.task]); const match = text.match(/spawned session ([A-Za-z0-9_-]+)/); if (!match) throw new Error(`could not parse session ID: ${text}`); sessionID = match[1]; created.push(sessionID); report.sessions.push({ role: "orchestrator", id: sessionID, harness: options.orchestratorHarness || options.harness, mode: options.mode }); } const payload = await poll(r, "orchestrator session", () => getSessionDetail(r, sessionID), (x) => sessionFrom(x)?.isTerminated === false); const promptArtifact = await readPromptArtifact(report.stages[0]?.observed?.status?.dataDir, sessionID); const promptText = promptArtifact.body || ""; delete promptArtifact.body; const tmux = await captureTmux(r, sessionID, "orchestrator"); r.observed = { session: payload, taskEvidence: classifyTaskEvidence(payload, options.task), promptArtifact, rolePromptMarker: promptText.includes("AO Orchestrator Role") ? "observed" : "unobservable", promptBytesReported: options.orchestratorSession ? "external-session" : text.includes("prompt "), systemPromptBytesReported: options.orchestratorSession ? "external-session" : text.includes("system "), mode: options.mode, tmux: { hasSession: tmux.hasSession, visibleBlockingPrompt: tmux.visibleBlockingPrompt ?? false } }; if (promptArtifact.status === "failed") throw new Error(`orchestrator prompt artifact is empty: ${promptArtifact.path}`); if (tmux.visibleBlockingPrompt) markUnobservable(r, "orchestrator tmux pane appears blocked on a prompt"); if (!options.orchestratorSession && (!r.observed.promptBytesReported || !r.observed.systemPromptBytesReported || (r.observed.taskEvidence !== "observed" && promptArtifact.status !== "observed"))) markUnobservable(r, "orchestrator prompt/task evidence is not fully observable through CLI/API/prompt artifacts"); });
  if (report.failure) return finish(report, options, created, command);
  if (wants(options, "roles")) await stage("delegation-and-worker", async (r) => { let worker = null; if (options.workerSession) { const detail = await getSessionDetail(r, options.workerSession); worker = sessionFrom(detail); } else { const payload = await poll(r, "worker delegation", () => run(r, ["session", "ls", "--project", options.project, "--all", "--json"], true), (x) => itemsFrom(x).some((item) => item.kind !== "orchestrator" && !baseline.has(item.id) && !item.isTerminated)); worker = itemsFrom(payload).find((x) => x.kind !== "orchestrator" && !baseline.has(x.id) && !x.isTerminated); created.push(worker.id); report.sessions.push({ role: "worker", id: worker.id, harness: worker.harness || options.harness }); } const detail = await getSessionDetail(r, worker.id); const workerSession = sessionFrom(detail); const promptArtifact = await readPromptArtifact(report.stages[0]?.observed?.status?.dataDir, worker.id); const promptText = promptArtifact.body || ""; delete promptArtifact.body; const tmux = await captureTmux(r, worker.id, "worker"); r.observed = { worker, session: detail, taskEvidence: classifyTaskEvidence(detail, options.task), promptArtifact, rolePromptMarker: promptText.includes("AO Worker Role") ? "observed" : "unobservable", activity: workerSession?.activity, status: workerSession?.status, branch: workerSession?.branch, tmux: { hasSession: tmux.hasSession, visibleBlockingPrompt: tmux.visibleBlockingPrompt ?? false } }; if (promptArtifact.status === "failed") throw new Error(`worker prompt artifact is empty: ${promptArtifact.path}`); if (tmux.visibleBlockingPrompt) markUnobservable(r, "worker tmux pane appears blocked on a prompt"); if (r.observed.taskEvidence !== "observed" && promptArtifact.status !== "observed") markUnobservable(r, "worker task prompt is not visible through session CLI/API JSON or prompt artifact"); });
  if (report.failure) return finish(report, options, created, command);
  if (wants(options, "kanban-activity")) await stage("work-kanban-and-pr", async (r) => { const worker = report.sessions.find((x) => x.role === "worker"); if (!worker?.id) throw new Error("kanban-activity stage requires a worker from roles or --worker-session"); const payload = await poll(r, "worker activity or PR", () => getSessionDetail(r, worker.id), (x) => { const s = sessionFrom(x); return Boolean(firstPR(s) || ["working", "pr_open", "draft", "review_pending", "changes_requested", "approved", "mergeable", "merged"].includes(s?.status)); }); const session = sessionFrom(payload); const pr = firstPR(session); const tmux = await captureTmux(r, worker.id, "worker-work"); r.observed = { session: payload, activity: session?.activity, status: session?.status, branch: session?.branch, pr, scmStatus: session?.scmStatus, tmux: { hasSession: tmux.hasSession, visibleBlockingPrompt: tmux.visibleBlockingPrompt ?? false } }; if (!session?.activity?.state && !session?.status) throw new Error("worker session exposes neither activity nor status"); if (tmux.visibleBlockingPrompt) markUnobservable(r, "worker tmux pane appears blocked during work/PR polling"); if (!session?.branch) markUnobservable(r, "worker branch/worktree metadata is not observable through CLI/API"); if (!pr) markUnobservable(r, "worker PR facts are not observable yet; continue polling manually or inspect the worker branch/worktree"); });
  if (report.failure) return finish(report, options, created, command);
  if (wants(options, "reviewer-testing")) await stage("reviewer", async (r) => { const worker = report.sessions.find((x) => x.role === "worker"); if (!worker?.id) throw new Error("reviewer-testing stage requires a worker from roles or --worker-session"); const payload = await poll(r, "review result", () => run(r, ["review", "ls", worker.id, "--json"], true), (x) => x?.reviews?.some((review) => reviewCompleted(review.latestRun))); const review = payload.reviews.find((item) => reviewCompleted(item.latestRun)); const latest = review.latestRun; const reviewerSessionID = options.reviewerSession || latest.sessionId; const tmux = reviewerSessionID ? await captureTmux(r, reviewerSessionID, "reviewer") : null; r.observed = { reviews: payload, selected: review, tmux: tmux ? { hasSession: tmux.hasSession, visibleBlockingPrompt: tmux.visibleBlockingPrompt ?? false } : null }; if (!report.sessions.some((x) => x.role === "reviewer" && x.id === reviewerSessionID)) report.sessions.push({ role: "reviewer", id: reviewerSessionID, reviewRunId: latest.id, harness: latest.harness || options.reviewerHarness || "configured-by-AO", verdict: latest.verdict, status: latest.status }); if (tmux?.visibleBlockingPrompt) markUnobservable(r, "reviewer tmux pane appears blocked on a prompt"); if (!reviewerSessionID || !latest.verdict || latest.verdict === "none") markUnobservable(r, "review run exists but reviewer session or verdict is not exposed"); });
  if (report.failure) return finish(report, options, created, command);
  if (wants(options, "lifecycle")) await stage("lifecycle", async (r) => { const targetID = options.lifecycleSession || options.workerSession || report.sessions.find((x) => x.role === "worker")?.id || options.orchestratorSession; if (!targetID) throw new Error("lifecycle stage requires --lifecycle-session, --worker-session, or a worker from roles"); const before = await getSessionDetail(r, targetID); const killResult = await command(["session", "kill", targetID, "--project", options.project]); r.evidence.push({ command: [ao, "session", "kill", targetID, "--project", options.project], result: killResult }); if (killResult.code !== 0) throw new Error(`session kill ${targetID}: ${killResult.stderr || killResult.stdout || `exit ${killResult.code}`}`); const killed = await poll(r, "session killed", () => getSessionDetail(r, targetID), (x) => sessionFrom(x)?.isTerminated === true); const restoreResult = await command(["session", "restore", targetID, "--project", options.project]); r.evidence.push({ command: [ao, "session", "restore", targetID, "--project", options.project], result: restoreResult }); if (restoreResult.code !== 0) throw new Error(`session restore ${targetID}: ${restoreResult.stderr || restoreResult.stdout || `exit ${restoreResult.code}`}`); const restored = await poll(r, "session restored", () => getSessionDetail(r, targetID), (x) => sessionFrom(x)?.isTerminated === false); const tmux = await captureTmux(r, targetID, "lifecycle-restored"); r.observed = { sessionId: targetID, before, killed, restored, tmux: { hasSession: tmux.hasSession, visibleBlockingPrompt: tmux.visibleBlockingPrompt ?? false } }; if (tmux.visibleBlockingPrompt) markUnobservable(r, "restored tmux pane appears blocked on a prompt"); });
  return finish(report, options, created, command);
}

async function finish(report, options, created, command) {
  if (options.cleanup) for (const id of [...created].reverse()) report.cleanup.results.push({ id, result: await command(["session", "kill", id]) });
  const counts = report.stages.reduce((a, x) => { a[x.status] = (a[x.status] || 0) + 1; return a; }, {});
  if (report.failure || (counts.unobservable || 0) > 0) report.diagnostics = await collectIssueDiagnostics(report, options, command, counts);
  report.finishedAt = new Date().toISOString(); await save(report, options.report);
  console.log(`AO agent E2E: ${counts.passed || 0} passed, ${counts.unobservable || 0} unobservable, ${counts.failed || 0} failed`); if (options.report) console.log(`Report: ${resolve(expandHome(options.report))}`); if (report.failure) { console.error(`Failed at ${report.failure.stage}: ${report.failure.reason}`); return 1; }
  if ((counts.unobservable || 0) > 0 && !options.allowUnobservable) {
    console.error("Unobservable evidence is non-passing by default. Re-run with --allow-unobservable only for diagnostic baselines.");
    return 1;
  }
  return 0;
}

async function collectIssueDiagnostics(report, options, command, counts) {
  const diagnostics = {
    capturedAt: new Date().toISOString(),
    trigger: report.failure ? "failure" : "unobservable",
    counts,
    sessions: [],
    commands: [],
  };
  for (const args of [
    ["status", "--json"],
    ["session", "ls", "--project", options.project, "--all", "--json"],
  ]) {
    const result = await command(args);
    diagnostics.commands.push({ command: args, result });
  }
  const known = new Map();
  for (const item of report.sessions) if (item?.id) known.set(item.id, item);
  for (const stage of report.stages) {
    for (const value of strings(stage.observed ?? {})) {
      if (/^[A-Za-z0-9_-]{8,}$/.test(value) && report.sessions.some((item) => item.id === value)) known.set(value, report.sessions.find((item) => item.id === value));
    }
  }
  for (const [sessionID, metadata] of known) {
    const detail = await command(["session", "get", sessionID, "--json"]);
    const tmux = await captureTmuxDiagnostics(sessionID, options);
    const entry = {
      id: sessionID,
      role: metadata.role,
      harness: metadata.harness,
      detail,
      tmux,
    };
    if (metadata.role === "worker") entry.reviews = await command(["review", "ls", sessionID, "--json"]);
    diagnostics.sessions.push(entry);
  }
  diagnostics.summary = diagnostics.sessions.map((session) => ({
    id: session.id,
    role: session.role,
    hasTmux: session.tmux.hasSession,
    signals: session.tmux.signals,
    detailExit: session.detail.code,
  }));
  return diagnostics;
}

async function captureTmuxDiagnostics(sessionID, options) {
  const target = `${sessionID}:0.0`;
  const hasSessionResult = await runCommand(["tmux", "has-session", "-t", sessionID], options.commandTimeoutSeconds);
  const tmux = { target, hasSession: hasSessionResult.code === 0, hasSessionResult, signals: ["tmux-session-missing"] };
  if (hasSessionResult.code !== 0) return tmux;
  const captureResult = await runCommand(["tmux", "capture-pane", "-t", target, "-p", "-S", `-${options.tmuxLines}`], options.commandTimeoutSeconds);
  tmux.captureResult = captureResult;
  tmux.visibleBlockingPrompt = visibleBlockingPrompt(captureResult.stdout);
  tmux.signals = paneSignals(captureResult.stdout);
  return tmux;
}

main().then((code) => { process.exitCode = code; }).catch((error) => { console.error(error.stack || error); process.exitCode = 1; });
