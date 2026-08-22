#!/usr/bin/env node
import { execFileSync, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { homedir, platform, tmpdir } from "node:os";
import path from "node:path";

const DEFAULT_TIMEOUT_MS = 180_000;
const POLL_MS = 5_000;
const ROLE_BOTH = "both";
const DEFAULT_ORCHESTRATOR_SEND_DELAY_MS = 8_000;
const DEFAULT_PERMISSION_MODE = "bypass-permissions";
const CURSOR_TRUST_READY_TIMEOUT_MS = 60_000;
const ORCHESTRATOR_READY_TIMEOUT_MS = 60_000;
const ORCHESTRATOR_DELIVERY_TIMEOUT_MS = 60_000;
const IDLE_ENTER_MS = 7_000;
const IDLE_POLL_MS = 1_000;
const PERMISSION_MODES = new Set(["default", "accept-edits", "auto", "bypass-permissions"]);

main().catch((err) => {
  console.error(`agent-role-smoke: ${err.message}`);
  if (err.cause?.stdout || err.cause?.stderr) {
    if (err.cause.stdout) console.error(err.cause.stdout);
    if (err.cause.stderr) console.error(err.cause.stderr);
  }
  process.exitCode = 1;
});

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) {
    printHelp();
    return;
  }

  try {
    const status = runAOJSON(opts, ["status", "--json"]);
    if (status.state !== "ready") {
      throw new Error(`AO daemon is ${status.state}; start AO before running this smoke test`);
    }
    if (!status.port) {
      throw new Error("AO status did not report a daemon port");
    }

    const harnesses = discoverHarnesses(opts);
    if (harnesses.length === 0) {
      throw new Error("No harnesses selected. Use --agent, --agents, or check `ao agent ls --refresh --json`.");
    }
    if (opts.preflightClean) {
      cleanupStaleSmokeState(opts, harnesses);
    }

    const fixture = createFixture();
    const projectID = `role-smoke-${Date.now().toString(36)}`;
    const createdSessions = new Set();
    const results = [];

    try {
      runAO(opts, ["project", "add", "--path", fixture.root, "--id", projectID, "--name", "AO Role Smoke"]);

      for (const harness of harnesses) {
        if (opts.role === "worker" || opts.role === ROLE_BOTH) {
          results.push(await runWorkerSmoke(opts, status, fixture, projectID, harness, createdSessions));
        }
        if (opts.role === "orchestrator" || opts.role === ROLE_BOTH) {
          results.push(await runOrchestratorSmoke(opts, status, fixture, projectID, harness, createdSessions));
        }
      }
    } finally {
      if (!opts.keep) {
        for (const id of [...createdSessions].reverse()) {
          try {
            runAO(opts, ["session", "kill", id]);
          } catch {
            // Best-effort cleanup. The summary already records test failures.
          }
        }
        try {
          runAO(opts, ["project", "rm", projectID, "--yes"]);
        } catch {
          // Project cleanup is best-effort because active worktrees can block removal.
        }
        try {
          rmSync(fixture.root, { recursive: true, force: true });
        } catch {
          // Temp fixture cleanup is best-effort.
        }
      }
    }

    printSummary(results, opts.keep ? fixture.root : "");
    if (results.some((r) => r.status === "fail")) {
      process.exitCode = 1;
    }
  } finally {
    teardownAO(opts);
  }
}

