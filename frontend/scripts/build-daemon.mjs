import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { meetsMinimumVersion, parseGoVersion, parseMinimumGoVersion } from "./go-version.mjs";

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(scriptsDir, "..");
const repoRoot = resolve(frontendRoot, "..");
const backendRoot = join(repoRoot, "backend");
const outDir = join(frontendRoot, "daemon");
const outPath = join(outDir, process.platform === "win32" ? "ao.exe" : "ao");
const minimumGoVersion = parseMinimumGoVersion(readFileSync(join(backendRoot, "go.mod"), "utf8"));

if (!minimumGoVersion) {
	console.error("Could not determine the required Go version from backend/go.mod.");
	process.exit(1);
}

const versionResult = spawnSync("go", ["version"], { encoding: "utf8" });
if (versionResult.error) {
	console.error(
		`Go ${minimumGoVersion.join(".")}+ is required, but Go could not be started: ${versionResult.error.message}`,
	);
	process.exit(1);
}
const actualGoVersion = parseGoVersion(versionResult.stdout);
if (versionResult.status !== 0 || !actualGoVersion || !meetsMinimumVersion(actualGoVersion, minimumGoVersion)) {
	const found = actualGoVersion ? actualGoVersion.join(".") : versionResult.stdout.trim() || "unknown";
	console.error(`Go ${minimumGoVersion.join(".")}+ required, found ${found} — upgrade at https://go.dev/dl/`);
	process.exit(1);
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const result = spawnSync("go", ["build", "-o", outPath, "./cmd/ao"], {
	cwd: backendRoot,
	stdio: "inherit",
});

if (result.error) {
	console.error(`failed to start go build: ${result.error.message}`);
	process.exit(1);
}

if (result.status !== 0) {
	process.exit(result.status ?? 1);
}

if (process.platform === "darwin") {
	// Keep the helper independent from Electron. It is an LSBackgroundOnly app
	// supervised by the daemon and can later grow the IndigoHID transport
	// without changing the renderer process.
	const helperApp = join(outDir, "ao-sim-helper.app");
	const helperPath = join(helperApp, "Contents", "MacOS", "ao-sim-helper");
	mkdirSync(dirname(helperPath), { recursive: true });
	const helper = spawnSync("swiftc", ["-parse-as-library", join(backendRoot, "internal/iossimulator/capturehelper/main.swift"), "-o", helperPath], { stdio: "inherit" });
	if (helper.error) { console.error(`failed to start ScreenCaptureKit helper build: ${helper.error.message}`); process.exit(1); }
	if (helper.status !== 0) { console.error("ScreenCaptureKit helper build failed"); process.exit(helper.status ?? 1); }
	const plist = `<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict><key>CFBundleExecutable</key><string>ao-sim-helper</string><key>CFBundleIdentifier</key><string>com.aoagents.sim-helper</string><key>CFBundleName</key><string>AO Simulator Helper</string><key>CFBundlePackageType</key><string>APPL</string><key>LSBackgroundOnly</key><true/></dict></plist>\n`;
	writeFileSync(join(helperApp, "Contents", "Info.plist"), plist);
}
