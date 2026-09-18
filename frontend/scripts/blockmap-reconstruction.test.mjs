// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { createHash } from "node:crypto";
import { EventEmitter } from "node:events";
import { createServer, request } from "node:http";
import { closeSync, copyFileSync, fstatSync, openSync, mkdtempSync, readFileSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createRequire } from "node:module";
import { writeBlockmap } from "./blockmap.mjs";

const require = createRequire(import.meta.url);
const { MacUpdater } = require("electron-updater/out/MacUpdater.js");
const { DownloadedUpdateHelper } = require("electron-updater/out/DownloadedUpdateHelper.js");
const { ElectronHttpExecutor } = require("electron-updater/out/electronHttpExecutor.js");
const { HttpExecutor, CancellationToken } = require("builder-util-runtime");
const { zipSync } = require("cross-zip");
const temporaryDirectories = [];
const sha512 = bytes => createHash("sha512").update(bytes).digest("base64");

// Replace only Electron's network transport with Node's loopback transport.
// The dependency still owns blockmap fetch/parse, reconstruction, digest checks,
// fallback, temporary files and cache promotion. Never catch and retry here.
class LoopbackExecutor extends HttpExecutor {
	createRequest(options, callback) { return request({ ...options, agent: false }, callback); }
	download(...args) {
		// Occupy a released descriptor before full fallback starts. Late cleanup
		// from the failed differential transfer must not close this unrelated file.
		this.sentinel = openSync(this.sentinelPath, "w+");
		return ElectronHttpExecutor.prototype.download.apply(this, args);
	}
}

afterEach(() => {
	vi.restoreAllMocks();
	for (const dir of temporaryDirectories.splice(0)) rmSync(dir, { recursive: true, force: true });
});

function fixtureBytes(seed, size = 512_000) {
	const bytes = Buffer.alloc(size);
	for (let offset = 0; offset < size; offset += 64) {
		createHash("sha512").update(`${seed}:${offset}`).digest().copy(bytes, offset);
	}
	return bytes;
}

function zipBytes(payload) {
	const dir = mkdtempSync(join(tmpdir(), "ao-zip-fixture-"));
	temporaryDirectories.push(dir);
	const source = join(dir, "fixture.bin");
	const archive = join(dir, "fixture.zip");
	writeFileSync(source, payload);
	utimesSync(source, new Date("2026-01-01T00:00:00Z"), new Date("2026-01-01T00:00:00Z"));
	zipSync(source, archive);
	return readFileSync(archive);
}