async function runWorkerSmoke(opts, status, fixture, projectID, harness, createdSessions) {
  const result = { harness, role: "worker", status: "fail", detail: "" };
  try {
    setProjectConfig(opts, projectID, {
      agentRules: "AO role smoke worker rule: complete only the assigned fixture edit and report the changed file.",
      agentConfig: permissionAgentConfig(opts),
      worker: { agent: harness, agentConfig: permissionAgentConfig(opts) },
    });

    const spawnText = runAO(opts, [
      "spawn",
      "--project",
      projectID,
      "--harness",
      harness,
      "--name",
      shortName(`worker-${harness}`),
      "--prompt",
      workerTaskPrompt(),
      "--skip-agent-check",
    ]);
    const sessionID = parseSpawnedSessionID(spawnText);
    createdSessions.add(sessionID);

    await waitForSession(opts, projectID, sessionID, DEFAULT_TIMEOUT_MS);
    const session = await getSession(status.port, sessionID, opts);
    assertSession(session, harness, "worker");
    assertSystemPrompt(status.dataDir, sessionID, "AO Worker Role");

    const workspace = await waitForWorkspace(fixture.root, session.branch, opts.timeoutMs);
    const changed = await waitForFixtureChange(workspace, opts.timeoutMs, opts, sessionID);
    if (!changed) {
      throw new Error(`worker ${sessionID} did not produce the expected fixture edit before timeout`);
    }

    result.status = "pass";
    result.detail = `${sessionID} changed fixture in ${workspace}`;
  } catch (err) {
    result.detail = err.message;
  }
  return result;
}

async function runOrchestratorSmoke(opts, status, fixture, projectID, harness, createdSessions) {
  const result = { harness, role: "orchestrator", status: "fail", detail: "" };
  try {
    setProjectConfig(opts, projectID, {
      orchestratorRules: "AO role smoke orchestrator rule: delegate implementation to a worker; do not edit files in the orchestrator workspace.",
      agentConfig: permissionAgentConfig(opts),
      worker: { agent: harness, agentConfig: permissionAgentConfig(opts) },
      orchestrator: { agent: harness, agentConfig: permissionAgentConfig(opts) },
    });

    const before = listSessions(opts, projectID, true).map((s) => s.id);
    if (harness === "cursor") {
      pretrustCursorWorkspace(opts, predictedOrchestratorWorkspace(status.dataDir, projectID));
    }
    const body = await apiJSON(status.port, "POST", "/orchestrators", { projectId: projectID, clean: true }, opts);
    const sessionID = body.orchestrator?.id;
    if (!sessionID) {
      throw new Error("orchestrator spawn response did not include orchestrator.id");
    }
    createdSessions.add(sessionID);

    await waitForSession(opts, projectID, sessionID, DEFAULT_TIMEOUT_MS);
    const session = await getSession(status.port, sessionID, opts);
    assertSession(session, harness, "orchestrator");
    assertSystemPrompt(status.dataDir, sessionID, "AO Orchestrator Role");

    if (opts.orchestratorSendDelayMs > 0) {
      await sleep(opts.orchestratorSendDelayMs);
    }
    await preparePostStartSend(opts, harness, sessionID);
    const taskPrompt = orchestratorTaskPrompt(opts, projectID, harness);
    await sendTaskToOrchestrator(opts, sessionID, taskPrompt, orchestratorTaskMarker(projectID, harness));

    const afterWorker = await waitForNewWorker(opts, projectID, before, opts.timeoutMs, sessionID);
    createdSessions.add(afterWorker.id);

    const orchestratorWorkspace = await waitForWorkspace(fixture.root, session.branch, opts.timeoutMs);
    const orchestratorDiff = gitOutput(["-C", orchestratorWorkspace, "status", "--porcelain"]);
    if (orchestratorDiff.trim() !== "") {
      throw new Error(`orchestrator ${sessionID} modified its own workspace:\n${orchestratorDiff}`);
    }

    result.status = "pass";
    result.detail = `${sessionID} delegated to worker ${afterWorker.id}`;
  } catch (err) {
    result.detail = err.message;
  }
  return result;
}

