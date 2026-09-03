import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const runner = fileURLToPath(new URL("./run_agent_e2e.mjs", import.meta.url));

function run(argv) {
  return new Promise((resolve, reject) => {
    let stdout = "";
    let stderr = "";
    const child = spawn(process.execPath, [runner, ...argv], { stdio: ["ignore", "pipe", "pipe"] });
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", (code) => resolve({ code, stdout, stderr }));
  });
}

async function fakeAO(dir, inventory) {
  const path = join(dir, "ao");
  const script = `#!/usr/bin/env node
const args = process.argv.slice(2);
if (args[0] === "version") process.stdout.write("ao test\\n");
else if (args[0] === "status") process.stdout.write(JSON.stringify({ port: 1, dataDir: "/tmp/ao-e2e-test" }));
else if (args[0] === "project" && args[1] === "get") process.stdout.write(JSON.stringify({ project: { id: args[2] } }));
else if (args[0] === "session" && args[1] === "ls") process.stdout.write(JSON.stringify({ sessions: [] }));
else if (args[0] === "session" && args[1] === "get") process.stdout.write(JSON.stringify({ session: { id: args[2], isTerminated: false } }));
else if (args[0] === "agent" && args[1] === "ls") process.stdout.write(${JSON.stringify(JSON.stringify(inventory))});
else if (args[0] === "spawn") { process.stderr.write("spawn must not run when readiness fails\\n"); process.exitCode = 99; }
else { process.stderr.write("unexpected command: " + args.join(" ")); process.exitCode = 98; }
`;
  await writeFile(path, script);
  await chmod(path, 0o755);
  return path;
}

test("roles blocks before spawn when fresh auth status is unknown", async () => {
  const dir = await mkdtemp(join(tmpdir(), "ao-agent-e2e-readiness-"));
  const reportPath = join(dir, "report.json");
  const ao = await fakeAO(dir, {
    supported: [{ id: "codex", label: "Codex" }],
    installed: [{ id: "codex", label: "Codex", authStatus: "unknown" }],
    authorized: [],
  });

  const result = await run(["--project", "test-project", "--harness", "codex", "--stages", "roles", "--ao", ao, "--report", reportPath]);
  assert.equal(result.code, 1);
  assert.match(result.stderr, /authorization could not be verified/i, result.stderr);
  const report = JSON.parse(await readFile(reportPath, "utf8"));
  assert.equal(report.failure.stage, "role-readiness");
  assert.equal(report.roleReadiness.harnesses[0].authorization, "unknown");
  assert.equal(report.roleReadiness.harnesses[0].reason, "authorization could not be verified by AO's fresh local probe");
  assert.deepEqual(report.stages.find((stage) => stage.name === "role-readiness").evidence[0].command.slice(-3), ["agent", "ls", "--refresh", "--json"].slice(-3));
  assert.equal(report.stages.some((stage) => stage.name === "orchestrator"), false);
});

test("roles reports when the installed harness requires authentication", async () => {
  const dir = await mkdtemp(join(tmpdir(), "ao-agent-e2e-readiness-"));
  const reportPath = join(dir, "report.json");
  const ao = await fakeAO(dir, {
    supported: [{ id: "codex", label: "Codex" }],
    installed: [{ id: "codex", label: "Codex", authStatus: "unauthorized" }],
    authorized: [],
  });

  const result = await run(["--project", "test-project", "--harness", "codex", "--stages", "roles", "--ao", ao, "--report", reportPath]);
  const report = JSON.parse(await readFile(reportPath, "utf8"));

  assert.equal(result.code, 1);
  assert.match(result.stderr, /authentication is required/i, result.stderr);
  assert.equal(report.roleReadiness.harnesses[0].installed, true);
  assert.equal(report.roleReadiness.harnesses[0].authorization, "unauthorized");
  assert.match(report.roleReadiness.harnesses[0].reason, /missing or invalid credentials/i);
  assert.equal(report.stages.some((stage) => stage.name === "orchestrator"), false);
});

test("roles reports when the harness CLI is not installed", async () => {
  const dir = await mkdtemp(join(tmpdir(), "ao-agent-e2e-readiness-"));
  const reportPath = join(dir, "report.json");
  const ao = await fakeAO(dir, {
    supported: [{ id: "codex", label: "Codex" }],
    installed: [],
    authorized: [],
  });

  const result = await run(["--project", "test-project", "--harness", "codex", "--stages", "roles", "--ao", ao, "--report", reportPath]);
  const report = JSON.parse(await readFile(reportPath, "utf8"));

  assert.equal(result.code, 1);
  assert.match(result.stderr, /agent CLI is not installed/i, result.stderr);
  assert.equal(report.roleReadiness.harnesses[0].installed, false);
  assert.equal(report.roleReadiness.harnesses[0].authorization, "unknown");
  assert.match(report.roleReadiness.harnesses[0].reason, /not installed/i);
  assert.equal(report.stages.some((stage) => stage.name === "orchestrator"), false);
});

test("authorized harnesses pass the readiness gate", async () => {
  const dir = await mkdtemp(join(tmpdir(), "ao-agent-e2e-readiness-"));
  const reportPath = join(dir, "report.json");
  const ao = await fakeAO(dir, {
    supported: [{ id: "codex", label: "Codex" }],
    installed: [{ id: "codex", label: "Codex", authStatus: "authorized" }],
    authorized: [{ id: "codex", label: "Codex", authStatus: "authorized" }],
  });

  const result = await run(["--project", "test-project", "--harness", "codex", "--stages", "roles", "--orchestrator-session", "orch-1", "--worker-session", "worker-1", "--ao", ao, "--report", reportPath]);
  const report = JSON.parse(await readFile(reportPath, "utf8"));

  assert.equal(result.code, 1);
  assert.equal(report.roleReadiness.harnesses[0].ready, true);
  assert.equal(report.roleReadiness.harnesses[0].authorization, "authorized");
  assert.equal(report.stages.find((stage) => stage.name === "role-readiness").status, "passed");
  assert.equal(report.stages.some((stage) => stage.name === "orchestrator"), true);
});