async function runDownload(arch, failure = "none", disabled = false, cycle = {}) {
	const version = cycle.version ?? "2.0.0";
	const dir = cycle.dir ?? mkdtempSync(join(tmpdir(), "ao-mac-blockmap-"));
	temporaryDirectories.push(dir);
	const oldFile = join(dir, "update.zip");
	const newFile = join(dir, `AO-darwin-${arch}-${version}.zip`);
	const oldPayload = fixtureBytes(arch);
	const newPayload = Buffer.from(oldPayload);
	fixtureBytes(`${arch}:${version}:patch`, 48_000).copy(newPayload, 180_000);
	const oldBytes = cycle.dir ? readFileSync(oldFile) : zipBytes(oldPayload);
	const target = await zipBytes(newPayload);
	writeFileSync(oldFile, oldBytes);
	writeFileSync(newFile, target);
	await writeBlockmap(oldFile);
	const targetInfo = await writeBlockmap(newFile);
	if (failure === "digest-mismatch") {
		// Corrupt only cached bytes. The authoritative target digest stays valid.
		const damaged = Buffer.from(oldBytes);
		damaged.fill(0);
		writeFileSync(oldFile, damaged);
	}
	if (failure === "no-cached-zip") rmSync(oldFile);
	if (failure === "kill-switch-cached-old-map" || (cycle.cachedMap && !cycle.dir)) {
		copyFileSync(`${oldFile}.blockmap`, join(dir, "current.blockmap"));
	}
	const requests = [];
	const server = createServer((req, res) => {
		const record = { path: req.url, range: req.headers.range, bytes: 0 };
		requests.push(record);
		const send = (status, body, headers = {}) => {
			record.bytes = body.length;
			res.writeHead(status, { "Content-Length": body.length, ...headers });
			res.end(body);
		};
		if (req.url.endsWith(".blockmap")) {
			if (failure === "no-published-sidecars" || (req.url === "/old.blockmap" && failure === "missing-old-blockmap") ||
				(req.url === "/new.blockmap" && ["unavailable-sidecar", "kill-switch-cached-old-map"].includes(failure))) {
				send(404, Buffer.from("missing"));
			} else if (req.url === "/new.blockmap" && failure === "corrupt-blockmap") {
				send(200, Buffer.from("not gzip"));
			} else send(200, readFileSync(`${req.url === "/old.blockmap" ? oldFile : newFile}.blockmap`));
			return;
		}
		if (req.headers.range) {
			if (failure === "range-rejected") { send(416, Buffer.alloc(0)); return; }
			const match = /^bytes=(\d+)-(\d+)$/.exec(req.headers.range);
			if (!match) { send(416, Buffer.alloc(0)); return; }
			const start = Number(match[1]), end = Number(match[2]);
			send(206, target.subarray(start, end + 1), {
				"Accept-Ranges": "bytes", "Content-Range": `bytes ${start}-${end}/${target.length}`,
			});
		} else {
			const full = Buffer.from(target);
			if (failure === "bad-full-digest") full[1000] ^= 255;
			send(200, full);
		}
	});
	await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
	try {
		const base = `http://127.0.0.1:${server.address().port}`;
		const url = new URL(`${base}/AO-darwin-${arch}-${version}.zip`);
		const file = { url, info: { url: url.href, ...targetInfo } };
		const provider = {
			resolveFiles: () => [file],
			getBlockMapFiles: async () => [new URL(`${base}/old.blockmap`), new URL(`${base}/new.blockmap`)],
			isUseMultipleRangeRequest: false,
		};
		// Avoid native Squirrel construction. Exercise real MacUpdater methods,
		// replacing only the OS handoff after successful verified download.
		const updater = new EventEmitter();
		Object.setPrototypeOf(updater, MacUpdater.prototype);
		updater.app = { version: "1.0.0" };
		updater.downloadedUpdateHelper = new DownloadedUpdateHelper(dir);
		updater.httpExecutor = new LoopbackExecutor();
		updater.httpExecutor.sentinelPath = join(dir, "sentinel");
		updater.on("download-progress", () => undefined);
		updater.logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
		const handedOff = [];
		updater.updateDownloaded = async (_file, event) => { handedOff.push(readFileSync(event.downloadedFile)); };
		const archProbe = vi.spyOn(require("node:child_process"), "execFileSync").mockImplementation(command =>
			command === "sysctl" ? "sysctl.proc_translated: 0" : arch === "arm64" ? "ARM" : "x86_64");
		const archDescriptor = Object.getOwnPropertyDescriptor(process, "arch");
		Object.defineProperty(process, "arch", { value: arch, configurable: true });
		let error;
		try {
			await updater.doDownloadUpdate({
				updateInfoAndProvider: { info: { version, files: [file.info] }, provider },
				cancellationToken: new CancellationToken(), requestHeaders: {},
				disableDifferentialDownload: disabled,
			});
		} catch (err) { error = err; }
		finally { Object.defineProperty(process, "arch", archDescriptor); archProbe.mockRestore(); }
		let sentinelIntact = true;
		if (updater.httpExecutor.sentinel !== undefined) {
			try { fstatSync(updater.httpExecutor.sentinel); }
			catch { sentinelIntact = false; }
			try { closeSync(updater.httpExecutor.sentinel); }
			catch { sentinelIntact = false; }
		}
		return { dir, target, requests, handedOff, error, targetInfo, sentinelIntact, logs: updater.logger };
	} finally { await new Promise(resolve => server.close(resolve)); }
}