function parseArgs(args) {
  const opts = {
    aoBin: process.env.AO_BIN || "ao",
    agent: "",
    agents: "",
    allSupported: false,
    role: ROLE_BOTH,
    timeoutMs: DEFAULT_TIMEOUT_MS,
    orchestratorSendDelayMs: DEFAULT_ORCHESTRATOR_SEND_DELAY_MS,
    permissionMode: DEFAULT_PERMISSION_MODE,
    manualPermissions: false,
    preflightClean: true,
    stopAOAfter: false,
    stopElectronAfter: false,
    keep: false,
    verbose: false,
    help: false,
  };
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    switch (arg) {
      case "--help":
      case "-h":
        opts.help = true;
        break;
      case "--ao-bin":
        opts.aoBin = requireValue(args, ++i, arg);
        break;
      case "--agent":
        opts.agent = requireValue(args, ++i, arg);
        break;
      case "--agents":
        opts.agents = requireValue(args, ++i, arg);
        break;
      case "--role":
        opts.role = requireValue(args, ++i, arg);
        if (!["worker", "orchestrator", ROLE_BOTH].includes(opts.role)) {
          throw new Error("--role must be worker, orchestrator, or both");
        }
        break;
      case "--timeout-ms":
        opts.timeoutMs = Number(requireValue(args, ++i, arg));
        if (!Number.isFinite(opts.timeoutMs) || opts.timeoutMs <= 0) {
          throw new Error("--timeout-ms must be a positive number");
        }
        break;
      case "--orchestrator-send-delay-ms":
        opts.orchestratorSendDelayMs = Number(requireValue(args, ++i, arg));
        if (!Number.isFinite(opts.orchestratorSendDelayMs) || opts.orchestratorSendDelayMs < 0) {
          throw new Error("--orchestrator-send-delay-ms must be zero or a positive number");
        }
        break;
      case "--permission-mode":
        opts.permissionMode = requireValue(args, ++i, arg);
        if (!PERMISSION_MODES.has(opts.permissionMode)) {
          throw new Error("--permission-mode must be default, accept-edits, auto, or bypass-permissions");
        }
        break;
      case "--manual-permissions":
        opts.manualPermissions = true;
        break;
      case "--no-preflight-clean":
        opts.preflightClean = false;
        break;
      case "--stop-ao-after":
        opts.stopAOAfter = true;
        break;
      case "--stop-electron-after":
        opts.stopElectronAfter = true;
        break;
      case "--all-supported":
        opts.allSupported = true;
        break;
      case "--keep":
        opts.keep = true;
        break;
      case "--verbose":
        opts.verbose = true;
        break;
      default:
        throw new Error(`unknown argument: ${arg}`);
    }
  }
  if (opts.agent && opts.agents) {
    throw new Error("use either --agent or --agents, not both");
  }
  return opts;
}

function printHelp() {
  console.log(`Usage: node skills/agent-role-smoke/scripts/agent-role-smoke.mjs [options]

Options:
  --agent <id>              Test one harness
  --agents <a,b,c>          Test a comma-separated harness list
  --role <role>             worker, orchestrator, or both (default: both)
  --all-supported           Attempt every supported harness
  --ao-bin <path>           Use a specific ao binary (default: AO_BIN or ao)
  --timeout-ms <ms>         Poll timeout for live checks (default: ${DEFAULT_TIMEOUT_MS})
  --orchestrator-send-delay-ms <ms>
                            Delay before sending to a fresh orchestrator (default: ${DEFAULT_ORCHESTRATOR_SEND_DELAY_MS})
  --permission-mode <mode>  AO permission mode for test sessions (default: ${DEFAULT_PERMISSION_MODE})
                            One of default, accept-edits, auto, bypass-permissions
  --manual-permissions      Do not set AO permissions in project config; answer native prompts manually
  --no-preflight-clean      Do not remove stale role-smoke-* projects/sessions before testing
  --stop-ao-after           Run \`ao stop\` after the smoke test finishes
  --stop-electron-after     Best-effort stop of local AO/Electron desktop processes after the test
  --keep                    Keep sessions, AO project, and fixture dir
  --verbose                 Print command/API details
  -h, --help                Show this help
`);
}

function requireValue(args, index, flag) {
  const value = args[index];
  if (!value || value.startsWith("--")) {
    throw new Error(`${flag} requires a value`);
  }
  return value;
}

function discoverHarnesses(opts) {
  if (opts.agent) return [opts.agent.trim()].filter(Boolean);
  if (opts.agents) {
    return opts.agents.split(",").map((v) => v.trim()).filter(Boolean);
  }

  const inv = runAOJSON(opts, ["agent", "ls", "--refresh", "--json"]);
  const source = opts.allSupported ? inv.supported : selectRunnableAgents(inv);
  return [...new Set(source.map((info) => info.id).filter(Boolean))];
}

