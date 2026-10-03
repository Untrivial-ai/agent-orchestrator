// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { createHash } from "node:crypto";
import { EventEmitter } from "node:events";
import { createServer, request } from "node:http";
import { closeSync, fstatSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { AppImageUpdater } = require("electron-updater/out/AppImageUpdater.js");
const { DownloadedUpdateHelper } = require("electron-updater/out/DownloadedUpdateHelper.js");
const { ElectronHttpExecutor } = require("electron-updater/out/electronHttpExecutor.js");
const { HttpExecutor, CancellationToken } = require("builder-util-runtime");
// Same fragile pin as scripts/blockmap.mjs. In append mode (no destination) it
// writes `deflate(blockmap v2) + 4-byte BE size` onto the file and returns the
// full-file size/sha512 plus blockMapSize, exactly like the AppImage maker.
const { buildBlockMap } = require("app-builder-lib/out/targets/blockmap/blockmap.js");

const temporaryDirectories = [];
const sha512 = bytes => createHash("sha512").update(bytes).digest("base64");

// Replace only Electron's network transport with Node's loopback transport.
// The dependency still owns blockmap parse, reconstruction, digest checks,
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

// Simulates the release maker: append `deflate(blockmap v2) + 4-byte BE size`
// and return the yml entry (full-file size/sha512 + blockMapSize) that the
// release pipeline publishes for the artifact.
async function appendEmbeddedBlockMap(filePath) {
	return buildBlockMap(filePath, "deflate");
}

function roundTripInfo(url, info) {
	return { url, info: { url: url.href, ...info } };
}

async function runDownload(failure = "none", disabled = false, legacyFeed = false) {
	const dir = mkdtempSync(join(tmpdir(), "ao-appimage-blockmap-"));
	temporaryDirectories.push(dir);
	const oldFile = join(dir, "Agent.Orchestrator-1.0.0.AppImage");
	const newFile = join(dir, "Agent.Orchestrator-2.0.0.AppImage");
	const oldPayload = fixtureBytes("linux");
	const newPayload = Buffer.from(oldPayload);
	fixtureBytes("linux:2.0.0:patch", 48_000).copy(newPayload, 180_000);
	writeFileSync(oldFile, oldPayload);
	await appendEmbeddedBlockMap(oldFile);
	writeFileSync(newFile, newPayload);
	const targetInfo = await appendEmbeddedBlockMap(newFile);
	const target = readFileSync(newFile);
	if (failure === "missing-old-file") rmSync(oldFile);
	const requests = [];
	const server = createServer((req, res) => {
		const record = { path: req.url, range: req.headers.range, bytes: 0 };
		requests.push(record);
		const send = (status, body, headers = {}) => {
			record.bytes = body.length;
			res.writeHead(status, { "Content-Length": body.length, ...headers });
			res.end(body);
		};
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
	const appImageEnv = process.env.APPIMAGE;
	process.env.APPIMAGE = oldFile;
	try {
		const base = `http://127.0.0.1:${server.address().port}`;
		const url = new URL(`${base}/Agent.Orchestrator-2.0.0.AppImage`);
		// A legacy feed omits blockMapSize; the embedded size then reads as NaN
		// and the attempt falls back to the full download (#5576 pre-fix shape).
		const info = legacyFeed ? Object.fromEntries(Object.entries(targetInfo).filter(([k]) => k !== "blockMapSize")) : targetInfo;
		const file = roundTripInfo(url, info);
		const provider = { resolveFiles: () => [file], isUseMultipleRangeRequest: false };
		const updater = new EventEmitter();
		Object.setPrototypeOf(updater, AppImageUpdater.prototype);
		updater.app = { version: "1.0.0" };
		updater.downloadedUpdateHelper = new DownloadedUpdateHelper(dir);
		updater.httpExecutor = new LoopbackExecutor();
		updater.httpExecutor.sentinelPath = join(dir, "sentinel");
		updater.on("download-progress", () => undefined);
		updater.logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
		const handedOff = [];
		updater.on("update-downloaded", event => { handedOff.push(readFileSync(event.downloadedFile)); });
		let error;
		try {
			await updater.doDownloadUpdate({
				updateInfoAndProvider: { info: { version: "2.0.0", files: [file.info] }, provider },
				cancellationToken: new CancellationToken(), requestHeaders: {},
				disableDifferentialDownload: disabled,
			});
		} catch (err) { error = err; }
		let sentinelIntact = true;
		if (updater.httpExecutor.sentinel !== undefined) {
			try { fstatSync(updater.httpExecutor.sentinel); }
			catch { sentinelIntact = false; }
			try { closeSync(updater.httpExecutor.sentinel); }
			catch { sentinelIntact = false; }
		}
		return { dir, target, requests, handedOff, error, targetInfo, sentinelIntact, logs: updater.logger };
	} finally {
		if (appImageEnv === undefined) delete process.env.APPIMAGE;
		else process.env.APPIMAGE = appImageEnv;
		await new Promise(resolve => server.close(resolve));
	}
}

describe("AppImageUpdater embedded-differential reconstruction", () => {
	it("reconstructs the AppImage from the embedded blockmap before handoff", async () => {
		const result = await runDownload();
		expect(result.error, JSON.stringify(result.requests)).toBeUndefined();
		expect(result.handedOff).toHaveLength(1);
		expect(result.handedOff[0].equals(result.target)).toBe(true);
		expect(sha512(result.handedOff[0])).toBe(result.targetInfo.sha512);
		expect(result.requests.some(req => req.range)).toBe(true);
		expect(result.requests.filter(req => !req.range)).toHaveLength(0);
		const transferred = result.requests.reduce((sum, req) => sum + req.bytes, 0);
		expect(transferred).toBeLessThan(result.target.length);
		process.stdout.write(`${JSON.stringify({ fixture: "appimage", targetBytes: result.target.length, transferredBytes: transferred, blockMapSize: result.targetInfo.blockMapSize, sha512: result.targetInfo.sha512 })}\n`);
	});

	it("skips differential and performs one full download when disabled", async () => {
		const result = await runDownload("none", true);
		expect(result.error).toBeUndefined();
		expect(result.requests).toHaveLength(1);
		expect(result.requests[0].range).toBeUndefined();
		expect(result.handedOff[0].equals(result.target)).toBe(true);
	});

	it("falls back to exactly one full download when the old AppImage tail is missing", async () => {
		const result = await runDownload("missing-old-file");
		expect(result.error, JSON.stringify({ requests: result.requests, errors: result.logs.error.mock.calls })).toBeUndefined();
		// The new file's embedded tail is range-read before the old file is opened.
		expect(result.requests.some(req => req.range)).toBe(true);
		expect(result.requests.filter(req => !req.range)).toHaveLength(1);
		expect(result.sentinelIntact).toBe(true);
		expect(result.handedOff[0].equals(result.target)).toBe(true);
		expect(result.logs.error.mock.calls.some(args => String(args[0]).includes("Cannot download differentially"))).toBe(true);
	});

	it("falls back to exactly one full download from a legacy feed without blockMapSize", async () => {
		const result = await runDownload("none", false, true);
		expect(result.error, JSON.stringify({ requests: result.requests, errors: result.logs.error.mock.calls })).toBeUndefined();
		// The NaN offset throws before any request is emitted, so no range
		// attempt reaches the server; the full download wins.
		expect(result.requests.filter(req => !req.range)).toHaveLength(1);
		expect(result.requests.some(req => req.range)).toBe(false);
		expect(result.sentinelIntact).toBe(true);
		expect(result.handedOff).toHaveLength(1);
		expect(result.handedOff[0].equals(result.target)).toBe(true);
	});

	it("falls back to exactly one full download when the server rejects ranges", async () => {
		const result = await runDownload("range-rejected");
		expect(result.error, JSON.stringify({ requests: result.requests, errors: result.logs.error.mock.calls })).toBeUndefined();
		expect(result.requests.filter(req => !req.range)).toHaveLength(1);
		expect(result.sentinelIntact).toBe(true);
		expect(result.handedOff).toHaveLength(1);
		expect(result.handedOff[0].equals(result.target)).toBe(true);
	});

	it("rejects a corrupt full download before handoff", async () => {
		const result = await runDownload("bad-full-digest", true);
		expect(result.error?.message).toMatch(/sha512|checksum/i);
		expect(result.handedOff).toHaveLength(0);
		expect(result.requests.filter(req => !req.range)).toHaveLength(1);
	});
});