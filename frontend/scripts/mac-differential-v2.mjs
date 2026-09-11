// Explicit opt-in local preparation for a separately reviewed conductor change.
// Never called by feed.mjs, package, make or publish. No upload operation exists.
import semver from "semver";
import { createPublicKey, sign } from "node:crypto";
import { basename, join } from "node:path";
import { existsSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import {
  MAC_V2_METADATA, MAC_V2_REPOSITORY, macV2Canonical, macV2Digest,
  macV2ReleaseURL, validateMacV2Payload, verifyMacV2Envelope,
} from "../src/main/mac-differential-v2-protocol.ts";

import { writeBlockmap } from "./blockmap.mjs";
const identity = (url, bytes) => ({ url, size: bytes.length, sha512: macV2Digest(bytes) });

export async function generateMacV2Assets({ allow = false, channel, minimumClientVersion, candidate, inputs, dir, keyId, privateKey, expiresAt }) {
  if (typeof minimumClientVersion !== "string" || semver.valid(minimumClientVersion) !== minimumClientVersion || allow !== true || channel !== "nightly" || !keyId || !privateKey || !expiresAt || !Array.isArray(inputs) || !inputs.length) {
    throw new Error("V2 generation denied");
  }
  const created = [];
  try {
    const artifacts = [];
    for (const input of inputs) {
      const make = async (zipPath, id) => {
        const name = basename(zipPath);
        const mapName = `${name}.aoblockmap`;
        const mapPath = join(dir, mapName);
        if (existsSync(mapPath)) throw new Error("Existing v2 asset");
        created.push(mapPath);
        await writeBlockmap(zipPath, mapPath);
        return {
          zip: identity(macV2ReleaseURL(id.tag, name), readFileSync(zipPath)),
          blockmap: identity(macV2ReleaseURL(candidate.tag, mapName), readFileSync(mapPath)),
        };
      };
      const target = await make(input.zipPath, candidate);
      const baseline = await make(input.baseline.zipPath, input.baseline.identity);
      artifacts.push({ arch: input.arch, ...target, baseline: { ...input.baseline.identity, ...baseline } });
    }
    const payload = validateMacV2Payload({ schemaVersion: 2, minimumClientVersion, protocol: "ao-mac-differential-v2", repository: MAC_V2_REPOSITORY, channel, enabled: true, expiresAt, candidate, artifacts });
    const envelope = { payload, signature: { keyId, value: sign(null, Buffer.from(macV2Canonical(payload)), privateKey).toString("base64") } };
    const destination = join(dir, MAC_V2_METADATA);
    if (existsSync(destination)) throw new Error("Existing v2 metadata");
    created.push(destination);
    writeFileSync(destination, `${JSON.stringify(envelope, null, 2)}\n`, { flag: "wx" });
    verifyMacV2Assets({ allow: true, dir, candidate, channel, trustedKeys: { [keyId]: createPublicKey(privateKey).export({ type: "spki", format: "pem" }).toString() } });
    return envelope;
  } catch (error) {
    for (const file of created) rmSync(file, { force: true });
    throw error;
  }
}

/** Apply before upload and to a downloaded exact release inventory after upload. */
export function verifyMacV2Assets({ allow = false, dir, trustedKeys = {}, candidate, channel }) {
  const names = readdirSync(dir);
  if (names.some(name => /\.zip\.blockmap$/.test(name))) throw new Error("Conventional macOS blockmap forbidden");
  for (const name of names.filter(name => /^(?:latest|nightly|pr\d+)-mac\.yml$/.test(name))) {
    if (/blockMapSize|\.blockmap|\.aoblockmap|ao-diff-v2/i.test(readFileSync(join(dir, name), "utf8"))) {
      throw new Error("Legacy macOS feed must remain full-ZIP-only");
    }
  }
  const v2Names = names.filter(name => name.endsWith(".aoblockmap") || name === MAC_V2_METADATA);
  if (allow !== true) {
    if (v2Names.length) throw new Error("V2 assets forbidden while disabled");
    return [];
  }
  const payload = verifyMacV2Envelope(readFileSync(join(dir, MAC_V2_METADATA)), trustedKeys);
  if (channel !== payload.channel || macV2Canonical(candidate) !== macV2Canonical(payload.candidate)) {
    throw new Error("V2 release context mismatch");
  }
  const expected = [MAC_V2_METADATA];
  for (const artifact of payload.artifacts) {
    // Candidate ZIP must exist on this release. Baseline ZIP remains on its own
    // historical release; its signed identity is verified against the local cache.
    for (const file of [artifact.zip, artifact.blockmap, artifact.baseline.blockmap]) {
      const name = decodeURIComponent(new URL(file.url).pathname.split("/").at(-1));
      const bytes = readFileSync(join(dir, name));
      if (bytes.length !== file.size || macV2Digest(bytes) !== file.sha512) throw new Error("V2 asset identity mismatch");
      if (name.endsWith(".aoblockmap")) expected.push(name);
    }
  }
  if ([...v2Names].sort().join("\n") !== expected.sort().join("\n")) throw new Error("Unexpected v2 inventory");
  return expected;
}
