import { createHash, createPublicKey, verify } from "node:crypto";
import semver from "semver";

export const MAC_V2_METADATA = "ao-diff-v2-mac.json";
export const MAC_V2_REPOSITORY = "Untrivial-ai/agent-orchestrator";
export const MAC_V2_MAX_METADATA = 256 * 1024;
export const MAC_V2_MAX_MAP = 8 * 1024 * 1024;
export type MacV2Arch = "arm64" | "x64";
export interface MacV2Identity { version: string; tag: string; commit: string }
export interface MacV2File { url: string; size: number; sha512: string }
export interface MacV2Artifact {
  arch: MacV2Arch;
  zip: MacV2File;
  blockmap: MacV2File;
  baseline: MacV2Identity & { zip: MacV2File; blockmap: MacV2File };
}
export interface MacV2Payload {
  schemaVersion: 2;
  minimumClientVersion: string;
  protocol: "ao-mac-differential-v2";
  repository: typeof MAC_V2_REPOSITORY;
  channel: "nightly";
  enabled: boolean;
  expiresAt: string;
  candidate: MacV2Identity;
  artifacts: MacV2Artifact[];
}
export interface MacV2Envelope {
  payload: MacV2Payload;
  signature: { keyId: string; value: string };
}

export function macV2Digest(bytes: Uint8Array): string {
  return createHash("sha512").update(bytes).digest("base64");
}

// Sign the canonical payload, not reserialized arbitrary input or the signature.
export function macV2Canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(macV2Canonical).join(",")}]`;
  if (value !== null && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map(key => `${JSON.stringify(key)}:${macV2Canonical(record[key])}`).join(",")}}`;
  }
  if (value === null || typeof value === "string" || typeof value === "boolean" ||
      (typeof value === "number" && Number.isFinite(value))) return JSON.stringify(value);
  throw new Error("Invalid v2 canonical value");
}

function record(value: unknown, keys: string[]): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid v2 object");
  const object = value as Record<string, unknown>;
  if (Object.keys(object).sort().join(",") !== [...keys].sort().join(",")) throw new Error("Invalid v2 fields");
  return object;
}
function identity(value: unknown): MacV2Identity {
  const o = record(value, ["version", "tag", "commit"]);
  if (typeof o.version !== "string" || semver.valid(o.version) !== o.version ||
      o.tag !== `v${o.version}` || typeof o.commit !== "string" || !/^[a-f0-9]{40}$/.test(o.commit)) {
    throw new Error("Invalid v2 release identity");
  }
  return o as unknown as MacV2Identity;
}
function file(value: unknown): MacV2File {
  const o = record(value, ["url", "size", "sha512"]);
  if (typeof o.url !== "string" || !Number.isSafeInteger(o.size) || (o.size as number) <= 0 ||
      typeof o.sha512 !== "string" || !/^[A-Za-z0-9+/]{86}==$/.test(o.sha512) ||
      Buffer.from(o.sha512, "base64").toString("base64") !== o.sha512) throw new Error("Invalid v2 file identity");
  return o as unknown as MacV2File;
}
export function macV2ReleaseURL(tag: string, name: string): string {
  return `https://github.com/${MAC_V2_REPOSITORY}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(name)}`;
}
function assetName(url: string, tag: string): string {
  const parsed = new URL(url);
  const name = decodeURIComponent(parsed.pathname.split("/").at(-1) ?? "");
  if (!name || /[/\\\x00-\x1f]/.test(name) || url !== macV2ReleaseURL(tag, name)) throw new Error("Invalid v2 asset location");
  return name;
}
export function macV2ZipName(url: string, id: MacV2Identity, arch: MacV2Arch): string {
  const name = assetName(url, id.tag);
  // Version required: unversioned ao-start aliases can never acquire maps.
  if (!name.endsWith(`-darwin-${arch}-${id.version}.zip`) || !/^[A-Za-z0-9][A-Za-z0-9._+-]+$/.test(name)) {
    throw new Error("Invalid v2 versioned ZIP");
  }
  return name;
}

