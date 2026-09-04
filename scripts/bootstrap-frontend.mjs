import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const REQUIRED_NODE_VERSION = "24.20.0";
export const REQUIRED_NPM_VERSION = "11.19.0";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const packageRoots = [
	repositoryRoot,
	join(repositoryRoot, "packages", "product-ui"),
	join(repositoryRoot, "frontend"),
];
const lockfilePaths = packageRoots.map((root) => join(root, "package-lock.json"));
const statePath = join(repositoryRoot, ".cache", "frontend-bootstrap.json");

export function assertSupportedRuntime(nodeVersion, npmVersion) {
	const normalizedNode = nodeVersion.replace(/^v/, "");
	if (normalizedNode === REQUIRED_NODE_VERSION && npmVersion === REQUIRED_NPM_VERSION) return;
	throw new Error(
		[
			`Frontend bootstrap requires Node ${REQUIRED_NODE_VERSION} and npm ${REQUIRED_NPM_VERSION}.`,
			`Found Node ${normalizedNode} and npm ${npmVersion}.`,
			"Run your version manager in the repository root (for example, `nvm use`) and retry; newer or older runtimes can omit required native packages.",
		].join(" "),
	);
}

export function createBootstrapKey({ platform, arch, nodeVersion, npmVersion, lockfiles }) {
	const hash = createHash("sha256");
	hash.update(JSON.stringify({ platform, arch, nodeVersion, npmVersion }));
	for (const lockfile of [...lockfiles].sort((left, right) => left.path.localeCompare(right.path))) {
		hash.update("\0");
		hash.update(lockfile.path);
		hash.update("\0");
		hash.update(lockfile.content);
	}
	return hash.digest("hex");
}

export function canReuseBootstrap(savedKey, currentKey, validationPassed) {
	return savedKey === currentKey && validationPassed;
}

export function commandForPlatform(command, platform) {
	return platform === "win32" && command === "npm" ? "npm.cmd" : command;
}

function run(command, args, options = {}) {
	let executable = commandForPlatform(command, process.platform);
	let executableArgs = args;
	if (command === "npm" && process.env.npm_execpath) {
		executable = process.execPath;
		executableArgs = [process.env.npm_execpath, ...args];
	} else if (command === "npm" && process.platform === "win32") {
		if (args.some((argument) => /[&|<>^%!\r\n]/.test(argument))) {
			throw new Error("Refusing to pass an unsafe argument to the Windows npm shim");
		}
		executable = process.env.ComSpec || "cmd.exe";
		executableArgs = ["/d", "/s", "/c", ["npm.cmd", ...args].join(" ")];
	}
	return spawnSync(executable, executableArgs, {
		cwd: repositoryRoot,
		encoding: "utf8",
		stdio: options.quiet ? "pipe" : "inherit",
		...options,
	});
}

function npmVersion() {
	const result = run("npm", ["--version"], { quiet: true });
	if (result.status !== 0) throw new Error(`Unable to execute npm: ${result.stderr || result.error}`);
	return result.stdout.trim();
}

function currentKey(npm) {
	return createBootstrapKey({
		platform: process.platform,
		arch: process.arch,
		nodeVersion: process.version,
		npmVersion: npm,
		lockfiles: lockfilePaths.map((path) => ({
			path: path.slice(repositoryRoot.length + 1).replaceAll("\\", "/"),
			content: readFileSync(path, "utf8"),
		})),
	});
}

function savedKey() {
	if (!existsSync(statePath)) return undefined;
	try {
		return JSON.parse(readFileSync(statePath, "utf8")).key;
	} catch {
		return undefined;
	}
}

function validateDependencies({ report = false } = {}) {
	const checks = [
		{
			label: "desktop native/tooling modules",
			cwd: join(repositoryRoot, "frontend"),
			script: [
				'const fs = require("node:fs")',
				'const Database = require("better-sqlite3")',
				'const db = new Database(":memory:"); db.close()',
				'const electronPath = require("electron")',
				'if (!fs.existsSync(electronPath)) throw new Error("Electron binary is missing")',
				'require.resolve("@electron-forge/plugin-auto-unpack-natives")',
				'Promise.all([import("vite"), import("vitest/node")]).catch((error) => { console.error(error); process.exit(1) })',
			].join(";"),
		},
		{
			label: "product UI peer dependencies",
			cwd: join(repositoryRoot, "packages", "product-ui"),
			script: [
				'require.resolve("react")',
				'require.resolve("motion/react")',
				'require.resolve("clsx")',
			].join(";"),
		},
	];
	for (const { label, cwd, script } of checks) {
		const result = run("node", ["-e", script], { cwd, quiet: true });
		if (result.status === 0) continue;
		if (report) console.error(`${label} validation failed:\n${result.stderr || result.error}`);
		return false;
	}
	return true;
}

function installAll() {
	for (const root of packageRoots) {
		const relative =
			root === repositoryRoot ? "repository root" : root.slice(repositoryRoot.length + 1);
		console.log(`Installing dependencies in ${relative}...`);
		let installed = false;
		for (let attempt = 1; attempt <= 3; attempt += 1) {
			if (run("npm", ["ci", "--include=optional"], { cwd: root }).status === 0) {
				installed = true;
				break;
			}
			if (attempt < 3) console.warn(`npm ci failed in ${relative}; retrying (${attempt}/3).`);
		}
		if (!installed) throw new Error(`npm ci failed in ${relative} after three attempts`);
	}
}

function repairOptionalDependencies() {
	console.warn("Native dependency validation failed; retrying optional dependency materialization once.");
	for (const root of packageRoots.slice(1)) {
		const result = run("npm", ["ci", "--include=optional"], { cwd: root });
		if (result.status !== 0) throw new Error(`Optional dependency repair failed in ${root}`);
	}
}

function writeState(key) {
	mkdirSync(dirname(statePath), { recursive: true });
	writeFileSync(
		statePath,
		`${JSON.stringify({ key, node: process.version, npm: REQUIRED_NPM_VERSION }, null, 2)}\n`,
	);
}

async function main() {
	const npm = npmVersion();
	assertSupportedRuntime(process.version, npm);
	const key = currentKey(npm);
	const previousKey = savedKey();
	const validationPassed = previousKey === key && validateDependencies();
	if (canReuseBootstrap(previousKey, key, validationPassed)) {
		console.log("Frontend dependencies already match this runtime and lockfile set.");
		return;
	}

	installAll();
	if (!validateDependencies()) repairOptionalDependencies();
	if (!validateDependencies()) {
		validateDependencies({ report: true });
		throw new Error(
			"Frontend native dependencies are still incomplete after one repair. Remove this worktree's node_modules directories and rerun npm run bootstrap:frontend.",
		);
	}
	writeState(key);
	console.log("Frontend bootstrap complete.");
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
	main().catch((error) => {
		console.error(error instanceof Error ? error.message : error);
		process.exitCode = 1;
	});
}
