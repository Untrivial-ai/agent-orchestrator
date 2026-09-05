import semver from "semver";
import { createHash } from "node:crypto";
import { constants } from "node:fs";
import { open, rm, type FileHandle } from "node:fs/promises";
import { resolve } from "node:path";
import { gunzipSync } from "node:zlib";
import {
  MAC_V2_METADATA, MAC_V2_MAX_METADATA, macV2Digest, macV2ReleaseURL,
  verifyMacV2Envelope, type MacV2File,
} from "./mac-differential-v2-protocol";

// Cleanup failure must never be converted into a replacement download.
export class MacV2CleanupError extends AggregateError {
  constructor(errors: unknown[]) { super(errors, "V2 resource cleanup failed"); }
}

const CHUNK = 1024 * 1024;
export interface MacV2TransferOptions {
  enabled: boolean;
  capability: "mac-differential-v2";
  arch: string;
  channel: string;
  installedVersion: string;
  candidateVersion: string;
  target: MacV2File;
  baselinePath: string;
  destination: string;
  trustedKeys: Readonly<Record<string, string>>;
  fetch: typeof globalThis.fetch;
  signal: AbortSignal;
  onProgress?: (progress: { total: number; transferred: number; percent: number; bytesPerSecond: number; delta: number }) => void;
}
interface Block { checksum: string; offset: number; size: number }

function blocks(bytes: Uint8Array, size: number): Block[] {
  const map = JSON.parse(gunzipSync(bytes, { maxOutputLength: 32 * 1024 * 1024 }).toString("utf8"));
  if (map.version !== "2" || !Array.isArray(map.files) || map.files.length !== 1) throw new Error("Invalid v2 map");
  const file = map.files[0];
  if (file.name !== "file" || file.offset !== 0 || !Array.isArray(file.sizes) || !Array.isArray(file.checksums) ||
      file.sizes.length !== file.checksums.length || file.sizes.length === 0 || file.sizes.length > 100_000) throw new Error("Invalid v2 map coverage");
  let offset = 0;
  const result = file.sizes.map((length: unknown, index: number) => {
    const checksum = file.checksums[index];
    if (!Number.isSafeInteger(length) || (length as number) <= 0 || (length as number) > CHUNK ||
        typeof checksum !== "string" || !/^[A-Za-z0-9+/=_-]{8,128}$/.test(checksum)) throw new Error("Invalid v2 block");
    const block = { checksum, offset, size: length as number };
    offset += block.size;
    return block;
  });
  if (offset !== size) throw new Error("Invalid v2 map length");
  return result;
}

async function readExactly(handle: FileHandle, length: number, position: number): Promise<Buffer> {
  const buffer = Buffer.alloc(length);
  let offset = 0;
  while (offset < length) {
    const read = await handle.read(buffer, offset, length - offset, position + offset);
    if (!read.bytesRead) throw new Error("Truncated v2 baseline");
    offset += read.bytesRead;
  }
  return buffer;
}
async function writeAll(handle: FileHandle, bytes: Uint8Array, position: number): Promise<void> {
  let offset = 0;
  while (offset < bytes.length) {
    const write = await handle.write(bytes, offset, bytes.length - offset, position + offset);
    if (!write.bytesWritten) throw new Error("Short v2 write");
    offset += write.bytesWritten;
  }
}
async function digest(handle: FileHandle, signal: AbortSignal): Promise<{ size: number; sha512: string }> {
  const stat = await handle.stat();
  if (!stat.isFile()) throw new Error("Invalid v2 local file");
  const size = stat.size;
  const hash = createHash("sha512");
  for (let offset = 0; offset < size; offset += CHUNK) {
    signal.throwIfAborted();
    hash.update(await readExactly(handle, Math.min(CHUNK, size - offset), offset));
  }
  return { size, sha512: hash.digest("base64") };
}

async function fetchBytes(options: MacV2TransferOptions, url: string, limit: number, range?: { start: number; end: number; total: number }): Promise<Buffer> {
  options.signal.throwIfAborted();
  const response = await options.fetch(url, {
    signal: options.signal, redirect: "follow",
    headers: { "Accept-Encoding": "identity", ...(range ? { Range: `bytes=${range.start}-${range.end}` } : {}) },
  });
  const reader = response.body?.getReader();
  try {
    if (response.status !== (range ? 206 : 200) || !reader ||
        (range && response.headers.get("content-range") !== `bytes ${range.start}-${range.end}/${range.total}`)) {
      throw new Error("Invalid v2 HTTP response");
    }
    const parts: Uint8Array[] = [];
    let size = 0;
    while (true) {
      options.signal.throwIfAborted();
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > limit) throw new Error("Oversized v2 response");
      parts.push(value);
    }
    if (range && size !== limit) throw new Error("Truncated v2 range");
    return Buffer.concat(parts, size);
  } finally {
    // Settle the HTTP body before returning or throwing. No request can keep
    // piping into a file after fallback starts: network bodies never own FDs.
    if (reader) {
      try { await reader.cancel(); } finally { reader.releaseLock(); }
    }
  }
}
async function verifiedMap(options: MacV2TransferOptions, file: MacV2File): Promise<Buffer> {
  const bytes = await fetchBytes(options, file.url, file.size);
  if (bytes.length !== file.size || macV2Digest(bytes) !== file.sha512) throw new Error("Invalid v2 map digest");
  return bytes;
}

