import { createHash, generateKeyPairSync } from "node:crypto";
import { createRequire } from "node:module";
import { mkdtempSync, readFileSync, writeFileSync, utimesSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { generateMacV2Assets } from "../mac-differential-v2.mjs";
const require = createRequire(import.meta.url);
const { zipSync } = require("cross-zip");

export async function macV2Fixture(arches = ["arm64", "x64"]) {
  const dir = mkdtempSync(join(tmpdir(), "ao-v2-fixture-"));
  const source = join(dir, "source");
  const old = join(dir, "old");
  mkdirSync(source); mkdirSync(old);
  const candidate = { version: "2.0.0", tag: "v2.0.0", commit: "a".repeat(40) };
  const baseline = { version: "1.0.0", tag: "v1.0.0", commit: "b".repeat(40) };
  const inputs = [];
  const zip = (bytes, destination) => {
    const payload = join(source, "fixture.bin");
    writeFileSync(payload, bytes);
    utimesSync(payload, new Date("2026-01-01T00:00:00Z"), new Date("2026-01-01T00:00:00Z"));
    zipSync(payload, destination);
  };
  for (const arch of arches) {
    const previous = Buffer.alloc(512_000);
    for (let offset = 0; offset < previous.length; offset += 64) createHash("sha512").update(`${arch}:${offset}`).digest().copy(previous, offset);
    const target = Buffer.from(previous);
    for (let offset = 180_000; offset < 228_000; offset += 64) createHash("sha512").update(`patch:${offset}`).digest().copy(target, offset);
    const oldPath = join(old, `Agent.Orchestrator-darwin-${arch}-1.0.0.zip`);
    const zipPath = join(dir, `Agent.Orchestrator-darwin-${arch}-2.0.0.zip`);
    zip(previous, oldPath); zip(target, zipPath);
    inputs.push({ arch, zipPath, baseline: { identity: baseline, zipPath: oldPath } });
  }
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const trustedKeys = { test: publicKey.export({ type: "spki", format: "pem" }).toString() };
  const envelope = await generateMacV2Assets({ minimumClientVersion: "1.0.0", allow: true, dir, candidate, channel: "nightly", inputs, keyId: "test", privateKey, expiresAt: new Date(Date.now() + 48 * 60 * 60 * 1000).toISOString() });
  return { dir, inputs, candidate, baseline, envelope, trustedKeys, privateKey,
    target: arch => readFileSync(inputs.find(input => input.arch === arch).zipPath) };
}