function selectRunnableAgents(inv) {
  const authorized = inv.authorized || [];
  if (authorized.length > 0) return authorized;
  return (inv.installed || []).filter((info) => info.authStatus !== "unauthorized");
}

function cleanupStaleSmokeState(opts, targetHarnesses) {
  const target = new Set(targetHarnesses);
  const sessions = listSessions(opts, "", true);
  const staleSessions = sessions.filter((s) => String(s.projectId || "").startsWith("role-smoke-") && !s.isTerminated);
  for (const session of staleSessions.reverse()) {
    if (session.role === "orchestrator" && session.harness && !target.has(session.harness)) {
      logVerbose(opts, `stale smoke orchestrator ${session.id} uses ${session.harness}; replacing for ${targetHarnesses.join(",")}`);
    } else {
      logVerbose(opts, `removing stale smoke session ${session.id}`);
    }
    try {
      runAO(opts, ["session", "kill", session.id]);
    } catch (err) {
      logVerbose(opts, `could not kill stale smoke session ${session.id}: ${err.message}`);
    }
  }

  const projects = listProjects(opts).filter((p) => String(p.id || "").startsWith("role-smoke-"));
  for (const project of projects) {
    try {
      runAO(opts, ["project", "rm", project.id, "--yes"]);
    } catch (err) {
      logVerbose(opts, `could not remove stale smoke project ${project.id}: ${err.message}`);
    }
  }
}

function listProjects(opts) {
  const out = runAOJSON(opts, ["project", "ls", "--json"]);
  return out.projects || out.data || [];
}

function createFixture() {
  const root = mkdtempSync(path.join(tmpdir(), "ao-agent-role-smoke-"));
  const src = path.join(root, "src");
  writeFileSync(path.join(root, "package.json"), JSON.stringify({ scripts: { test: "node -e \"console.log('fixture ok')\"" } }, null, 2) + "\n");
  mkdirSync(src, { recursive: true });
  writeFileSync(
    path.join(src, "App.jsx"),
    `export function App() {
  return (
    <main>
      <button className="new-task" style={{ backgroundColor: "blue" }}>New Task</button>
      <span className="notification-icon" style={{ color: "gray" }}>!</span>
    </main>
  );
}
`,
  );
  gitOutput(["-C", root, "init"]);
  gitOutput(["-C", root, "add", "."]);
  gitOutput(["-C", root, "-c", "user.name=AO Smoke", "-c", "user.email=ao-smoke@example.invalid", "commit", "-m", "initial fixture"]);
  return { root };
}

function setProjectConfig(opts, projectID, config) {
  runAO(opts, ["project", "set-config", projectID, "--config-json", JSON.stringify(config)]);
}

function permissionAgentConfig(opts) {
  if (opts.manualPermissions) return {};
  return { permissions: opts.permissionMode };
}

function workerTaskPrompt() {
  return `Change the New Task button color to green in src/App.jsx. If that is not straightforward, change the notification icon color to red instead. Keep the change minimal and report the file changed.`;
}

function orchestratorTaskPrompt(opts, projectID, harness) {
  const marker = orchestratorTaskMarker(projectID, harness);
  return `${marker}

Please get this fixture UI change done: change the New Task button color to green in src/App.jsx, or change the notification icon color to red.

You are testing the ${harness} orchestrator role for AO project ${projectID}.
The AO daemon is already running. Do not run \`ao start\`, do not open Electron, and do not use a different AO binary.
Use this exact AO command shape if you need to delegate:

\`${opts.aoBin} spawn --project ${projectID} --harness ${harness} --name ui-green-btn --prompt "Change the New Task button color to green in src/App.jsx. If that is not straightforward, change the notification icon color to red instead. Keep the change minimal and report the file changed." --skip-agent-check\`

Delegate the implementation to a worker session and do not edit files in your own orchestrator workspace.`;
}

function orchestratorTaskMarker(projectID, harness) {
  return `AO_ROLE_SMOKE_TASK ${projectID} ${harness}`;
}

