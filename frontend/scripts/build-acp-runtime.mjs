import { createHash } from "node:crypto";
import {
	cpSync,
	existsSync,
	mkdirSync,
	readFileSync,
	renameSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { createWorkDirectory, npmInvocation, pruneNodeDistribution } from "./build-acp-runtime-helpers.mjs";

const NODE_VERSION = "22.23.2";
const scriptsDir = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(scriptsDir, "..");
const sourceDir = join(frontendRoot, "acp-runtime");
const outDir = join(frontendRoot, "resources", "acp-runtime");

const platform = { darwin: "darwin", linux: "linux", win32: "win" }[process.platform];
const arch = { x64: "x64", arm64: "arm64" }[process.arch];
if (!platform || !arch) {
	throw new Error(`ACP runtime packaging is unsupported on ${process.platform}/${process.arch}`);
}

// Release builds run natively on their target OS. This keeps AO's packaged Node
// runtime aligned with the adapter's >=22 requirement without making the user
// install Node globally.
const extension = process.platform === "win32" ? "zip" : "tar.gz";
const archiveName = `node-v${NODE_VERSION}-${platform}-${arch}.${extension}`;
const baseURL = `https://nodejs.org/dist/v${NODE_VERSION}`;
const buildSignature = createHash("sha256")
	.update(readFileSync(join(sourceDir, "package-lock.json")))
	.update(readFileSync(fileURLToPath(import.meta.url)))
	.update(readFileSync(join(scriptsDir, "build-acp-runtime-helpers.mjs")))
	.update(`node=${NODE_VERSION};platform=${platform};arch=${arch}`)
	.digest("hex");
const markerPath = join(outDir, ".ao-acp-runtime.json");
const expectedNode = process.platform === "win32"
	? join(outDir, "node", "node.exe")
	: join(outDir, "node", "bin", "node");
const expectedAdapter = join(
	outDir,
	"node_modules",
	"@agentclientprotocol",
	"claude-agent-acp",
	"dist",
	"index.js",
);
if (existsSync(markerPath) && existsSync(expectedNode) && existsSync(expectedAdapter)) {
	const marker = JSON.parse(readFileSync(markerPath, "utf8"));
	if (marker.signature === buildSignature) process.exit(0);
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });
cpSync(join(sourceDir, "package.json"), join(outDir, "package.json"));
cpSync(join(sourceDir, "package-lock.json"), join(outDir, "package-lock.json"));

const npm = npmInvocation(["ci", "--omit=dev", "--omit=optional", "--ignore-scripts"]);
run(npm.command, npm.args, { cwd: outDir });

// The Claude Agent SDK declares platform-native Claude executables as optional
// dependencies. --omit=optional excludes them; this removal is defense-in-depth.
// At runtime AO sets
// CLAUDE_CODE_EXECUTABLE to the user's already-installed Claude Code binary.
const anthropicDir = join(outDir, "node_modules", "@anthropic-ai");
const nativeClaudePackages = [
	"claude-agent-sdk-darwin-arm64",
	"claude-agent-sdk-darwin-x64",
	"claude-agent-sdk-linux-arm64",
	"claude-agent-sdk-linux-arm64-musl",
	"claude-agent-sdk-linux-x64",
	"claude-agent-sdk-linux-x64-musl",
	"claude-agent-sdk-win32-arm64",
	"claude-agent-sdk-win32-x64",
];
for (const name of nativeClaudePackages) {
	rmSync(join(anthropicDir, name), { recursive: true, force: true });
}

const workDir = createWorkDirectory(outDir);
try {
	const [sums, archive] = await Promise.all([
		download(`${baseURL}/SHASUMS256.txt`),
		download(`${baseURL}/${archiveName}`),
	]);
	const expectedLine = sums.toString("utf8").split(/\r?\n/).find((line) => line.endsWith(`  ${archiveName}`));
	if (!expectedLine) throw new Error(`${archiveName} is absent from Node's SHASUMS256.txt`);
	const expected = expectedLine.split(/\s+/)[0];
	const actual = createHash("sha256").update(archive).digest("hex");
	if (actual !== expected) throw new Error(`Node archive checksum mismatch: expected ${expected}, got ${actual}`);

	const archivePath = join(workDir, archiveName);
	writeFileSync(archivePath, archive);
	if (process.platform === "win32") {
		const escapedArchive = archivePath.replaceAll("'", "''");
		const escapedDestination = workDir.replaceAll("'", "''");
		run("powershell.exe", [
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			`Expand-Archive -LiteralPath '${escapedArchive}' -DestinationPath '${escapedDestination}' -Force`,
		]);
	} else {
		run("tar", ["-xzf", archivePath, "-C", workDir]);
	}
	const extracted = join(workDir, basename(archiveName, `.${extension}`));
	const nodeOut = join(outDir, "node");
	if (!existsSync(extracted)) throw new Error(`Node archive did not contain ${extracted}`);
	renameSync(extracted, nodeOut);
	// Only the runtime executable is needed; npm, headers, docs, and corepack are
	// build/development tools and would add roughly 80 MB to every desktop update.
	pruneNodeDistribution(nodeOut);
} finally {
	rmSync(workDir, { recursive: true, force: true });
}

// A build-time invariant, not a comment: packaging fails if a Claude native
// executable was accidentally left in the shipped runtime.
for (const name of nativeClaudePackages) {
	if (existsSync(join(anthropicDir, name))) {
		throw new Error(`ACP runtime contains packaged Claude Code dependency ${name}`);
	}
}
writeFileSync(markerPath, `${JSON.stringify({ signature: buildSignature, node: NODE_VERSION }, null, 2)}\n`);

function run(command, args, options = {}) {
	const result = spawnSync(command, args, { stdio: "inherit", windowsHide: true, ...options });
	if (result.error) throw result.error;
	if (result.status !== 0) throw new Error(`${command} exited ${result.status}`);
}

async function download(url) {
	const response = await fetch(url);
	if (!response.ok) throw new Error(`download ${url}: HTTP ${response.status}`);
	return Buffer.from(await response.arrayBuffer());
}
