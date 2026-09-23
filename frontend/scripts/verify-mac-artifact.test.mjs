import { describe, expect, it, beforeAll, afterAll } from "vitest";
import { execFile } from "node:child_process";
import { chmodSync, existsSync, mkdtempSync, rmSync, writeFileSync, statSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// Smoke coverage for scripts/verify-mac-artifact.sh.
//
// Honest scope: the three real checks (codesign, spctl, stapler) cannot be
// exercised here. Mocking them convincingly would require real Apple signing
// material, and a mock that always passes proves nothing about the gate. The
// meaningful verification of the core logic happens against real published
// artifacts: the public mac-update-e2e workflow runs this script against the
// published baseline and the installed app, developers run it as the local
// diagnostic, and the release conductor's pre-publication gate applies the
// same nested-Node rules (frontend/docs/desktop-release.md).
//
// What IS covered here, on any platform with no signing material at all:
// the script parses, every usage/precondition path exits 2 with a clear
// message, and mocked trust failures prove nested code is never executed before
// verification succeeds. Real Apple trust decisions remain release evidence.

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "verify-mac-artifact.sh");

function run(args, env = {}) {
	return new Promise((resolve) => {
		execFile("bash", [SCRIPT, ...args], { env: { ...process.env, ...env } }, (err, stdout, stderr) => {
			resolve({ code: err?.code ?? 0, stdout, stderr });
		});
	});
}

function writeExecutable(path, contents) {
	writeFileSync(path, `#!/usr/bin/env bash\n${contents}`);
	chmodSync(path, 0o755);
}

let dir;
beforeAll(() => {
	dir = mkdtempSync(join(tmpdir(), "verify-mac-artifact-"));
});
afterAll(() => {
	rmSync(dir, { recursive: true, force: true });
});

