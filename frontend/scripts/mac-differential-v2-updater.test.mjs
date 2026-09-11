// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { EventEmitter } from "node:events";
import { createServer, request } from "node:http";
import { createRequire } from "node:module";
import { sign } from "node:crypto";
import { gzipSync } from "node:zlib";
import { closeSync, copyFileSync, fstatSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import * as fsPromises from "node:fs/promises";
import { join } from "node:path";
import { macV2Fixture } from "./test-fixtures/mac-differential-v2.mjs";
import { macV2Canonical, macV2Digest, MAC_V2_METADATA } from "../src/main/mac-differential-v2-protocol";
import { MacDifferentialV2Updater } from "../src/main/mac-differential-v2-updater";

vi.mock("node:fs/promises", async importOriginal => {
  const actual = await importOriginal();
  return { ...actual, open: vi.fn(actual.open), rm: vi.fn(actual.rm) };
});
vi.mock("electron", () => ({ net: { fetch: (...args) => globalThis.fetch(...args) } }));
const require = createRequire(import.meta.url);
const { MacUpdater } = require("electron-updater/out/MacUpdater.js");
const { AppUpdater } = require("electron-updater/out/AppUpdater.js");
const { DownloadedUpdateHelper } = require("electron-updater/out/DownloadedUpdateHelper.js");
const { ElectronHttpExecutor } = require("electron-updater/out/electronHttpExecutor.js");
const { HttpExecutor, CancellationToken } = require("builder-util-runtime");
const semver = require("semver");
const dirs = [];
afterEach(() => { vi.restoreAllMocks(); vi.mocked(fsPromises.open).mockReset(); vi.mocked(fsPromises.rm).mockReset(); for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true }); });

// The real dependency full download/digest implementation. Only Electron's
// network transport and native handoff are substituted with a loopback harness.
class FullExecutor extends HttpExecutor {
  createRequest(options, callback) { return request({ ...options, agent: false }, callback); }
  download(url, destination, options) {
    this.onFull();
    this.sentinels ??= [];
    this.sentinels.push(openSync(this.sentinelPath, "w+"));
    return ElectronHttpExecutor.prototype.download.call(this, new URL(`${this.base}${url.pathname}`), destination, {
      ...options,
      onProgress: options.onProgress ? info => {
        this.inFullProgress = true;
        try { options.onProgress(info); } finally { this.inFullProgress = false; }
      } : undefined,
    });
  }
}

