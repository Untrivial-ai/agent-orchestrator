import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { npmInvocation } from "./build-acp-runtime-helpers.mjs";

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(scriptsDir, "..");
const sourceDir = join(frontendRoot, "device-runtime");
const outDir = join(frontendRoot, "resources", "device-runtime");
const sources = ["package.json", "package-lock.json", "ao-device-runner.mjs"];

// PR 1 is deliberately macOS-only. Keep other platform packages free of a
// convincing but unavailable local-device surface until their adapters land.
if (process.platform !== "darwin") {
	rmSync(outDir, { recursive: true, force: true });
	process.exit(0);
}

const signature = createHash("sha256");
for (const source of sources) signature.update(readFileSync(join(sourceDir, source)));
signature.update(readFileSync(fileURLToPath(import.meta.url)));
const expectedSignature = signature.digest("hex");
const markerPath = join(outDir, ".ao-device-runtime.json");
const agentEntry = join(outDir, "node_modules", "agent-device", "bin", "agent-device.mjs");
const aoRunner = join(outDir, "ao-device-runner.mjs");
if (existsSync(markerPath) && existsSync(agentEntry) && existsSync(aoRunner)) {
	const marker = JSON.parse(readFileSync(markerPath, "utf8"));
	if (marker.signature === expectedSignature) process.exit(0);
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });
for (const source of sources) cpSync(join(sourceDir, source), join(outDir, source));
const npm = npmInvocation(["ci", "--omit=dev", "--ignore-scripts"]);
run(npm.command, npm.args, { cwd: outDir });

for (const required of [
	agentEntry,
	join(outDir, "node_modules", "agent-device", "LICENSE"),
]) {
	if (!existsSync(required)) throw new Error(`device runtime is missing ${required}`);
}

const integrity = {};
for (const file of [agentEntry, aoRunner]) {
	integrity[file.slice(outDir.length + 1)] = createHash("sha256").update(readFileSync(file)).digest("hex");
}
writeFileSync(join(outDir, "integrity.json"), `${JSON.stringify(integrity, null, 2)}\n`);
writeFileSync(markerPath, `${JSON.stringify({
	signature: expectedSignature,
	packages: { "agent-device": "0.21.1" },
}, null, 2)}\n`);

function run(command, args, options = {}) {
	const result = spawnSync(command, args, { stdio: "inherit", windowsHide: true, ...options });
	if (result.error) throw result.error;
	if (result.status !== 0) throw new Error(`${command} exited ${result.status}`);
}