export function validateMacV2Payload(value: unknown): MacV2Payload {
  const p = record(value, ["schemaVersion", "minimumClientVersion", "protocol", "repository", "channel", "enabled", "expiresAt", "candidate", "artifacts"]);
  if (p.schemaVersion !== 2 || typeof p.minimumClientVersion !== "string" ||
      semver.valid(p.minimumClientVersion) !== p.minimumClientVersion) throw new Error("Invalid v2 client authorization");
  if (p.protocol !== "ao-mac-differential-v2" || p.repository !== MAC_V2_REPOSITORY || p.channel !== "nightly") throw new Error("Ineligible v2 protocol/channel");
  if (p.enabled !== true || typeof p.expiresAt !== "string" ||
      !Number.isFinite(Date.parse(p.expiresAt)) || new Date(p.expiresAt).toISOString() !== p.expiresAt ||
      Date.parse(p.expiresAt) <= Date.now() || Date.parse(p.expiresAt) > Date.now() + 72 * 60 * 60 * 1000) {
    throw new Error("Denied or expired v2 authorization");
  }
  const candidate = identity(p.candidate);
  if (!Array.isArray(p.artifacts) || p.artifacts.length < 1 || p.artifacts.length > 2) throw new Error("Invalid v2 artifacts");
  const arches = new Set<string>();
  const urls = new Set<string>();
  for (const entry of p.artifacts) {
    const a = record(entry, ["arch", "zip", "blockmap", "baseline"]);
    if ((a.arch !== "arm64" && a.arch !== "x64") || arches.has(a.arch)) throw new Error("Invalid v2 architecture");
    arches.add(a.arch);
    const zip = file(a.zip), map = file(a.blockmap);
    const base = record(a.baseline, ["version", "tag", "commit", "zip", "blockmap"]);
    const baseline = identity({ version: base.version, tag: base.tag, commit: base.commit });
    if (!semver.lt(baseline.version, candidate.version)) throw new Error("Invalid v2 baseline order");
    const oldZip = file(base.zip), oldMap = file(base.blockmap);
    const name = macV2ZipName(zip.url, candidate, a.arch);
    const oldName = macV2ZipName(oldZip.url, baseline, a.arch);
    // Both versioned maps are on the candidate release. Neither is a legacy path.
    if (map.url !== macV2ReleaseURL(candidate.tag, `${name}.aoblockmap`) ||
        oldMap.url !== macV2ReleaseURL(candidate.tag, `${oldName}.aoblockmap`) ||
        map.size > MAC_V2_MAX_MAP || oldMap.size > MAC_V2_MAX_MAP) throw new Error("Invalid v2 blockmap location");
    for (const url of [zip.url, map.url, oldMap.url]) {
      if (urls.has(url)) throw new Error("Duplicate v2 asset");
      urls.add(url);
    }
  }
  return p as unknown as MacV2Payload;
}

export function verifyMacV2Envelope(bytes: Uint8Array, trustedKeys: Readonly<Record<string, string>>): MacV2Payload {
  if (bytes.byteLength > MAC_V2_MAX_METADATA) throw new Error("Oversized v2 metadata");
  const envelope = record(JSON.parse(Buffer.from(bytes).toString("utf8")), ["payload", "signature"]);
  const signature = record(envelope.signature, ["keyId", "value"]);
  if (typeof signature.keyId !== "string" || !Object.hasOwn(trustedKeys, signature.keyId) ||
      typeof signature.value !== "string" || !/^[A-Za-z0-9+/]{86}==$/.test(signature.value)) throw new Error("Unknown v2 signing key");
  const key = createPublicKey(trustedKeys[signature.keyId]);
  if (key.asymmetricKeyType !== "ed25519" || !verify(null, Buffer.from(macV2Canonical(envelope.payload)), key, Buffer.from(signature.value, "base64"))) {
    throw new Error("Invalid v2 signature");
  }
  return validateMacV2Payload(envelope.payload);
}