function runAO(opts, args) {
  if (opts.verbose) console.error(`$ ${opts.aoBin} ${args.join(" ")}`);
  const res = spawnSync(opts.aoBin, args, { encoding: "utf8" });
  if (res.status !== 0) {
    const err = new Error(`ao ${args.join(" ")} failed with exit ${res.status}`);
    err.cause = { stdout: res.stdout, stderr: res.stderr };
    throw err;
  }
  return res.stdout;
}

function teardownAO(opts) {
  if (opts.stopAOAfter) {
    try {
      runAO(opts, ["stop"]);
    } catch (err) {
      logVerbose(opts, `ao stop failed: ${err.message}`);
    }
  }
  if (opts.stopElectronAfter) {
    stopElectron(opts);
  }
}

function stopElectron(opts) {
  if (platform() === "win32") {
    spawnSync("taskkill", ["/IM", "Agent Orchestrator.exe", "/F"], { encoding: "utf8" });
    return;
  }
  const patterns = [
    "Agent Orchestrator",
    "Electron.*agent-orchestrator",
    "electron-forge.*agent-orchestrator",
  ];
  for (const pattern of patterns) {
    const res = spawnSync("pkill", ["-f", pattern], { encoding: "utf8" });
    if (opts.verbose && res.status === 0) {
      console.error(`stopped Electron process matching ${pattern}`);
    }
  }
}

function logVerbose(opts, message) {
  if (opts.verbose) console.error(`agent-role-smoke: ${message}`);
}

function runAOJSON(opts, args) {
  const out = runAO(opts, args);
  try {
    return JSON.parse(out);
  } catch (err) {
    throw new Error(`ao ${args.join(" ")} did not return JSON: ${err.message}`);
  }
}

async function apiJSON(port, method, apiPath, body, opts) {
  const url = `http://127.0.0.1:${port}/api/v1${apiPath}`;
  if (opts.verbose) console.error(`${method} ${url}`);
  const res = await fetch(url, {
    method,
    headers: { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let parsed = {};
  if (text.trim()) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { raw: text };
    }
  }
  if (!res.ok) {
    throw new Error(`${method} ${apiPath} failed with HTTP ${res.status}: ${text}`);
  }
  return parsed;
}

function parseSpawnedSessionID(output) {
  const match = output.match(/spawned session\s+(\S+)/);
  if (!match) {
    throw new Error(`could not parse spawned session id from:\n${output}`);
  }
  return match[1];
}

function listSessions(opts, projectID, includeOrchestrators = false) {
  const args = ["session", "ls", "--include-terminated", "--json"];
  if (projectID) args.splice(2, 0, "--project", projectID);
  if (includeOrchestrators) args.splice(2, 0, "--all");
  return runAOJSON(opts, args).data || [];
}

async function waitForSession(opts, projectID, sessionID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const sessions = listSessions(opts, projectID, true);
    const found = sessions.find((s) => s.id === sessionID);
    if (found) return found;
    await sleep(POLL_MS);
  }
  throw new Error(`session ${sessionID} did not appear before timeout`);
}

async function getSession(port, sessionID, opts) {
  const body = await apiJSON(port, "GET", `/sessions/${encodeURIComponent(sessionID)}`, undefined, opts);
  if (!body.session) {
    throw new Error(`GET session ${sessionID} did not include session`);
  }
  return body.session;
}

async function waitForNewWorker(opts, projectID, beforeIDs, timeoutMs, sessionID = "") {
  const before = new Set(beforeIDs);
  const deadline = Date.now() + timeoutMs;
  let sentEnter = false;
  const idleEnter = createIdleEnterTracker(opts, sessionID, "orchestrator delegation poll");
  while (Date.now() < deadline) {
    const sessions = listSessions(opts, projectID, true);
    const found = sessions.find((s) => s.role === "worker" && !before.has(s.id));
    if (found) return found;
    if (sessionID) {
      idleEnter(captureTmuxPane(opts, sessionID, 180));
    }
    if (!sentEnter && acknowledgeVisibleBlockingPrompt(opts, sessionID, "orchestrator delegation poll")) {
      sentEnter = true;
    }
    await sleep(IDLE_POLL_MS);
  }
  throw new Error("orchestrator did not create or redirect a new worker before timeout");
}