describe("verify-mac-artifact.sh", () => {
	it("is committed executable so CI and humans invoke it the same way", () => {
		expect(statSync(SCRIPT).mode & 0o111).not.toBe(0);
	});

	it("parses under bash", async () => {
		const { code } = await new Promise((resolve) => {
			execFile("bash", ["-n", SCRIPT], (err) => resolve({ code: err?.code ?? 0 }));
		});
		expect(code).toBe(0);
	});

	it("exits 2 with usage when given no arguments", async () => {
		const { code, stderr } = await run([]);
		expect(code).toBe(2);
		expect(stderr).toContain("usage: verify-mac-artifact.sh");
	});

	it("exits 2 when given more than one argument", async () => {
		const { code } = await run(["a.zip", "b.zip"]);
		expect(code).toBe(2);
	});

	it("exits 2 for a nonexistent file", async () => {
		const missing = join(dir, "not-here.zip");
		const { code, stderr } = await run([missing]);
		expect(code).toBe(2);
		expect(stderr).toContain("no such file");
	});

	it("exits 2 for a non-zip, non-app input", async () => {
		const txt = join(dir, "notes.txt");
		writeFileSync(txt, "not an artifact\n");
		const { code, stderr } = await run([txt]);
		expect(code).toBe(2);
		expect(stderr).toContain("unsupported artifact type");
	});

	it("exits 2 when a .zip path is actually a directory", async () => {
		const fake = join(dir, "bundle.zip");
		mkdirSync(fake, { recursive: true });
		const { code, stderr } = await run([fake]);
		expect(code).toBe(2);
		expect(stderr).toContain("expected a .zip file");
	});

	it("exits 2 when a .app path is not a directory", async () => {
		const fake = join(dir, "Fake.app");
		writeFileSync(fake, "");
		const { code, stderr } = await run([fake]);
		expect(code).toBe(2);
		expect(stderr).toContain("expected an .app bundle directory");
	});

	// A .dmg is a first-class input now: forge.config.ts's postMake seals the dmg
	// and then gates on this script (#3267 decision 3 step 4). Same usage
	// contract, so the same paths have to exit 2 rather than fall through to
	// macOS-only tooling.
	it("accepts .dmg as a known artifact type", async () => {
		const { readFileSync } = await import("node:fs");
		const src = readFileSync(SCRIPT, "utf8");
		expect(src).toContain("*.dmg)");
		const { stderr } = await run([]);
		expect(stderr).toContain(".dmg");
	});

	it("exits 2 when a .dmg path is actually a directory", async () => {
		const fake = join(dir, "bundle.dmg");
		mkdirSync(fake, { recursive: true });
		const { code, stderr } = await run([fake]);
		expect(code).toBe(2);
		expect(stderr).toContain("expected a .dmg file");
	});

	it("exits 2 for a nonexistent .dmg", async () => {
		const { code, stderr } = await run([join(dir, "not-here.dmg")]);
		expect(code).toBe(2);
		expect(stderr).toContain("no such file");
	});

	it("rejects a directory masquerading as a package", async () => {
		const fake = join(dir, "bundle.pkg");
		mkdirSync(fake);
		const { code, stderr } = await run([fake]);
		expect(code).toBe(2);
		expect(stderr).toContain("expected a .pkg file");
	});

	it.each(["signature", "gatekeeper", "staple", "none"])("checks package trust without installing its payload (%s failure)", async (failure) => {
		const mockBin = join(dir, `pkg-tools-${failure}`);
		mkdirSync(mockBin);
		const artifact = join(dir, `repair-${failure}.pkg`);
		const calls = join(dir, `pkg-calls-${failure}`);
		writeFileSync(artifact, "fixture; no actual signed package");
		writeExecutable(join(mockBin, "uname"), "echo Darwin\n");
		for (const tool of ["codesign", "ditto", "lipo", "plutil", "installer"]) {
			writeExecutable(join(mockBin, tool), `echo unexpected-${tool} >> "$PKG_CALLS"; exit 1\n`);
		}
		for (const [tool, stage] of [["pkgutil", "signature"], ["spctl", "gatekeeper"], ["xcrun", "staple"]]) {
			writeExecutable(join(mockBin, tool), `echo "${tool} $*" >> "$PKG_CALLS"\nexit ${failure === stage ? 1 : 0}\n`);
		}
		const { code } = await run([artifact], { PATH: `${mockBin}:${process.env.PATH}`, PKG_CALLS: calls });
		expect(code).toBe(failure === "none" ? 0 : 1);
		const { readFileSync } = await import("node:fs");
		const commands = readFileSync(calls, "utf8");
		expect(commands).toContain("pkgutil --check-signature");
		expect(commands).toContain("spctl -a -vv -t install");
		expect(commands).toContain("xcrun stapler validate");
		expect(commands).not.toContain("unexpected-");
	});

	it("never executes nested code when an artifact fails a trust check", async () => {
		const mockBin = join(dir, "mock-bin");
		const app = join(dir, "Untrusted.app");
		const node = join(app, "Contents", "Resources", "acp-runtime", "node", "bin", "node");
		const sentinel = join(dir, "nested-node-executed");
		mkdirSync(mockBin, { recursive: true });
		mkdirSync(dirname(node), { recursive: true });

		writeExecutable(join(mockBin, "uname"), "echo Darwin\n");
		writeExecutable(join(mockBin, "codesign"), 'if [[ "$1" == "-d" ]]; then echo "<plist><dict></dict></plist>"; fi\n');
		writeExecutable(join(mockBin, "spctl"), "exit 1\n");
		writeExecutable(join(mockBin, "xcrun"), "exit 0\n");
		writeExecutable(join(mockBin, "ditto"), "exit 0\n");
		writeExecutable(join(mockBin, "lipo"), "echo x86_64\n");
		writeExecutable(
			join(mockBin, "plutil"),
			"echo '  \"com.apple.security.cs.allow-jit\" => true'\n" +
				"echo '  \"com.apple.security.cs.allow-unsigned-executable-memory\" => true'\n",
		);
		writeExecutable(node, 'echo executed > "$ACP_NODE_SENTINEL"\n');

		const { code, stderr } = await run([app], {
			PATH: `${mockBin}:${process.env.PATH}`,
			ACP_NODE_SENTINEL: sentinel,
		});

		expect(code).toBe(1);
		expect(stderr).toContain("spctl failed");
		expect(existsSync(sentinel)).toBe(false);
	});

	it("encodes the verified command set (ditto, spctl -vv, stapler validate)", async () => {
		const { readFileSync } = await import("node:fs");
		const src = readFileSync(SCRIPT, "utf8");
		// These exact forms are load-bearing and were each verified empirically;
		// see the header comment. A drive-by "simplification" of any of them
		// silently turns the gate into a no-op, so pin them here.
		expect(src).toContain("ditto -x -k");
		expect(src).not.toMatch(/^\s*unzip /m);
		expect(src).toContain("codesign --verify --deep --strict");
		expect(src).toContain("xcrun stapler validate");
		// The .zip/.app path must keep assessing executable code...
		expect(src).toContain("spctl -a -vv -t exec");
		// ...and the dmg container must be assessed on open against the primary
		// signature (#3267 decision 3 step 4), never with -t exec.
		expect(src).toContain("spctl -a -vv -t open --context context:primary-signature");
		// A valid outer seal can still contain the Intel Node regression from
		// #3879, so the canonical gate also checks the final nested executable.
		expect(src).toContain("Contents/Resources/acp-runtime/node/bin/node");
		expect(src).toContain("com.apple.security.cs.allow-jit");
		expect(src).toContain("com.apple.security.cs.allow-unsigned-executable-memory");
		expect(src).toContain('console.log("ACP runtime JavaScript OK")');
		expect(src).toContain('if [[ $failed -eq 0 && "$ARTIFACT" != *.dmg ]]');
		expect(src.indexOf('run_check "ACP Node JavaScript execution"')).toBeGreaterThan(
			src.indexOf('run_check "stapler"'),
		);
	});
});
