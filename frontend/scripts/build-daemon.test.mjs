// @vitest-environment node
import { afterEach, expect, it } from "vitest";
import * as fs from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { runInNewContext } from "node:vm";
import { meetsMinimumVersion, parseGoVersion, parseMinimumGoVersion } from "./go-version.mjs";

const source = fs.readFileSync(new URL("./build-daemon.mjs", import.meta.url), "utf8");
const roots = [];
afterEach(() => {
	for (const root of roots.splice(0)) fs.rmSync(root, { recursive: true, force: true });
});
// Runs the build script in a scratch repository with a fake Go toolchain.
function runBuild(platform, helperStatus = 0) {
	const root = fs.mkdtempSync(join(tmpdir(), "ao-account-build-"));
	roots.push(root);
	const helper = join(root, "proxy-host");
	for (const dir of [join(root, "frontend", "scripts"), join(root, "backend"), helper]) fs.mkdirSync(dir, { recursive: true });
	fs.writeFileSync(join(root, "backend", "go.mod"), "module ao.test/backend\n\ngo 1.27.1\n");
	fs.writeFileSync(join(helper, "CLIProxyAPI-LICENSE"), "license\n");
	const calls = [];
	const exit = {};
	let status = 0;
	const script = source.replace(/^import .*;\n/gm, "").replaceAll("import.meta.url", JSON.stringify(pathToFileURL(join(root, "frontend", "scripts", "build-daemon.mjs")).href));
	const spawnSync = (_command, args, options = {}) => {
		if (args[0] === "version") return { status: 0, stdout: "go version go1.27.1 darwin/arm64" };
		calls.push({ args: [...args], cwd: options.cwd, gowork: options.env?.GOWORK });
		if (options.cwd === helper && helperStatus) return { status: helperStatus };
		fs.writeFileSync(args[2], options.cwd === helper ? "helper" : "daemon");
		return { status: 0 };
	};
	const exitWith = (code) => {
		status = code;
		throw exit;
	};
	try {
		runInNewContext(script, { ...fs, dirname, join, resolve, fileURLToPath, meetsMinimumVersion, parseGoVersion, parseMinimumGoVersion, spawnSync, console: { error: () => undefined }, process: { platform, pid: 1, argv: [], env: { GOWORK: join(root, "go.work") }, exit: exitWith } });
	} catch (error) {
		if (error !== exit) throw error;
	}
	return { out: join(root, "frontend", "daemon"), helper, calls, status };
}

it.each([["darwin", ""], ["win32", ".exe"]])("puts the account helper and its licence beside the daemon on %s", (platform, suffix) => {
	const { out, helper, calls, status } = runBuild(platform);
	expect(status).toBe(0);
	expect(fs.readFileSync(join(out, `ao${suffix}`), "utf8")).toBe("daemon");
	expect(fs.readFileSync(join(out, `ao-proxy-host${suffix}`), "utf8")).toBe("helper");
	expect(fs.readFileSync(join(out, "CLIProxyAPI-LICENSE"), "utf8")).toBe("license\n");
	// The helper is its own module with a pinned SDK, so it builds there with the workspace off.
	expect(calls).toEqual([expect.objectContaining({ gowork: undefined }), { args: ["build", "-o", join(out, `ao-proxy-host${suffix}`), "./cmd/ao-proxy-host"], cwd: helper, gowork: "off" }]);
});
it("fails the build with the helper's exit status when the helper does not build", () => {
	const { out, status } = runBuild("darwin", 7);
	expect(status).toBe(7);
	expect(fs.existsSync(join(out, "CLIProxyAPI-LICENSE"))).toBe(false);
});