function assertSession(session, harness, role) {
  const actualRole = session.role || session.kind;
  if (actualRole !== role) {
    throw new Error(`session ${session.id} role = ${actualRole}, want ${role}`);
  }
  if (session.harness !== harness) {
    throw new Error(`session ${session.id} harness = ${session.harness}, want ${harness}`);
  }
}

function assertSystemPrompt(dataDir, sessionID, expectedMarker) {
  const file = path.join(dataDir, "prompts", sessionID, "system.md");
  if (!existsSync(file)) {
    throw new Error(`missing system prompt artifact: ${file}`);
  }
  const body = readFileSync(file, "utf8");
  if (!body.trim()) {
    throw new Error(`system prompt artifact is empty: ${file}`);
  }
  if (!body.includes(expectedMarker)) {
    throw new Error(`system prompt artifact ${file} does not include ${expectedMarker}`);
  }
}

async function waitForWorkspace(repoRoot, branch, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const worktree = findWorktreeByBranch(repoRoot, branch);
    if (worktree) return worktree;
    await sleep(1_000);
  }
  throw new Error(`could not find worktree for branch ${branch}`);
}

function findWorktreeByBranch(repoRoot, branch) {
  if (!branch) return "";
  const out = gitOutput(["-C", repoRoot, "worktree", "list", "--porcelain"]);
  const entries = out.split(/\n\n+/);
  for (const entry of entries) {
    const lines = entry.split("\n");
    const worktree = lines.find((line) => line.startsWith("worktree "))?.slice("worktree ".length);
    const foundBranch = lines.find((line) => line.startsWith("branch "))?.slice("branch refs/heads/".length);
    if (worktree && foundBranch === branch) {
      return worktree;
    }
  }
  return "";
}

async function waitForFixtureChange(workspace, timeoutMs, opts, sessionID = "") {
  const deadline = Date.now() + timeoutMs;
  let sentEnter = false;
  const idleEnter = createIdleEnterTracker(opts, sessionID, "fixture-change poll");
  while (Date.now() < deadline) {
    const file = path.join(workspace, "src", "App.jsx");
    if (existsSync(file)) {
      const body = readFileSync(file, "utf8").toLowerCase();
      const changed = body.includes("green") || body.includes("#008000") || body.includes("rgb(0, 128, 0)") || body.includes("red") || body.includes("#ff0000");
      const dirty = gitOutput(["-C", workspace, "status", "--porcelain"]).trim() !== "";
      if (changed && dirty) return true;
    }
    if (sessionID) {
      idleEnter(captureTmuxPane(opts, sessionID, 180));
    }
    if (!sentEnter && acknowledgeVisibleBlockingPrompt(opts, sessionID, "fixture-change poll")) {
      sentEnter = true;
    }
    await sleep(IDLE_POLL_MS);
  }
  return false;
}

function sendEnterToTmux(opts, sessionID, reason) {
  sendKeysToTmux(opts, sessionID, ["Enter"], reason);
}

function sendKeysToTmux(opts, sessionID, keys, reason) {
  if (!sessionID) return;
  const target = `${sessionID}:0.0`;
  const res = spawnSync("tmux", ["send-keys", "-t", target, ...keys], { encoding: "utf8" });
  if (res.status === 0) {
    logVerbose(opts, `sent ${keys.join(" ")} to tmux pane ${target} during ${reason}`);
    return;
  }
  logVerbose(opts, `could not send ${keys.join(" ")} to tmux pane ${target}: ${res.stderr || res.stdout || `exit ${res.status}`}`);
}

async function preparePostStartSend(opts, harness, sessionID) {
  await waitForOrchestratorPane(opts, sessionID, ORCHESTRATOR_READY_TIMEOUT_MS);
  await clearStartupBlockingPrompt(opts, harness, sessionID, ORCHESTRATOR_READY_TIMEOUT_MS);
  const ready = await waitForOrchestratorPromptReady(opts, harness, sessionID, ORCHESTRATOR_READY_TIMEOUT_MS);
  if (!ready) {
    throw new Error(`orchestrator ${sessionID} did not reach a usable prompt before task injection`);
  }
}