describe("MacUpdater reconstruction and full fallback", () => {
	it.each(["arm64", "x64"])("reconstructs the %s ZIP before native handoff", async arch => {
		const result = await runDownload(arch);
		expect(result.error).toBeUndefined();
		expect(result.handedOff).toHaveLength(1);
		expect(result.handedOff[0].equals(result.target)).toBe(true);
		expect(sha512(result.handedOff[0])).toBe(result.targetInfo.sha512);
		expect(result.requests.some(req => req.range)).toBe(true);
		expect(result.requests.filter(req => req.path.endsWith(".zip") && !req.range)).toHaveLength(0);
		const transferred = result.requests.reduce((sum, req) => sum + req.bytes, 0);
		expect(transferred).toBeLessThan(result.target.length);
		process.stdout.write(`${JSON.stringify({ fixture: arch, targetBytes: result.target.length, transferredBytes: transferred, sha512: result.targetInfo.sha512 })}\n`);
	});

	it.each(["missing-old-blockmap", "unavailable-sidecar", "corrupt-blockmap", "digest-mismatch", "no-cached-zip", "kill-switch-cached-old-map"])(
		"performs exactly one full download for %s", async failure => {
			const result = await runDownload("arm64", failure);
			expect(result.error, JSON.stringify({ requests: result.requests, errors: result.logs.error.mock.calls })).toBeUndefined();
			expect(result.requests.filter(req => req.path.endsWith(".zip") && !req.range)).toHaveLength(1);
			expect(result.sentinelIntact).toBe(true);
			expect(result.handedOff).toHaveLength(1);
			expect(result.handedOff[0].equals(result.target)).toBe(true);
		});

	it("rejects a bad full ZIP before native handoff", async () => {
		const result = await runDownload("arm64", "bad-full-digest", true);
		expect(result.error?.message).toMatch(/sha512|checksum/i);
		expect(result.handedOff).toHaveLength(0);
		expect(result.requests.filter(req => req.path.endsWith(".zip"))).toHaveLength(1);
	});

	it("does not request sidecars or ranges when disabled", async () => {
		const result = await runDownload("arm64", "none", true);
		expect(result.error).toBeUndefined();
		expect(result.requests).toHaveLength(1);
		expect(result.requests[0].range).toBeUndefined();
		expect(result.handedOff[0].equals(result.target)).toBe(true);
	});
	it("keeps cached ZIP/map cycles full-only despite published sidecars when disabled", async () => {
		let dir;
		for (const version of ["2.0.0", "3.0.0"]) {
			const result = await runDownload("arm64", "none", true, { dir, version, cachedMap: true });
			dir = result.dir;
			expect(result.error).toBeUndefined();
			expect(result.sentinelIntact).toBe(true);
			expect(result.requests).toHaveLength(1);
			expect(result.requests[0].path).toMatch(/\.zip$/);
			expect(result.requests[0].range).toBeUndefined();
			expect(result.handedOff).toHaveLength(1);
			expect(result.handedOff[0].equals(result.target)).toBe(true);
		}
	});

	it("keeps a legacy implicit-allow client full-only across cycles while sidecars are absent", async () => {
		let dir;
		for (const version of ["2.0.0", "3.0.0"]) {
			const result = await runDownload("arm64", "no-published-sidecars", false, { dir, version, cachedMap: true });
			dir = result.dir;
			expect(result.error).toBeUndefined();
			expect(result.sentinelIntact).toBe(true);
			expect(result.requests.some(req => req.range)).toBe(false);
			expect(result.requests.filter(req => req.path.endsWith(".zip"))).toHaveLength(1);
			expect(result.handedOff).toHaveLength(1);
			expect(result.handedOff[0].equals(result.target)).toBe(true);
		}
	});

	it("documents that sidecar presence activates an unpatched legacy client", async () => {
		// This is why suppression is mandatory. A new binary's policy cannot
		// retroactively guard an older MacUpdater with implicit differential allow.
		const result = await runDownload("arm64", "none", false, { cachedMap: true });
		expect(result.error).toBeUndefined();
		expect(result.requests.some(req => req.range)).toBe(true);
		expect(result.handedOff).toHaveLength(1);
	});

	// Stock 6.8.9 remains unsafe on 416. Current AO cannot enter that worker;
	// the separate v2 MacUpdater suite tests real range failures through its
	// declared subclass extension, with no stock differential call.
	it("keeps stock HTTP 416 unreachable for the current disabled client", async () => {
		const rollout = JSON.parse(readFileSync(new URL("./mac-differential-rollout.json", import.meta.url), "utf8"));
		expect(rollout.enabled).toBe(false);
		const result = await runDownload("arm64", "range-rejected", true);
		expect(result.requests.some(request => request.range)).toBe(false);
		expect(result.error, JSON.stringify(result.requests)).toBeUndefined();
		expect(result.sentinelIntact).toBe(true);
		expect(result.requests.filter(req => req.path.endsWith(".zip") && !req.range)).toHaveLength(1);
		expect(result.handedOff).toHaveLength(1);
		expect(result.handedOff[0].equals(result.target)).toBe(true);
	});

});
