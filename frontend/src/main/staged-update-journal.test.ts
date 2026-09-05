import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { StagedUpdateJournalStore } from "./staged-update-journal";
import type { StagedUpdateJournal, UpdateCandidate } from "./staged-update-state";

const dirs: string[] = [];
const candidate = (version: string, operationId = version): UpdateCandidate => ({ version, channel: "latest", operationId });
const staged = (version: string, at: number): StagedUpdateJournal => ({ schemaVersion: 1, state: "native-possibly-staged", staged: candidate(version), stagedAt: at });

afterEach(async () => Promise.all(dirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true }))));
async function tempDir(): Promise<string> {
  const dir = await mkdtemp(path.join(os.tmpdir(), "ao-staged-update-"));
  dirs.push(dir);
  return dir;
}

describe("StagedUpdateJournalStore", () => {
  it("migrates PR 4849 provenance without forgetting a possibly staged build", async () => {
    const dir = await tempDir();
    await writeFile(path.join(dir, "staged-update.json"), JSON.stringify({ version: "1.2.0", channel: "nightly", stagedAt: 100 }));
    expect(await new StagedUpdateJournalStore(dir).read("1.0.0")).toMatchObject({ state: "version-mismatch", staged: { version: "1.2.0", channel: "nightly" }, stagedAt: 100 });
  });
  it("round trips a valid journal", async () => {
    const store = new StagedUpdateJournalStore(await tempDir());
    await store.write(staged("1.2.0", 100));
    expect(await store.read("1.0.0")).toMatchObject({ state: "version-mismatch", staged: candidate("1.2.0"), runningVersion: "1.0.0" });
  });

  it("ignores an interrupted temporary write", async () => {
    const dir = await tempDir();
    const store = new StagedUpdateJournalStore(dir);
    await store.write(staged("1.2.0", 100));
    await writeFile(path.join(dir, "staged-update-journal.json.tmp-interrupted"), "{broken");
    expect(await store.read("1.0.0")).toMatchObject({ state: "version-mismatch", staged: candidate("1.2.0"), runningVersion: "1.0.0" });
  });

  it("quarantines corrupt records instead of treating them as none", async () => {
    const dir = await tempDir();
    await writeFile(path.join(dir, "staged-update-journal.json"), JSON.stringify({ schemaVersion: 99, state: "none" }));
    const store = new StagedUpdateJournalStore(dir);
    await expect(store.read("1.0.0")).rejects.toThrow(/invalid/i);
    expect((await readdir(dir)).some((name) => name.startsWith("staged-update-journal.json.corrupt-"))).toBe(true);
  });

  it("serializes rapid A to B to C writes", async () => {
    const dir = await tempDir();
    const store = new StagedUpdateJournalStore(dir);
    await Promise.all([store.write(staged("1.2.0", 100)), store.write(staged("1.3.0", 200)), store.write(staged("1.4.0", 300))]);
    expect(JSON.parse(await readFile(path.join(dir, "staged-update-journal.json"), "utf8"))).toEqual(staged("1.4.0", 300));
  });
});
