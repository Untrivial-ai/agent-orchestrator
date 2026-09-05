// @vitest-environment node
import { afterEach, describe, expect, it } from "vitest";
import { sign } from "node:crypto";
import { readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { macV2Fixture } from "./test-fixtures/mac-differential-v2.mjs";
import { generateMacV2Assets, verifyMacV2Assets } from "./mac-differential-v2.mjs";
import { generateFeeds } from "./feed.mjs";
import { MAC_V2_METADATA, macV2Canonical, verifyMacV2Envelope, validateMacV2Payload } from "../src/main/mac-differential-v2-protocol";
const dirs = [];
afterEach(() => { for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true }); });
async function fixture() { const f = await macV2Fixture(); dirs.push(f.dir); return f; }

describe("isolated macOS differential v2 release contract", () => {
  it("signs both architectures and keeps every legacy feed free of v2 references", async () => {
    const f = await fixture();
    expect(verifyMacV2Assets({ allow: true, dir: f.dir, candidate: f.candidate, channel: "nightly", trustedKeys: f.trustedKeys })).toHaveLength(5);
    for (const channel of ["latest", "nightly", "pr3288"]) {
      await generateFeeds(f.dir, "2.0.0", channel, "2026-09-05T00:00:00Z");
      const text = readFileSync(join(f.dir, `${channel}-mac.yml`), "utf8");
      expect(text).not.toMatch(/blockMapSize|aoblockmap|ao-diff-v2|\.blockmap/);
      expect(text.match(/  - url:/g)).toHaveLength(2);
    }
    expect(readdirSync(f.dir).some(name => name.endsWith(".zip.blockmap"))).toBe(false);
    expect(verifyMacV2Envelope(readFileSync(join(f.dir, MAC_V2_METADATA)), f.trustedKeys)).toEqual(f.envelope.payload);
  });

  it.each([undefined, false, "true", 1, null])("denies generation and verification unless allow is exactly true (%s)", async allow => {
    const f = await fixture();
    const before = readdirSync(f.dir);
    await expect(generateMacV2Assets({ minimumClientVersion: "1.0.0", allow, dir: f.dir, channel: "nightly", candidate: f.candidate, inputs: f.inputs, keyId: "test", privateKey: f.privateKey, expiresAt: f.envelope.payload.expiresAt })).rejects.toThrow();
    expect(readdirSync(f.dir)).toEqual(before);
    expect(() => verifyMacV2Assets({ allow, dir: f.dir, candidate: f.candidate, channel: "nightly", trustedKeys: f.trustedKeys })).toThrow();
  });

  it.each([undefined, null, 1, "", "v1.0.0", ">=1.0.0"])("rejects invalid minimumClientVersion before generating assets (%s)", async minimumClientVersion => {
    const f = await fixture();
    const before = readdirSync(f.dir);
    await expect(generateMacV2Assets({ minimumClientVersion, allow: true, dir: f.dir, channel: "nightly", candidate: f.candidate, inputs: f.inputs, keyId: "test", privateKey: f.privateKey, expiresAt: f.envelope.payload.expiresAt })).rejects.toThrow("V2 generation denied");
    expect(readdirSync(f.dir)).toEqual(before);
  });

  it.each(["latest", "pr3288", "", undefined])("denies ineligible channel %s", async channel => {
    const f = await fixture();
    await expect(generateMacV2Assets({ minimumClientVersion: "1.0.0", allow: true, dir: f.dir, channel, candidate: f.candidate, inputs: f.inputs, keyId: "test", privateKey: f.privateKey, expiresAt: f.envelope.payload.expiresAt })).rejects.toThrow();
  });

  it.each(["alias", "legacy-map", "other-release", "other-repo", "duplicate-arch", "bad-commit", "unknown-field", "disabled", "expired", "malformed-allow"])("rejects even signed invalid identity: %s", async fault => {
    const f = await fixture();
    const payload = structuredClone(f.envelope.payload);
    const a = payload.artifacts[0];
    if (fault === "alias") a.zip.url = a.zip.url.replace("-2.0.0.zip", ".zip");
    if (fault === "legacy-map") a.blockmap.url = a.blockmap.url.replace(".aoblockmap", ".blockmap");
    if (fault === "other-release") a.blockmap.url = a.blockmap.url.replace("/v2.0.0/", "/v1.0.0/");
    if (fault === "other-repo") a.blockmap.url = a.blockmap.url.replace("Untrivial-ai", "attacker");
    if (fault === "duplicate-arch") payload.artifacts[1].arch = a.arch;
    if (fault === "bad-commit") payload.candidate.commit = "unknown";
    if (fault === "unknown-field") payload.allow = true;
    if (fault === "disabled") payload.enabled = false;
    if (fault === "malformed-allow") payload.enabled = "true";
    if (fault === "expired") payload.expiresAt = "2020-01-01T00:00:00.000Z";
    const signed = { payload, signature: { keyId: "test", value: sign(null, Buffer.from(macV2Canonical(payload)), f.privateKey).toString("base64") } };
    expect(() => verifyMacV2Envelope(Buffer.from(JSON.stringify(signed)), f.trustedKeys)).toThrow();
  });

  it("rejects tampering, unknown keys, stray maps, and corrupted release assets", async () => {
    const f = await fixture();
    const bytes = readFileSync(join(f.dir, MAC_V2_METADATA));
    expect(() => verifyMacV2Envelope(bytes, {})).toThrow();
    for (const candidate of [undefined, { ...f.candidate, commit: "c".repeat(40) }]) {
      expect(() => verifyMacV2Assets({ allow: true, dir: f.dir, candidate, channel: "nightly", trustedKeys: f.trustedKeys })).toThrow();
    }
    expect(() => verifyMacV2Assets({ allow: true, dir: f.dir, candidate: f.candidate, channel: "latest", trustedKeys: f.trustedKeys })).toThrow();
    const tampered = structuredClone(f.envelope); tampered.payload.candidate.commit = "c".repeat(40);
    expect(() => verifyMacV2Envelope(Buffer.from(JSON.stringify(tampered)), f.trustedKeys)).toThrow();
    for (const name of ["alias.aoblockmap", "Agent.Orchestrator-darwin-arm64-2.0.0.zip.blockmap"]) {
      writeFileSync(join(f.dir, name), "injected");
      expect(() => verifyMacV2Assets({ allow: true, dir: f.dir, candidate: f.candidate, channel: "nightly", trustedKeys: f.trustedKeys })).toThrow();
      rmSync(join(f.dir, name));
    }
    for (const reference of ["blockMapSize: 12", "url: alias.zip.blockmap", "url: alias.zip.aoblockmap", "url: ao-diff-v2-mac.json"]) {
      writeFileSync(join(f.dir, "nightly-mac.yml"), reference);
      expect(() => verifyMacV2Assets({ allow: true, dir: f.dir, candidate: f.candidate, channel: "nightly", trustedKeys: f.trustedKeys })).toThrow();
      rmSync(join(f.dir, "nightly-mac.yml"));
    }
    writeFileSync(f.inputs[0].zipPath, "corrupted");
    expect(() => verifyMacV2Assets({ allow: true, dir: f.dir, candidate: f.candidate, channel: "nightly", trustedKeys: f.trustedKeys })).toThrow();
    expect(() => validateMacV2Payload({})).toThrow();
  });
});
