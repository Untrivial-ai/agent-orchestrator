// @vitest-environment node
// frontend/scripts/blockmap.test.mjs
import { describe, it, expect } from "vitest";
import { writeBlockmap } from "./blockmap.mjs";
import { mkdtempSync, writeFileSync, existsSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";

// Smoke test for the pinned app-builder-lib internal import. Since #3288
// workstream 3 the only caller is the windows installer branch of
// feed.mjs's generateFeeds; mac and linux take hashFile instead.
describe("writeBlockmap", () => {
	it("writes a gzip sidecar and returns the file's base64 sha512 + size", async () => {
		const dir = mkdtempSync(join(tmpdir(), "bm-"));
		const file = join(dir, "artifact.bin");
		// ~200KB of varied bytes so the chunker produces multiple chunks.
		const buf = Buffer.alloc(200_000);
		for (let i = 0; i < buf.length; i++) buf[i] = (i * 37) % 256;
		writeFileSync(file, buf);

		const { sha512, size } = await writeBlockmap(file);

		expect(size).toBe(buf.length);
		// Must match electron-updater's expectation: base64 SHA-512 of the raw file.
		expect(sha512).toBe(createHash("sha512").update(buf).digest("base64"));
		// Sidecar exists, is non-empty, and is gzip (magic bytes 1f 8b).
		expect(existsSync(`${file}.blockmap`)).toBe(true);
		const sidecar = readFileSync(`${file}.blockmap`);
		expect(sidecar.length).toBeGreaterThan(0);
		expect(sidecar[0]).toBe(0x1f);
		expect(sidecar[1]).toBe(0x8b);
		expect(statSync(`${file}.blockmap`).size).toBe(sidecar.length);
	});
});
