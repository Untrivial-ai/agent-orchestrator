// @vitest-environment node
import { it, expect, vi } from "vitest";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

it.each(["win32", "linux"])("uses supported directory durability on %s", async (platform) => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "ao-journal-platform-"));
  const descriptor = Object.getOwnPropertyDescriptor(process, "platform")!;
  const directorySync = vi.fn(async () => {
    throw Object.assign(new Error("directory flush denied"), { code: "EPERM" });
  });
  vi.resetModules();
  vi.doMock("node:fs/promises", async (original) => {
    const actual = await original<typeof import("node:fs/promises")>();
    return {
      ...actual,
      open: async (...args: Parameters<typeof actual.open>) => {
        const handle = await actual.open(...args);
        if (args[0] === dir) handle.sync = directorySync;
        return handle;
      },
    };
  });
  Object.defineProperty(process, "platform", { value: platform });
  try {
    const { StagedUpdateJournalStore } = await import("./staged-update-journal");
    const write = new StagedUpdateJournalStore(dir).write({ schemaVersion: 1, state: "none" });
    if (platform === "win32") {
      await expect(write).resolves.toBeUndefined();
      expect(directorySync).not.toHaveBeenCalled();
    } else {
      await expect(write).rejects.toMatchObject({ code: "EPERM" });
      expect(directorySync).toHaveBeenCalledOnce();
    }
    expect(JSON.parse(await readFile(path.join(dir, "staged-update-journal.json"), "utf8"))).toEqual({ schemaVersion: 1, state: "none" });
  } finally {
    Object.defineProperty(process, "platform", descriptor);
    vi.doUnmock("node:fs/promises");
    vi.resetModules();
    await rm(dir, { recursive: true, force: true });
  }
});
