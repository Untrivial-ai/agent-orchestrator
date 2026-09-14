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
const hubEntry = join(outDir, "node_modules", "expo-device-hub", "dist", "server", "cli.mjs");
const scrcpyServer = join(outDir, "node_modules", "expo-device-hub", "vendor", "serve-emu", "vendor", "scrcpy-server-v4.0");
const scrcpyServerURL = "https://github.com/Genymobile/scrcpy/releases/download/v4.0/scrcpy-server-v4.0";
const scrcpyServerSHA256 = "84924bd564a1eb6089c872c7521f968058977f91f5ff02514a8c74aff3210f3a";
const aoRunner = join(outDir, "ao-device-runner.mjs");
if (existsSync(markerPath) && existsSync(agentEntry) && existsSync(hubEntry) && existsSync(scrcpyServer) && existsSync(aoRunner)) {
	const marker = JSON.parse(readFileSync(markerPath, "utf8"));
	if (marker.signature === expectedSignature) process.exit(0);
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });
for (const source of sources) cpSync(join(sourceDir, source), join(outDir, source));
const npm = npmInvocation(["ci", "--omit=dev", "--ignore-scripts"]);
run(npm.command, npm.args, { cwd: outDir });
await downloadVerified(scrcpyServerURL, scrcpyServerSHA256, scrcpyServer);

for (const required of [
	agentEntry,
	hubEntry,
	scrcpyServer,
	join(outDir, "node_modules", "agent-device", "LICENSE"),
	join(outDir, "node_modules", "expo-device-hub", "LICENSE"),
	join(outDir, "node_modules", "expo-device-hub", "vendor", "serve-sim", "LICENSE"),
	join(outDir, "node_modules", "expo-device-hub", "vendor", "serve-emu", "LICENSE"),
]) {
	if (!existsSync(required)) throw new Error(`device runtime is missing ${required}`);
}

const integrity = {};
for (const file of [agentEntry, hubEntry, scrcpyServer, aoRunner]) {
	integrity[file.slice(outDir.length + 1)] = createHash("sha256").update(readFileSync(file)).digest("hex");
}
writeFileSync(join(outDir, "integrity.json"), `${JSON.stringify(integrity, null, 2)}\n`);
writeFileSync(markerPath, `${JSON.stringify({
	signature: expectedSignature,
	packages: { "agent-device": "0.21.1", "expo-device-hub": "0.9.0" },
}, null, 2)}\n`);

function run(command, args, options = {}) {
	const result = spawnSync(command, args, { stdio: "inherit", windowsHide: true, ...options });
	if (result.error) throw result.error;
	if (result.status !== 0) throw new Error(`${command} exited ${result.status}`);
}

async function downloadVerified(url, expectedSHA256, destination) {
	const response = await fetch(url);
	if (!response.ok) throw new Error(`download ${url}: HTTP ${response.status}`);
	const contents = Buffer.from(await response.arrayBuffer());
	const actualSHA256 = createHash("sha256").update(contents).digest("hex");
	if (actualSHA256 !== expectedSHA256) {
		throw new Error(`checksum mismatch for ${url}: got ${actualSHA256}`);
	}
	mkdirSync(dirname(destination), { recursive: true });
	writeFileSync(destination, contents, { mode: 0o644 });
}