async function waitForOrchestratorPane(opts, sessionID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  const idleEnter = createIdleEnterTracker(opts, sessionID, "orchestrator pane startup");
  while (Date.now() < deadline) {
    const output = captureTmuxPane(opts, sessionID, 120);
    if (output.trim()) return output;
    idleEnter(output);
    await sleep(1_000);
  }
  throw new Error(`orchestrator ${sessionID} tmux pane did not produce output before timeout`);
}

async function clearStartupBlockingPrompt(opts, harness, sessionID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let answered = false;
  const idleEnter = createIdleEnterTracker(opts, sessionID, "startup prompt clear");
  while (Date.now() < deadline) {
    const output = captureTmuxPane(opts, sessionID, 140);
    if (harness === "cursor" && cursorWorkspaceTrustVisible(output)) {
      sendKeysToTmux(opts, sessionID, ["a"], "cursor workspace trust prompt before send");
      await sleep(1_000);
      continue;
    }
    if (visibleBlockingPrompt(output) && !answered) {
      sendEnterToTmux(opts, sessionID, "startup blocking prompt before send");
      answered = true;
      await sleep(1_000);
      continue;
    }
    idleEnter(output);
    return;
  }
}

async function waitForOrchestratorPromptReady(opts, harness, sessionID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  const idleEnter = createIdleEnterTracker(opts, sessionID, "orchestrator prompt readiness");
  while (Date.now() < deadline) {
    const output = captureTmuxPane(opts, sessionID, 140);
    if (orchestratorPromptReady(output, harness)) return true;
    idleEnter(output);
    await sleep(1_000);
  }
  return false;
}

function orchestratorPromptReady(output, harness) {
  if (!output.trim()) return false;
  if (harness === "cursor" && cursorWorkspaceTrustVisible(output)) return false;
  if (visibleBlockingPrompt(output)) return false;
  return true;
}

async function sendTaskToOrchestrator(opts, sessionID, message, marker) {
  const deadline = Date.now() + ORCHESTRATOR_DELIVERY_TIMEOUT_MS;
  let attempts = 0;
  let acknowledged = false;
  let lastError = "";
  while (Date.now() < deadline && attempts < 3) {
    attempts += 1;
    const beforeOutput = captureTmuxPane(opts, sessionID, 180);
    try {
      runAO(opts, ["send", "--session", sessionID, "--message", message]);
    } catch (err) {
      lastError = err.message;
      if (!sendFailedOnDecision(err)) throw err;
      acknowledgeVisibleBlockingPrompt(opts, sessionID, `send attempt ${attempts} blocked by decision prompt`);
      await sleep(2_000);
      continue;
    }

    if (await waitForTaskDeliveryEvidence(opts, sessionID, marker, beforeOutput, 10_000)) return;

    if (!acknowledged) {
      acknowledgeVisibleBlockingPrompt(opts, sessionID, "task delivery verification");
      acknowledged = true;
      await sleep(2_000);
      continue;
    }
  }
  const pane = captureTmuxPane(opts, sessionID, 160);
  const extra = lastError ? ` Last send error: ${lastError}.` : "";
  throw new Error(`task message marker ${marker} did not appear in orchestrator ${sessionID} pane after send.${extra}\nRecent pane output:\n${pane}`);
}

async function waitForTaskDeliveryEvidence(opts, sessionID, marker, beforeOutput, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  const beforePastes = pastedTextCount(beforeOutput);
  const idleEnter = createIdleEnterTracker(opts, sessionID, "task delivery evidence");
  while (Date.now() < deadline) {
    const output = captureTmuxPane(opts, sessionID, 180);
    if (output.includes(marker)) return true;
    if (pastedTextCount(output) > beforePastes) return true;
    idleEnter(output);
    await sleep(1_000);
  }
  return false;
}