async function runCase(fault = "none", { arch = "arm64", progress = true, disabled = false, legacy = false, attempts = 1 } = {}) {
  const f = await macV2Fixture(); dirs.push(f.dir);
  const cache = join(f.dir, "cache"); mkdirSync(cache);
  const input = f.inputs.find(entry => entry.arch === arch);
  const target = f.target(arch);
  copyFileSync(input.baseline.zipPath, join(cache, "update.zip"));
  if (fault === "baseline-digest") writeFileSync(join(cache, "update.zip"), "wrong cached zip");
  if (fault === "no-cache") rmSync(join(cache, "update.zip"));
  if (fault === "baseline-directory") { rmSync(join(cache, "update.zip")); mkdirSync(join(cache, "update.zip")); }
  const selected = f.envelope.payload.artifacts.find(entry => entry.arch === arch);
  const metadata = structuredClone(f.envelope);
  const authorizationFaults = {
    "schema-missing": payload => { delete payload.schemaVersion; },
    "schema-string": payload => { payload.schemaVersion = "2"; },
    "schema-future": payload => { payload.schemaVersion = 3; },
    "minimum-missing": payload => { delete payload.minimumClientVersion; },
    "minimum-malformed": payload => { payload.minimumClientVersion = ">=1.0.0"; },
    "minimum-above": payload => { payload.minimumClientVersion = "1.0.1"; },
    "minimum-below": payload => { payload.minimumClientVersion = "0.9.0"; },
    "minimum-prerelease": payload => { payload.minimumClientVersion = "1.0.0-rc.1"; },
  };
  if (authorizationFaults[fault]) {
    authorizationFaults[fault](metadata.payload);
    metadata.signature.value = sign(null, Buffer.from(macV2Canonical(metadata.payload)), f.privateKey).toString("base64");
  }
  if (fault === "signature") metadata.signature.value = "A".repeat(86) + "==";
  if (fault === "candidate") {
    metadata.payload.candidate.version = "3.0.0"; metadata.payload.candidate.tag = "v3.0.0";
    for (const a of metadata.payload.artifacts) {
      a.zip.url = a.zip.url.replaceAll("2.0.0", "3.0.0");
      a.blockmap.url = a.blockmap.url.replaceAll("2.0.0", "3.0.0");
      a.baseline.blockmap.url = a.baseline.blockmap.url.replace("/v2.0.0/", "/v3.0.0/");
    }
    metadata.signature.value = sign(null, Buffer.from(macV2Canonical(metadata.payload)), f.privateKey).toString("base64");
  }
  if (fault === "baseline-version") {
    metadata.payload.artifacts.forEach(entry => {
      entry.baseline.version = "0.9.0"; entry.baseline.tag = "v0.9.0";
      entry.baseline.zip.url = entry.baseline.zip.url.replaceAll("1.0.0", "0.9.0");
      entry.baseline.blockmap.url = entry.baseline.blockmap.url.replaceAll("1.0.0", "0.9.0");
    });
    metadata.signature.value = sign(null, Buffer.from(macV2Canonical(metadata.payload)), f.privateKey).toString("base64");
  }
  if (fault === "denied" || fault === "expired") {
    if (fault === "denied") metadata.payload.enabled = false;
    else metadata.payload.expiresAt = "2020-01-01T00:00:00.000Z";
    metadata.signature.value = sign(null, Buffer.from(macV2Canonical(metadata.payload)), f.privateKey).toString("base64");
  }
  if (fault === "map-digest") writeFileSync(join(f.dir, decodeURIComponent(new URL(selected.blockmap.url).pathname.split("/").at(-1))), "corrupt");
  if (fault === "map-shape") {
    const bad = gzipSync(JSON.stringify({ version: "2", files: [] }));
    const map = metadata.payload.artifacts.find(entry => entry.arch === arch).blockmap;
    map.size = bad.length; map.sha512 = macV2Digest(bad);
    writeFileSync(join(f.dir, decodeURIComponent(new URL(map.url).pathname.split("/").at(-1))), bad);
    metadata.signature.value = sign(null, Buffer.from(macV2Canonical(metadata.payload)), f.privateKey).toString("base64");
  }
  if (fault === "alias") {
    metadata.payload.artifacts.find(entry => entry.arch === arch).blockmap.url = selected.blockmap.url.replace(/[^/]+$/, "alias.aoblockmap");
    metadata.signature.value = sign(null, Buffer.from(macV2Canonical(metadata.payload)), f.privateKey).toString("base64");
  }
  const requests = [], handoffs = [], observations = [];
  let rangeCount = 0, fullStarted = false, laterWork = 0;
  const token = new CancellationToken();
  const server = createServer((req, res) => {
    const name = decodeURIComponent(req.url.split("/").at(-1));
    const record = { name, range: req.headers.range, afterFull: fullStarted, bytes: 0 };
    requests.push(record);
    if (fullStarted && (req.headers.range || !name.endsWith(".zip"))) laterWork++;
    const send = (status, bytes, headers = {}) => { record.bytes = bytes.length; res.writeHead(status, { "Content-Length": bytes.length, ...headers }); res.end(bytes); };
    if (name === MAC_V2_METADATA) {
      if (fault === "missing-metadata") { send(404, Buffer.from("absent")); return; }
      send(200, Buffer.from(fault === "malformed-metadata" ? "{" : fault === "oversized-metadata" ? "x".repeat(256 * 1024 + 1) : JSON.stringify(metadata))); return;
    }
    if (name.endsWith(".aoblockmap")) {
      if (fault === "missing-map") { send(404, Buffer.alloc(0)); return; }
      send(200, readFileSync(join(f.dir, name))); return;
    }
    if (name.endsWith(".blockmap")) { send(404, Buffer.alloc(0)); return; }
    if (req.headers.range) {
      rangeCount++;
      if (fault === "cancel") { token.cancel(); res.destroy(); return; }
      if (fault === "timeout") return; // Cancellation by the attempt timeout closes this socket.
      if (fault === "416-delayed") {
        res.writeHead(416, { "Content-Length": 20 }); res.flushHeaders();
        const timer = setTimeout(() => res.end("x".repeat(20)), 200);
        res.once("close", () => clearTimeout(timer)); return;
      }
      if (["416", "416-body", "bad-full", "cleanup-output-close", "cleanup-baseline-close", "cleanup-remove"].includes(fault) || (fault === "second-416" && rangeCount === 2)) {
        send(416, fault === "416-body" ? Buffer.from("range rejected") : Buffer.alloc(0)); return;
      }
      const match = /^bytes=(\d+)-(\d+)$/.exec(req.headers.range);
      const start = Number(match[1]), end = Number(match[2]);
      const bytes = Buffer.from(target.subarray(start, end + 1));
      if (fault === "reconstruction-digest") bytes[0] ^= 255;
      if (fault === "range-reset") {
        res.writeHead(206, { "Content-Length": bytes.length, "Content-Range": `bytes ${start}-${end}/${target.length}` });
        res.write(bytes.subarray(0, Math.floor(bytes.length / 2)));
        setImmediate(() => res.destroy()); return;
      }
      send(fault === "range-200" ? 200 : 206, fault === "short-range" ? bytes.subarray(1) : fault === "oversized-range" ? Buffer.concat([bytes, Buffer.from("x")]) : bytes,
        { "Content-Range": fault === "wrong-range" ? `bytes 0-1/${target.length}` : `bytes ${start}-${end}/${target.length}` });
      return;
    }
    const bytes = Buffer.from(target);
    if (fault === "bad-full") bytes[1000] ^= 255;
    send(200, bytes);
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  const updater = new EventEmitter();
  Object.setPrototypeOf(updater, legacy ? MacUpdater.prototype : MacDifferentialV2Updater.prototype);
  updater.v2 = { trustedKeys: fault === "unknown-key" ? {} : f.trustedKeys, timeoutMs: fault === "timeout" ? 50 : 2000,
    fetch: (url, init) => fetch(`${base}${new URL(url).pathname}`, init) };
  if (fault === "capability-missing" || fault === "capability-unknown") {
    Object.defineProperty(updater, "differentialCapability", { value: fault === "capability-missing" ? undefined : "mac-differential-v3" });
  }
  updater.app = { version: "1.0.0" };
  updater.currentVersion = new semver.SemVer(fault === "installed-prerelease" ? "1.0.0-rc.1" : "1.0.0");
  updater.channel = fault === "ineligible" ? "latest" : "nightly";
  updater.logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
  updater.downloadedUpdateHelper = new DownloadedUpdateHelper(cache);
  updater.httpExecutor = new FullExecutor();
  updater.httpExecutor.base = base;
  updater.httpExecutor.sentinelPath = join(cache, "sentinel");
  updater.httpExecutor.onFull = () => { fullStarted = true; };
  updater.updateDownloaded = async (_file, event) => { handoffs.push(readFileSync(event.downloadedFile)); };
  if (progress) updater.on("download-progress", info => {
    if (fullStarted && !updater.httpExecutor.inFullProgress) laterWork++;
    observations.push(info);
  });
  const file = { url: new URL(selected.zip.url), info: { url: selected.zip.url, size: target.length, sha512: selected.zip.sha512 } };
  const provider = { resolveFiles: () => [file], isUseMultipleRangeRequest: false,
    getBlockMapFiles: vi.fn(() => [new URL(`${base}${new URL(selected.baseline.zip.url).pathname}.blockmap`), new URL(`${base}${file.url.pathname}.blockmap`)]) };
  const closeAttempts = [];
  const injectedOwners = [];
  let liveOwnerAfterAttempt = false;
  const promote = vi.spyOn(updater.downloadedUpdateHelper, "setDownloadedFile");
  if (["cancel-final-read", "cancel-output-close", "cancel-baseline-close", "cleanup-output-close", "cleanup-baseline-close"].includes(fault)) {
    const realOpen = (await vi.importActual("node:fs/promises")).open;
    vi.mocked(fsPromises.open).mockImplementation(async (...args) => {
      const handle = await realOpen(...args);
      const output = args[0] !== join(cache, "update.zip");
      let synced = false;
      const sync = handle.sync.bind(handle), read = handle.read.bind(handle), close = handle.close.bind(handle);
      handle.sync = async () => { await sync(); synced = true; };
      handle.read = async (...readArgs) => {
        const result = await read(...readArgs);
        if (output && synced && fault === "cancel-final-read" && readArgs[3] + result.bytesRead === target.length) token.cancel();
        return result;
      };
      handle.close = async () => {
        closeAttempts.push(output ? "output" : "baseline");
        if ((output && fault === "cleanup-output-close") || (!output && fault === "cleanup-baseline-close")) {
          injectedOwners.push({ handle, close });
          throw new Error("injected close EIO before descriptor release");
        }
        await close();
        if ((output && fault === "cancel-output-close") || (!output && fault === "cancel-baseline-close")) token.cancel();
      };
      return handle;
    });
  }
  if (fault === "cleanup-remove") vi.mocked(fsPromises.rm).mockRejectedValue(new Error("injected remove failure"));
  const stockDifferential = vi.spyOn(AppUpdater.prototype, "differentialDownloadInstaller");
  const archProbe = vi.spyOn(require("node:child_process"), "execFileSync").mockImplementation(command => command === "sysctl" ? "sysctl.proc_translated: 0" : arch === "arm64" ? "ARM" : "x86_64");
  const descriptor = Object.getOwnPropertyDescriptor(process, "arch");
  Object.defineProperty(process, "arch", { value: arch, configurable: true });
  let error, sentinelIntact = true;
  try {
    for (let attempt = 0; attempt < attempts; attempt++) {
      if (attempt) {
        // Reuse this updater/executor in the same process, with a valid baseline
        // and no staged target so the next manual attempt exercises transfer.
        await updater.downloadedUpdateHelper.clear();
        copyFileSync(input.baseline.zipPath, join(cache, "update.zip"));
      }
      fullStarted = false;
      await updater.doDownloadUpdate({ updateInfoAndProvider: { info: { version: "2.0.0", files: [file.info] }, provider },
        requestHeaders: {}, cancellationToken: token, disableDifferentialDownload: disabled });
      await new Promise(resolve => setImmediate(resolve));
    }
  } catch (err) { error = err; }
  finally {
    // Fixture disposal happens only after the updater settles. This cannot make
    // production cleanup succeed or hide fallback while the injected FD is live.
    for (const owner of injectedOwners) {
      if (owner.handle.fd >= 0) {
        fstatSync(owner.handle.fd);
        liveOwnerAfterAttempt = true;
        await owner.close();
      }
    }
    Object.defineProperty(process, "arch", descriptor); archProbe.mockRestore();
    for (const sentinel of updater.httpExecutor.sentinels ?? []) {
      try { fstatSync(sentinel); closeSync(sentinel); } catch { sentinelIntact = false; }
    }
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
  if (legacy) {
    expect(provider.getBlockMapFiles).toHaveBeenCalledTimes(1);
    expect(stockDifferential).toHaveBeenCalledTimes(1);
    expect(requests.some(req => req.name === MAC_V2_METADATA || req.name.endsWith(".aoblockmap"))).toBe(false);
  } else {
    expect(provider.getBlockMapFiles).not.toHaveBeenCalled();
    expect(stockDifferential).not.toHaveBeenCalled();
    expect(requests.some(req => req.name.endsWith(".blockmap"))).toBe(false);
  }
  stockDifferential.mockRestore();
  expect(laterWork).toBe(0);
  const promotions = promote.mock.calls.length;
  promote.mockRestore();
  const removals = vi.mocked(fsPromises.rm).mock.calls.length;
  return { liveOwnerAfterAttempt, closeAttempts, removals, promotions, requests, handoffs, error, target, sentinelIntact, observations, rangeCount };
}

const fullGETs = result => result.requests.filter(req => req.name.endsWith(".zip") && !req.range);
describe("real MacUpdater with isolated v2 transfer", () => {
  it.each(["arm64", "x64"])("reconstructs %s and hands off only exact target bytes", async arch => {
    const result = await runCase("none", { arch });
    expect(result.error).toBeUndefined();
    expect(result.rangeCount).toBeGreaterThan(0);
    expect(fullGETs(result)).toHaveLength(0);
    expect(result.handoffs).toHaveLength(1);
    expect(result.handoffs[0].equals(result.target)).toBe(true);
    const httpBytes = result.requests.reduce((sum, request) => sum + request.bytes, 0);
    expect(httpBytes).toBeLessThan(result.target.length);
    process.stdout.write(`${JSON.stringify({ protocol: "v2", arch, targetBytes: result.target.length, httpBytes, rangeCount: result.rangeCount, sha512: macV2Digest(result.target) })}\n`);
  });

  it.each(["schema-missing", "schema-string", "schema-future", "minimum-missing", "minimum-malformed", "minimum-above", "installed-prerelease", "capability-missing", "capability-unknown", "missing-metadata", "malformed-metadata", "oversized-metadata", "denied", "expired", "signature", "candidate", "baseline-version", "unknown-key", "alias", "map-digest", "map-shape", "missing-map", "baseline-digest", "baseline-directory", "no-cache", "ineligible", "416", "416-body", "416-delayed", "second-416", "range-reset", "range-200", "wrong-range", "short-range", "oversized-range", "reconstruction-digest", "timeout"])(
    "settles %s before exactly one clean full fallback", async fault => {
      const result = await runCase(fault);
      expect(result.error).toBeUndefined();
      if (/^(schema-|minimum-|capability-|installed-prerelease)/.test(fault)) expect(result.rangeCount).toBe(0);
      if (fault.startsWith("capability-")) expect(result.requests.some(req => req.name === MAC_V2_METADATA)).toBe(false);
      expect(fullGETs(result)).toHaveLength(1);
      expect(result.sentinelIntact).toBe(true);
      expect(result.handoffs).toHaveLength(1);
      expect(result.handoffs[0].equals(result.target)).toBe(true);
    });

  it.each(["minimum-below", "minimum-prerelease"])("accepts a matching capable client above %s", async fault => {
    const result = await runCase(fault);
    expect(result.error).toBeUndefined();
    expect(fullGETs(result)).toHaveLength(0);
    expect(result.rangeCount).toBeGreaterThan(0);
    expect(result.handoffs).toHaveLength(1);
    expect(result.handoffs[0].equals(result.target)).toBe(true);
  });

  it("settles repeated HTTP 416 failures on the same updater without late work or FD errors", async () => {
    const result = await runCase("416-body", { attempts: 3 });
    expect(result.error).toBeUndefined();
    expect(result.rangeCount).toBe(3);
    expect(fullGETs(result)).toHaveLength(3);
    expect(result.sentinelIntact).toBe(true);
    expect(result.handoffs).toHaveLength(3);
    expect(result.handoffs.every(bytes => bytes.equals(result.target))).toBe(true);
  });
  it("handles HTTP 416 without a progress subscriber", async () => {
    const result = await runCase("416-body", { progress: false });
    expect(result.error).toBeUndefined();
    expect(fullGETs(result)).toHaveLength(1);
    expect(result.sentinelIntact).toBe(true);
    expect(result.handoffs).toHaveLength(1);
    expect(result.handoffs[0].equals(result.target)).toBe(true);
  });
  it("rejects a corrupt full fallback before handoff", async () => {
    const result = await runCase("bad-full");
    expect(result.error?.message).toMatch(/sha512|checksum/);
    expect(fullGETs(result)).toHaveLength(1);
    expect(result.handoffs).toHaveLength(0);
    expect(result.sentinelIntact).toBe(true);
  });
  it.each(["cancel-final-read", "cancel-output-close", "cancel-baseline-close"])("keeps %s terminal before cache promotion and handoff", async fault => {
    const result = await runCase(fault);
    expect(result.error?.message).toMatch(/cancel/i);
    expect(result.removals).toBe(1);
    expect(fullGETs(result)).toHaveLength(0);
    expect(result.promotions).toBe(0);
    expect(result.handoffs).toHaveLength(0);
  });
  it.each(["cleanup-output-close", "cleanup-baseline-close", "cleanup-remove"])("stops %s before fallback, promotion or handoff", async fault => {
    const result = await runCase(fault);
    expect(result.error?.message).toMatch(/cleanup/i);
    expect(fullGETs(result)).toHaveLength(0);
    expect(result.promotions).toBe(0);
    expect(result.handoffs).toHaveLength(0);
    expect(result.removals).toBe(1);
    if (fault !== "cleanup-remove") {
      expect(result.liveOwnerAfterAttempt).toBe(true);
      expect(result.closeAttempts).toEqual(["output", "baseline"]);
    }
  });
  it("cancels without starting a full transfer or handing off", async () => {
    const result = await runCase("cancel");
    expect(result.error).toBeDefined();
    expect(fullGETs(result)).toHaveLength(0);
    expect(result.handoffs).toHaveLength(0);
  });
  it("leaves old MacUpdater clients on full ZIPs even with v2 assets on the same release", async () => {
    const result = await runCase("none", { legacy: true });
    expect(result.error).toBeUndefined();
    expect(result.rangeCount).toBe(0);
    expect(fullGETs(result)).toHaveLength(1);
    expect(result.handoffs).toHaveLength(1);
    expect(result.handoffs[0].equals(result.target)).toBe(true);
  });
  it("keeps current full-only clients off v2 metadata and maps entirely", async () => {
    const result = await runCase("416", { disabled: true });
    expect(result.error).toBeUndefined();
    expect(result.requests).toHaveLength(1);
    expect(fullGETs(result)).toHaveLength(1);
    expect(result.handoffs[0].equals(result.target)).toBe(true);
  });
});