/** Reconstruct only. The caller owns the single full fallback after this settles. */
export async function reconstructMacV2(options: MacV2TransferOptions): Promise<void> {
  if (options.capability !== "mac-differential-v2" || options.enabled !== true || options.channel !== "nightly" || !Object.keys(options.trustedKeys).length ||
      resolve(options.baselinePath) === resolve(options.destination)) throw new Error("Ineligible v2 attempt");
  const metadataURL = macV2ReleaseURL(`v${options.candidateVersion}`, MAC_V2_METADATA);
  const metadata = verifyMacV2Envelope(await fetchBytes(options, metadataURL, MAC_V2_MAX_METADATA), options.trustedKeys);
  if (semver.valid(options.installedVersion) !== options.installedVersion ||
      !semver.gte(options.installedVersion, metadata.minimumClientVersion)) throw new Error("Ineligible v2 client version");
  if (metadata.candidate.version !== options.candidateVersion || metadata.channel !== options.channel) throw new Error("Mismatched v2 candidate");
  const artifact = metadata.artifacts.find(entry => entry.arch === options.arch && entry.zip.url === options.target.url);
  if (!artifact || artifact.zip.size !== options.target.size || artifact.zip.sha512 !== options.target.sha512 ||
      artifact.baseline.version !== options.installedVersion) throw new Error("Mismatched v2 artifact/baseline");
  let baseline: FileHandle | undefined;
  let output: FileHandle | undefined;
  let completed = false;
  try {
    baseline = await open(options.baselinePath, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const actual = await digest(baseline, options.signal);
    if (actual.size !== artifact.baseline.zip.size || actual.sha512 !== artifact.baseline.zip.sha512) throw new Error("Mismatched v2 baseline bytes");
    const oldBlocks = blocks(await verifiedMap(options, artifact.baseline.blockmap), actual.size);
    const newBlocks = blocks(await verifiedMap(options, artifact.blockmap), options.target.size);
    const reusable = new Map(oldBlocks.map(block => [`${block.checksum}:${block.size}`, block.offset]));
    const plan: Array<{ offset: number; size: number; oldOffset?: number }> = [];
    for (const block of newBlocks) {
      const oldOffset = reusable.get(`${block.checksum}:${block.size}`);
      const previous = plan.at(-1);
      if (previous && previous.offset + previous.size === block.offset && previous.size + block.size <= CHUNK &&
          (oldOffset === undefined ? previous.oldOffset === undefined :
            previous.oldOffset !== undefined && previous.oldOffset + previous.size === oldOffset)) {
        previous.size += block.size;
      } else plan.push({ offset: block.offset, size: block.size, oldOffset });
    }
    output = await open(options.destination, constants.O_RDWR | constants.O_CREAT | constants.O_TRUNC | constants.O_NOFOLLOW | constants.O_NONBLOCK, 0o600);
    let transferred = 0;
    const started = Date.now();
    for (const block of plan) {
      options.signal.throwIfAborted();
      const oldOffset = block.oldOffset;
      const bytes = oldOffset === undefined
        ? await fetchBytes(options, artifact.zip.url, block.size, { start: block.offset, end: block.offset + block.size - 1, total: artifact.zip.size })
        : await readExactly(baseline, block.size, oldOffset);
      await writeAll(output, bytes, block.offset);
      if (oldOffset === undefined) transferred += bytes.length;
      options.onProgress?.({ total: artifact.zip.size, transferred,
        percent: 100 * (block.offset + block.size) / artifact.zip.size,
        bytesPerSecond: transferred * 1000 / Math.max(1, Date.now() - started), delta: oldOffset === undefined ? bytes.length : 0 });
    }
    options.signal.throwIfAborted();
    await output.sync();
    const reconstructed = await digest(output, options.signal);
    if (reconstructed.size !== artifact.zip.size || reconstructed.sha512 !== artifact.zip.sha512) {
      throw new Error("Mismatched reconstructed v2 digest");
    }
    completed = true;
  } finally {
    // Exactly one owner per handle. No WriteStream, pipe, callback continuation
    // or shared raw descriptor can race this close or the subsequent fallback.
    const cleanupErrors: unknown[] = [];
    for (const handle of [output, baseline]) {
      try { if (handle) await handle.close(); }
      catch (error) { cleanupErrors.push(error); }
    }
    if (output && (!completed || options.signal.aborted || cleanupErrors.length)) {
      try { await rm(options.destination, { force: true }); }
      catch (error) { cleanupErrors.push(error); }
    }
    if (cleanupErrors.length) throw new MacV2CleanupError(cleanupErrors);
  }
  options.signal.throwIfAborted();
}