function pastedTextCount(output) {
  return (output.match(/\[Pasted text #\d+/g) || []).length;
}

function acknowledgeVisibleBlockingPrompt(opts, sessionID, reason) {
  if (!sessionID) return false;
  const output = captureTmuxPane(opts, sessionID, 140);
  if (!visibleBlockingPrompt(output)) return false;
  sendEnterToTmux(opts, sessionID, reason);
  return true;
}

function createIdleEnterTracker(opts, sessionID, reason) {
  let lastOutput = null;
  let lastChangedAt = Date.now();
  let lastEnterAt = 0;
  return (output) => {
    if (!sessionID) return;
    const now = Date.now();
    if (lastOutput === null || output !== lastOutput) {
      lastOutput = output;
      lastChangedAt = now;
      return;
    }
    if (now - lastChangedAt < IDLE_ENTER_MS || now - lastEnterAt < IDLE_ENTER_MS) {
      return;
    }
    sendEnterToTmux(opts, sessionID, `${reason}; no tmux output change for ${IDLE_ENTER_MS / 1_000}s`);
    lastEnterAt = now;
    lastChangedAt = now;
  };
}

function captureTmuxPane(opts, sessionID, lines) {
  const target = `${sessionID}:0.0`;
  const res = spawnSync("tmux", ["capture-pane", "-t", target, "-p", "-S", `-${lines}`], { encoding: "utf8" });
  if (res.status === 0) return res.stdout;
  logVerbose(opts, `could not capture tmux pane ${target}: ${res.stderr || res.stdout || `exit ${res.status}`}`);
  return "";
}

function cursorWorkspaceTrustVisible(output) {
  return output.includes("Workspace Trust Required") || output.includes("Trusting workspace");
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
    normalized.includes("permission denied") ||
    normalized.includes("permission prompt") ||
    normalized.includes("do you want to proceed") ||
    normalized.includes("press enter") ||
    normalized.includes("hit enter") ||
    /\[(y|yes)\/(n|no)\]/i.test(output) ||
    /\((y|yes)\)/i.test(output)
  );
}

function sendFailedOnDecision(err) {
  const text = `${err.message || ""}\n${err.cause?.stdout || ""}\n${err.cause?.stderr || ""}`;
  return text.includes("SESSION_AWAITING_DECISION") || text.toLowerCase().includes("awaiting decision");
}

function predictedOrchestratorWorkspace(dataDir, projectID) {
  const prefix = String(projectID).slice(0, 12);
  return path.join(dataDir, "worktrees", String(projectID), "orchestrator", `${prefix}-orchestrator`);
}

function pretrustCursorWorkspace(opts, workspacePath) {
  const dir = path.join(homedir(), ".cursor", "projects", cursorProjectKey(workspacePath));
  mkdirSync(dir, { recursive: true });
  const trustPath = path.join(dir, ".workspace-trusted");
  writeFileSync(
    trustPath,
    JSON.stringify({ trustedAt: new Date().toISOString(), workspacePath }, null, 2) + "\n",
    { mode: 0o644 },
  );
  const repoPath = path.join(dir, "repo.json");
  if (!existsSync(repoPath)) {
    writeFileSync(repoPath, JSON.stringify({ id: randomUUID() }, null, 2) + "\n", { mode: 0o644 });
  }
  logVerbose(opts, `pretrusted Cursor workspace ${workspacePath}`);
}

function cursorProjectKey(workspacePath) {
  return path.resolve(workspacePath).replace(/^[\\/]+/, "").replace(/[^A-Za-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
}

function gitOutput(args) {
  return execFileSync("git", args, { encoding: "utf8" });
}

function shortName(name) {
  return name.replace(/[^a-z0-9-]/gi, "-").slice(0, 20);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function printSummary(results, keptFixture) {
  console.log("\nAgent role smoke summary");
  for (const result of results) {
    const icon = result.status === "pass" ? "PASS" : "FAIL";
    console.log(`${icon} ${result.harness} ${result.role}: ${result.detail}`);
  }
  if (keptFixture) {
    const exists = existsSync(keptFixture) && statSync(keptFixture).isDirectory();
    if (exists) console.log(`Kept fixture: ${keptFixture}`);
  }
}
