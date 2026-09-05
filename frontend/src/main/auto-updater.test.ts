// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync, chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import nodePath from "node:path";

type UpdateSettings = {
  enabled: boolean;
  channel: "latest" | "nightly";
  nightlyAck: boolean;
  feature: { pr: number } | null;
};

type UpdateSettingsReader = ReturnType<
  typeof vi.fn<() => Promise<UpdateSettings>>
>;
type UpdaterEventHandler = (...args: any[]) => void;

type ImportOptions = {
  reconcileFeaturePin?: (
    settings: UpdateSettings,
  ) => Promise<{ settings: UpdateSettings; cleared: boolean }>;
  isPackaged?: boolean;
};

type AutoUpdaterMock = {
  on: ReturnType<typeof vi.fn>;
  checkForUpdates: ReturnType<typeof vi.fn>;
  downloadUpdate: ReturnType<typeof vi.fn>;
  quitAndInstall: ReturnType<typeof vi.fn>;
  setFeedURL: ReturnType<typeof vi.fn>;
  channel: string;
  allowPrerelease: boolean;
  allowDowngrade: boolean;
  autoDownload: boolean;
  autoInstallOnAppQuit: boolean;
};

function createAutoUpdaterMock(): AutoUpdaterMock {
  return {
    on: vi.fn(),
    checkForUpdates: vi.fn(() => Promise.resolve()),
    downloadUpdate: vi.fn(() => Promise.resolve()),
    quitAndInstall: vi.fn(),
    setFeedURL: vi.fn(),
    channel: "",
    allowPrerelease: false,
    allowDowngrade: false,
    autoDownload: false,
    autoInstallOnAppQuit: false,
  };
}

// The module persists staged provenance beside the update settings, and that
// write is fire-and-forget: on a shared state dir a write from one test could
// land after the next test's cleanup. One fresh directory per test removes the
// race outright rather than trying to time it.
let stateDir = "";
beforeEach(() => {
  stateDir = mkdtempSync(nodePath.join(os.tmpdir(), "ao-updater-state-"));
});
afterEach(() => {
  rmSync(stateDir, { recursive: true, force: true });
});

/** Alias kept for readability: a re-import is a simulated relaunch. */
const importAutoUpdaterKeepingStagedFile = importAutoUpdater;

async function importAutoUpdater(
  settings: UpdateSettings | UpdateSettingsReader = {
    enabled: true,
    channel: "latest",
    nightlyAck: false,
    feature: null,
  },
  options: ImportOptions = {},
) {
  vi.resetModules();
  const updaterEvents = new Map<string, UpdaterEventHandler>();
  const autoUpdater = createAutoUpdaterMock();
  autoUpdater.on.mockImplementation(
    (event: string, handler: UpdaterEventHandler) => {
      updaterEvents.set(event, handler);
      return autoUpdater;
    },
  );
  const dialog = {
    showMessageBox: vi.fn(),
  };
  // Records what actually reaches renderers, by channel. Update telemetry rides
  // a channel separate from "updates:status" precisely so that suppressing a UI
  // status never suppresses its telemetry, and only a per-channel view can tell
  // those two apart.
  const sent: { channel: string; payload: unknown }[] = [];
  const fakeWindow = {
    isDestroyed: () => false,
    webContents: {
      send: (channel: string, payload: unknown) => {
        sent.push({ channel, payload });
      },
    },
  };
  const BrowserWindow = {
    getAllWindows: vi.fn(() => [fakeWindow]),
  };
  const statusMessages = () => sent.filter((m) => m.channel === "updates:status");
  const telemetryMessages = () => sent.filter((m) => m.channel === "updates:telemetry");
  vi.doMock("electron-updater", () => ({ autoUpdater }));
  vi.doMock("electron", () => ({
    app: {
      isPackaged: options.isPackaged ?? true,
      getVersion: () => "1.0.0",
    },
    BrowserWindow,
    dialog,
  }));
  const readUpdateSettings =
    typeof settings === "function"
      ? settings
      : vi.fn(() => Promise.resolve(settings));
  const writeUpdateSettings = vi.fn<
    (_stateDir: string, settings: UpdateSettings) => Promise<void>
  >(() => Promise.resolve());
  const updateUpdateSettings = vi.fn(
    async (
      _stateDir: string,
      update: (
        current: UpdateSettings,
      ) => UpdateSettings | Promise<UpdateSettings>,
    ) => update(await readUpdateSettings()),
  );
  vi.doMock("./update-settings", () => ({
    readUpdateSettings,
    writeUpdateSettings,
    updateUpdateSettings,
    UPDATE_SETTINGS_FILE_NAME: "update-settings.json",
  }));
  vi.doMock("./feature-builds", () => ({
    reconcileFeaturePin:
      options.reconcileFeaturePin ??
      ((current: UpdateSettings) =>
        Promise.resolve({ settings: current, cleared: false })),
  }));
  const module = await import("./auto-updater");
  return {
    sent,
    statusMessages,
    telemetryMessages,
    module,
    autoUpdater,
    dialog,
    BrowserWindow,
    updaterEvents,
    readUpdateSettings,
    writeUpdateSettings,
    updateUpdateSettings,
  };
}

function latestInterval(setIntervalSpy: ReturnType<typeof vi.spyOn>): {
  callback: () => void;
  delay: number;
  timer: ReturnType<typeof setInterval>;
} {
  const calls = setIntervalSpy.mock.calls;
  expect(calls.length).toBeGreaterThan(0);
  const [callback, delay] = calls.at(-1) ?? [];
  expect(typeof callback).toBe("function");
  expect(typeof delay).toBe("number");
  const results = setIntervalSpy.mock.results;
  const timer = results.at(-1)?.value as ReturnType<typeof setInterval>;
  return { callback: callback as () => void, delay: delay as number, timer };
}

function intervalWithDelay(
  setIntervalSpy: ReturnType<typeof vi.spyOn>,
  delay: number,
): () => void {
  const calls = setIntervalSpy.mock.calls as Array<[() => void, number]>;
  const call = calls.find(([, candidateDelay]) => candidateDelay === delay);
  expect(call).toBeDefined();
  return call?.[0] as () => void;
}

function deferred<T = void>(): {
  promise: Promise<T>;
  resolve: (value: T | PromiseLike<T>) => void;
} {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

async function flushMicrotasks(turns = 16): Promise<void> {
  for (let i = 0; i < turns; i += 1) {
    await Promise.resolve();
  }
}

describe("startAutoUpdates", () => {
  // stateDir comes from the per-test beforeEach above.

  afterEach(() => {
    rmSync(stateDir, { recursive: true, force: true });
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("runs the automatic updater check immediately on launch", async () => {
    const { module, autoUpdater } = await importAutoUpdater();

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.autoDownload).toBe(true);
    expect(autoUpdater.autoInstallOnAppQuit).toBe(true);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("keeps stable automatic checks on the hourly cadence", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, autoUpdater } = await importAutoUpdater();

    await module.startAutoUpdates(stateDir);
    const { delay } = latestInterval(setIntervalSpy);

    expect(delay).toBeGreaterThanOrEqual(60 * 60 * 1000);
    expect(delay).toBeLessThanOrEqual(2 * 60 * 60 * 1000);
    await vi.advanceTimersByTimeAsync(delay - 1);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
  });

  it("rechecks the nightly channel within 15 minutes", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, autoUpdater } = await importAutoUpdater({
      enabled: true,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.startAutoUpdates(stateDir);
    const { delay } = latestInterval(setIntervalSpy);

    expect(delay).toBe(15 * 60 * 1000);
    await vi.advanceTimersByTimeAsync(delay - 1);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
  });

  it("manual nightly checks resolve the newest completed release without the Atom feed", async () => {
    const platformManifest =
      process.platform === "darwin"
        ? "nightly-mac.yml"
        : process.platform === "linux"
          ? "nightly-linux.yml"
          : "nightly.yml";
    const resourcesPath = mkdtempSync(
      nodePath.join(os.tmpdir(), "ao-nightly-feed-"),
    );
    writeFileSync(
      nodePath.join(resourcesPath, "app-update.yml"),
      "provider: github\nowner: Untrivial-ai\nrepo: agent-orchestrator\n",
    );
    const originalResourcesPath = Object.getOwnPropertyDescriptor(
      process,
      "resourcesPath",
    );
    Object.defineProperty(process, "resourcesPath", {
      configurable: true,
      value: resourcesPath,
    });
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify([
          {
            tag_name: "v1.0.1-nightly.202608231518",
            draft: false,
            prerelease: true,
            assets: [{ name: "Agent.Orchestrator.dmg" }],
          },
          {
            tag_name: "v1.0.1-nightly.202608231517",
            draft: false,
            prerelease: true,
            assets: [{ name: platformManifest }],
          },
          {
            tag_name: "v1.0.1-nightly.202608231350",
            draft: false,
            prerelease: true,
            assets: [{ name: platformManifest }],
          },
        ]),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    try {
      const { module, autoUpdater } = await importAutoUpdater({
        enabled: true,
        channel: "nightly",
        nightlyAck: true,
        feature: null,
      });

      await module.checkForUpdatesNow(stateDir);

      expect(fetchMock).toHaveBeenCalledWith(
        "https://api.github.com/repos/Untrivial-ai/agent-orchestrator/releases?per_page=100",
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(1, {
        provider: "generic",
        url: "https://github.com/Untrivial-ai/agent-orchestrator/releases/download/v1.0.1-nightly.202608231517",
        channel: "nightly",
        useMultipleRangeRequest: false,
      });
      expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(2, {
        provider: "github",
        owner: "Untrivial-ai",
        repo: "agent-orchestrator",
      });
    } finally {
      if (originalResourcesPath) {
        Object.defineProperty(process, "resourcesPath", originalResourcesPath);
      } else {
        Reflect.deleteProperty(process, "resourcesPath");
      }
      rmSync(resourcesPath, { recursive: true, force: true });
    }
  });

  // Regression: the nightly direct feed is electron-updater's GENERIC provider,
  // which never populates releaseNotes -- only GitHubProvider does -- and
  // nightly-mac.yml has no field for them. So "what's new" could never say
  // anything on nightly unless the notes are resolved out of band.
  it("carries release notes for nightly, whose feed provider cannot", async () => {
    const platformManifest =
      process.platform === "darwin"
        ? "nightly-mac.yml"
        : process.platform === "linux"
          ? "nightly-linux.yml"
          : "nightly.yml";
    const resourcesPath = mkdtempSync(nodePath.join(os.tmpdir(), "ao-nightly-notes-"));
    writeFileSync(
      nodePath.join(resourcesPath, "app-update.yml"),
      "provider: github\nowner: Untrivial-ai\nrepo: agent-orchestrator\n",
    );
    const originalResourcesPath = Object.getOwnPropertyDescriptor(process, "resourcesPath");
    Object.defineProperty(process, "resourcesPath", { configurable: true, value: resourcesPath });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              tag_name: "v1.0.1-nightly.202608231517",
              draft: false,
              prerelease: true,
              body: "<ul><li>Stopped the re-stage loop</li><li>Rebuilt the Updates page</li></ul>",
              assets: [{ name: platformManifest }],
            },
          ]),
          { status: 200 },
        ),
      ),
    );

    try {
      const { module, updaterEvents, statusMessages } = await importAutoUpdater({
        enabled: false,
        channel: "nightly",
        nightlyAck: true,
        feature: null,
      });

      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-available")?.({ version: "1.0.1-nightly.202608231517" });

      // HTML stripped, list items kept as separate lines.
      expect(statusMessages().at(-1)?.payload).toMatchObject({
        state: "available",
        releaseNotes: "Stopped the re-stage loop\nRebuilt the Updates page",
      });
    } finally {
      if (originalResourcesPath) {
        Object.defineProperty(process, "resourcesPath", originalResourcesPath);
      } else {
        Reflect.deleteProperty(process, "resourcesPath");
      }
      rmSync(resourcesPath, { recursive: true, force: true });
    }
  });

  it("automatic nightly checks resolve the newest completed release without the Atom feed", async () => {
    const platformManifest =
      process.platform === "darwin"
        ? "nightly-mac.yml"
        : process.platform === "linux"
          ? "nightly-linux.yml"
          : "nightly.yml";
    const resourcesPath = mkdtempSync(
      nodePath.join(os.tmpdir(), "ao-nightly-feed-"),
    );
    writeFileSync(
      nodePath.join(resourcesPath, "app-update.yml"),
      "provider: github\nowner: Untrivial-ai\nrepo: agent-orchestrator\n",
    );
    const originalResourcesPath = Object.getOwnPropertyDescriptor(
      process,
      "resourcesPath",
    );
    Object.defineProperty(process, "resourcesPath", {
      configurable: true,
      value: resourcesPath,
    });
    // The newest entry is still uploading its manifest: the Atom feed would
    // point electron-updater at it and 404, and the automatic path swallows
    // that error, so the install would go silently stale (no sidebar row).
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify([
          {
            tag_name: "v1.0.1-nightly.202608231518",
            draft: false,
            prerelease: true,
            assets: [{ name: "Agent.Orchestrator.dmg" }],
          },
          {
            tag_name: "v1.0.1-nightly.202608231517",
            draft: false,
            prerelease: true,
            assets: [{ name: platformManifest }],
          },
        ]),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    try {
      const { module, autoUpdater } = await importAutoUpdater({
        enabled: true,
        channel: "nightly",
        nightlyAck: true,
        feature: null,
      });

      await module.startAutoUpdates(stateDir);

      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(1, {
        provider: "generic",
        url: "https://github.com/Untrivial-ai/agent-orchestrator/releases/download/v1.0.1-nightly.202608231517",
        channel: "nightly",
        useMultipleRangeRequest: false,
      });
      expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
      // Later background checks start from the normal provider again.
      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(2, {
        provider: "github",
        owner: "Untrivial-ai",
        repo: "agent-orchestrator",
      });
    } finally {
      if (originalResourcesPath) {
        Object.defineProperty(process, "resourcesPath", originalResourcesPath);
      } else {
        Reflect.deleteProperty(process, "resourcesPath");
      }
      rmSync(resourcesPath, { recursive: true, force: true });
    }
  });

  it("feature checks resolve the newest completed PR release without the Atom feed", async () => {
    const platformManifest =
      process.platform === "darwin"
        ? "pr4473-mac.yml"
        : process.platform === "linux"
          ? "pr4473-linux.yml"
          : "pr4473.yml";
    const resourcesPath = mkdtempSync(
      nodePath.join(os.tmpdir(), "ao-feature-feed-"),
    );
    writeFileSync(
      nodePath.join(resourcesPath, "app-update.yml"),
      "provider: github\nowner: Untrivial-ai\nrepo: agent-orchestrator\n",
    );
    const originalResourcesPath = Object.getOwnPropertyDescriptor(
      process,
      "resourcesPath",
    );
    Object.defineProperty(process, "resourcesPath", {
      configurable: true,
      value: resourcesPath,
    });
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify([
          {
            tag_name: "v1.0.0-pr4473.202608271543",
            draft: false,
            prerelease: true,
            assets: [{ name: "Agent.Orchestrator.dmg" }],
          },
          {
            tag_name: "v1.0.0-pr4473.202608271542",
            draft: false,
            prerelease: true,
            assets: [{ name: platformManifest }],
          },
          {
            tag_name: "v1.0.0-pr9999.202608271544",
            draft: false,
            prerelease: true,
            assets: [{ name: platformManifest }],
          },
        ]),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    try {
      const { module, autoUpdater } = await importAutoUpdater({
        enabled: true,
        channel: "nightly",
        nightlyAck: true,
        feature: { pr: 4473 },
      });

      await module.startAutoUpdates(stateDir);

      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(1, {
        provider: "generic",
        url: "https://github.com/Untrivial-ai/agent-orchestrator/releases/download/v1.0.0-pr4473.202608271542",
        channel: "pr4473",
        useMultipleRangeRequest: false,
      });
      expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(2, {
        provider: "github",
        owner: "Untrivial-ai",
        repo: "agent-orchestrator",
      });
    } finally {
      if (originalResourcesPath) {
        Object.defineProperty(process, "resourcesPath", originalResourcesPath);
      } else {
        Reflect.deleteProperty(process, "resourcesPath");
      }
      rmSync(resourcesPath, { recursive: true, force: true });
    }
  });

  it("checks stable on launch and hourly when automatic downloads are disabled", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, autoUpdater } = await importAutoUpdater({
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    });

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.autoDownload).toBe(false);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
    const { delay } = latestInterval(setIntervalSpy);
    expect(delay).toBe(60 * 60 * 1000);
    await vi.advanceTimersByTimeAsync(delay);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
  });

  it("checks nightly every 15 minutes when automatic downloads are disabled", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, autoUpdater } = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.channel).toBe("nightly");
    expect(autoUpdater.allowPrerelease).toBe(true);
    expect(autoUpdater.autoDownload).toBe(false);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
    expect(latestInterval(setIntervalSpy).delay).toBe(15 * 60 * 1000);
  });

  it("does not stack periodic automatic or retirement timers across repeated startAutoUpdates calls", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module } = await importAutoUpdater();

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);

    expect(setIntervalSpy).toHaveBeenCalledTimes(2);
    expect(setIntervalSpy.mock.calls.map(([, delay]) => delay).sort()).toEqual([
      30 * 60 * 1000,
      60 * 60 * 1000,
    ]);
  });

  it("logs periodic check failures without UI and retries on later ticks", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, dialog, BrowserWindow } =
      await importAutoUpdater();
    autoUpdater.checkForUpdates
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(undefined);

    await module.startAutoUpdates(stateDir);
    const { delay } = latestInterval(setIntervalSpy);

    await vi.advanceTimersByTimeAsync(delay);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      expect.any(Error),
    );
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
    expect(BrowserWindow.getAllWindows).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(delay);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(3);
  });

  it("logs updater error events during automatic checks without broadcasting renderer errors", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages, telemetryMessages } =
      await importAutoUpdater();
    const err = new Error("feed failed");
    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("error")?.(err);
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);

    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
    // The UI stays quiet: no status is pushed and the status never leaves idle.
    expect(statusMessages()).toEqual([]);
    expect(module.getUpdateStatus()).toEqual({ state: "idle" });
    // But the outcome is still reported. Automatic checks run hourly and are how
    // installs go silently stale, so suppressing the UI must not lose the signal.
    expect(telemetryMessages().map((m) => m.payload)).toEqual([
      {
        event: "ao.renderer.update_failed",
        phase: "check",
        trigger: "automatic",
        error_category: "unknown",
      },
    ]);
  });

  it("restores the prior renderer status when an automatic check emits checking before an error", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error("feed failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-available")?.({ version: "2.0.0" });
    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "2.0.0",
      checkedAt: expect.any(Number),
    });

    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(err);
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);

    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "2.0.0",
      checkedAt: expect.any(Number),
    });
  });

  it("restores the prior status when an automatic download fails after publishing progress", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const lateDownload = deferred();
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error("download failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-available")?.({ version: "2.0.0" });
    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "2.0.0",
      checkedAt: expect.any(Number),
    });

    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("update-available")?.({ version: "2.1.0" });
      updaterEvents.get("download-progress")?.({ percent: 42 });
      return Promise.resolve({ downloadPromise: lateDownload.promise });
    });
    const startPromise = module.startAutoUpdates(stateDir);
    await flushMicrotasks();
    expect(module.getUpdateStatus()).toEqual({
      state: "downloading",
      version: "2.1.0",
      percent: 42,
      checkedAt: expect.any(Number),
    });

    updaterEvents.get("error")?.(err);
    lateDownload.resolve();
    await startPromise;

    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "2.0.0",
      checkedAt: expect.any(Number),
    });
  });

  it("restores a staged update when an automatic check emits checking before an error", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-19T12:00:00.000Z"));
    const stagedAt = Date.now();
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error("feed failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    expect(module.getUpdateStatus()).toEqual({
      state: "downloaded",
      version: "2.1.0",
      stagedAt,
      escalated: false,
      checkedAt: expect.any(Number),
      staged: { version: "2.1.0", stagedAt, escalated: false },
    });

    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(err);
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);

    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
    expect(module.getUpdateStatus()).toEqual({
      state: "downloaded",
      version: "2.1.0",
      stagedAt,
      escalated: false,
      checkedAt: expect.any(Number),
      staged: { version: "2.1.0", stagedAt, escalated: false },
    });
  });

  it("tells the renderer once automatic checks have failed three times over", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();
    // A manifest 404 on every check: the failure mode that strands a nightly
    // install. It resets the net:: streak, so only the generic counter sees it.
    const err = new Error(
      'Cannot find nightly-mac.yml in the latest release artifacts: HttpError: 404 "method: GET url: https://example.invalid/nightly-mac.yml"',
    );
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("error")?.(err);
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    // Two failures are still a blip: nothing is broadcast at all.
    expect(statusMessages()).toEqual([]);
    expect(module.getUpdateStatus().checksFailing).toBeUndefined();

    await module.startAutoUpdates(stateDir);

    // The state stays truthful, the suppressed failure never replaces it,
    // and the flag rides along so the sidebar can offer a retry.
    expect(statusMessages().map((message) => message.payload)).toEqual([
      expect.objectContaining({ state: "idle", checksFailing: true }),
    ]);
    expect(module.getUpdateStatus()).toEqual(
      expect.objectContaining({ state: "idle", checksFailing: true }),
    );
    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
  });

  it("announces a failing streak once, not once per failure", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("error")?.(new Error("HttpError: 404 nightly-mac.yml"));
      return Promise.resolve();
    });

    for (let attempt = 0; attempt < 6; attempt += 1) {
      await module.startAutoUpdates(stateDir);
    }

    // Six failures, one announcement: a check every 15 minutes must not become
    // a status broadcast every 15 minutes.
    expect(statusMessages()).toHaveLength(1);
  });

  it("clears the failing-check streak as soon as a check reaches an answer", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("error")?.(new Error("HttpError: 404 nightly-mac.yml"));
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus().checksFailing).toBe(true);

    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("update-not-available")?.();
      return Promise.resolve();
    });
    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus()).toEqual(
      expect.objectContaining({ state: "not-available" }),
    );
    expect(module.getUpdateStatus().checksFailing).toBeUndefined();
  });

  it("does not overwrite a newer staged escalation when an automatic check fails", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const stagedAt = new Date("2026-07-17T12:00:00.000Z").getTime();
    vi.setSystemTime(stagedAt);
    const automaticCheck = deferred();
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error("feed failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    await Promise.resolve();
    await Promise.resolve();
    const { callback: runEscalation } = latestInterval(setIntervalSpy);

    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("checking-for-update")?.();
      return automaticCheck.promise;
    });
    const startPromise = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2));

    vi.setSystemTime(stagedAt + 49 * 60 * 60 * 1000);
    runEscalation();
    await Promise.resolve();
    await Promise.resolve();
    expect(module.getUpdateStatus()).toEqual({
      state: "downloaded",
      version: "2.1.0",
      stagedAt,
      escalated: true,
      checkedAt: expect.any(Number),
      staged: { version: "2.1.0", stagedAt, escalated: true },
    });

    updaterEvents.get("error")?.(err);
    automaticCheck.resolve();
    await startPromise;

    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
    expect(module.getUpdateStatus()).toEqual({
      state: "downloaded",
      version: "2.1.0",
      stagedAt,
      escalated: true,
      checkedAt: expect.any(Number),
      staged: { version: "2.1.0", stagedAt, escalated: true },
    });
  });

  it("restores an independent staged escalation after later automatic download progress fails", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const stagedAt = new Date("2026-07-17T12:00:00.000Z").getTime();
    vi.setSystemTime(stagedAt);
    const automaticDownload = deferred();
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error("download failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    await Promise.resolve();
    await Promise.resolve();
    const { callback: runEscalation } = latestInterval(setIntervalSpy);

    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("checking-for-update")?.();
      return Promise.resolve({ downloadPromise: automaticDownload.promise });
    });
    const startPromise = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2));

    vi.setSystemTime(stagedAt + 49 * 60 * 60 * 1000);
    runEscalation();
    await Promise.resolve();
    await Promise.resolve();
    updaterEvents.get("update-available")?.({ version: "2.2.0" });
    updaterEvents.get("download-progress")?.({ percent: 64 });
    expect(module.getUpdateStatus()).toMatchObject({
      state: "replacing",
      version: "2.2.0",
      percent: 64,
      stagedCandidate: expect.objectContaining({ version: "2.1.0" }),
      replacementCandidate: expect.objectContaining({ version: "2.2.0" }),
      replacementPhase: "full-fallback",
      checkedAt: expect.any(Number),
      // The staged 2.1.0 is still on disk while 2.2.0 downloads, so every
      // status carries it and the sidebar keeps an actionable row throughout.
      staged: { version: "2.1.0", stagedAt, escalated: true },
    });

    updaterEvents.get("error")?.(err);
    automaticDownload.resolve();
    await startPromise;

    expect(consoleErrorSpy).not.toHaveBeenCalledWith("auto-update check failed:", err);
    expect(module.getUpdateStatus()).toMatchObject({
      state: "replacement-failed",
      message: "download failed",
      stagedCandidate: expect.objectContaining({ version: "2.1.0" }),
      replacementCandidate: expect.objectContaining({ version: "2.2.0" }),
    });
  });

  it("keeps automatic download errors silent after checkForUpdates resolves", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const lateDownload = deferred();
    const { module, autoUpdater, updaterEvents, statusMessages, telemetryMessages } =
      await importAutoUpdater();
    const err = new Error("download failed");
    autoUpdater.checkForUpdates.mockResolvedValueOnce({
      downloadPromise: lateDownload.promise,
    });

    const startPromise = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1));
    let startSettled = false;
    void startPromise.then(() => {
      startSettled = true;
    });
    await Promise.resolve();
    await Promise.resolve();
    expect(startSettled).toBe(false);
    updaterEvents.get("error")?.(err);

    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      err,
    );
    expect(statusMessages()).toEqual([]);
    expect(telemetryMessages().map((m) => m.payload)).toEqual([
      {
        event: "ao.renderer.update_failed",
        phase: "check",
        trigger: "automatic",
        error_category: "unknown",
      },
    ]);
    lateDownload.resolve();
    await startPromise;
  });

  it("keeps manual download errors visible when requested during an automatic check", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const automaticCheck = deferred();
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error("manual download failed");
    autoUpdater.checkForUpdates.mockReturnValueOnce(automaticCheck.promise);
    autoUpdater.downloadUpdate.mockImplementationOnce(() => {
      updaterEvents.get("error")?.(err);
      return Promise.resolve();
    });

    const startPromise = module.startAutoUpdates(stateDir);
    await Promise.resolve();
    await Promise.resolve();
    const downloadPromise = module.downloadUpdateNow();
    await Promise.resolve();
    await Promise.resolve();

    automaticCheck.resolve();
    await Promise.all([startPromise, downloadPromise]);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "manual download failed",
    });
  });

  it("keeps manual updater error events visible to the renderer", async () => {
    const { module, BrowserWindow, updaterEvents } = await importAutoUpdater();
    const err = new Error("manual feed failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(err);

    expect(BrowserWindow.getAllWindows).toHaveBeenCalled();
    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "manual feed failed",
      checkedAt: expect.any(Number),
    });
  });

  it("broadcasts friendly error on manifest 404 event during manual check", async () => {
    vi.spyOn(console, "info").mockImplementation(() => undefined);
    const { module, updaterEvents } = await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(err);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message:
        "Couldn't check for updates, the update information was not found on the server.",
      checkedAt: expect.any(Number),
    });
  });

  it("broadcasts friendly error on manifest 404 event during manual download", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );
    autoUpdater.downloadUpdate.mockImplementationOnce(() => {
      updaterEvents.get("error")?.(err);
      return Promise.resolve();
    });

    await module.downloadUpdateNow();

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "Download failed, the update file was not found on the server.",
    });
  });

  it("broadcasts friendly error on rejected checkForUpdatesNow with manifest 404", async () => {
    vi.spyOn(console, "info").mockImplementation(() => undefined);
    const { module, autoUpdater } = await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );
    autoUpdater.checkForUpdates.mockRejectedValueOnce(err);

    await module.checkForUpdatesNow(stateDir);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message:
        "Couldn't check for updates, the update information was not found on the server.",
      checkedAt: expect.any(Number),
    });
  });

  it("broadcasts friendly error on rejected downloadUpdateNow with manifest 404", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater } = await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );
    autoUpdater.downloadUpdate.mockRejectedValueOnce(err);

    await module.downloadUpdateNow();

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "Download failed, the update file was not found on the server.",
    });
  });

  it("restores staged build on manifest 404 event during manual check", async () => {
    vi.spyOn(console, "info").mockImplementation(() => undefined);
    const { module, updaterEvents } = await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(err);

    expect(module.getUpdateStatus()).toEqual(
      expect.objectContaining({
        state: "downloaded",
        version: "2.1.0",
      }),
    );
  });

  it("restores staged build on rejected checkForUpdatesNow with manifest 404", async () => {
    vi.spyOn(console, "info").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    autoUpdater.checkForUpdates.mockRejectedValueOnce(err);
    await module.checkForUpdatesNow(stateDir);

    expect(module.getUpdateStatus()).toEqual(
      expect.objectContaining({
        state: "downloaded",
        version: "2.1.0",
      }),
    );
  });

  it("restores an earlier staged status immediately after the owned manual-check failure", async () => {
    vi.spyOn(console, "info").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();
    const err = new Error(
      'Cannot find latest-mac.yml in the latest release artifacts (https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml):\nHttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/latest-mac.yml"',
    );
    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      return Promise.resolve();
    });
    await module.checkForUpdatesNow(stateDir, {
      requestId: "earlier-download",
    });

    autoUpdater.checkForUpdates.mockRejectedValueOnce(err);
    await module.checkForUpdatesNow(stateDir, {
      requestId: "manual-update",
    });

    expect(statusMessages().slice(-2).map((message) => message.payload)).toEqual([
      expect.objectContaining({
        state: "error",
        requestId: "manual-update",
      }),
      expect.objectContaining({
        state: "downloaded",
        version: "2.1.0",
        requestId: "earlier-download",
      }),
    ]);
  });

  it("still surfaces non-manifest 404 errors", async () => {
    const { module, updaterEvents } = await importAutoUpdater();
    const err = new Error(
      'HttpError: 404 "method: GET url: https://github.com/AgentWrapper/agent-orchestrator/releases/download/v0.10.1/some-file.png"',
    );

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(err);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: err.message,
      checkedAt: expect.any(Number),
    });
  });

  it("flags net errors on a rejected manual check", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockRejectedValueOnce(new Error("net::ERR_FAILED"));

    await module.checkForUpdatesNow(stateDir);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "net::ERR_FAILED",
      netError: true,
      checkedAt: expect.any(Number),
    });
  });

  it("flags net errors on a net:: error event during a manual check", async () => {
    const { module, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "net::ERR_FAILED",
      netError: true,
      checkedAt: expect.any(Number),
    });
  });

  it("keeps non-net manual check errors verbatim", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockRejectedValueOnce(new Error("boom"));

    await module.checkForUpdatesNow(stateDir);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "boom",
      checkedAt: expect.any(Number),
    });
  });

  it("nudges a restart after three consecutive net:: automatic-check failures", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    // Below the threshold the suppressed automatic failure stays fully silent.
    expect(module.getUpdateStatus()).toEqual({ state: "idle" });

    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
      checksFailing: true,
    });
  });

  it("does not nudge for non-net automatic-check failures", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(new Error("HttpError: 500"));
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);

    // No restart guidance: the network stack was never the problem. The generic
    // failing-checks flag still trips, because three failed checks in a row
    // leave the install just as stranded.
    expect(module.getUpdateStatus().staleCheckNudge).toBeUndefined();
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      checksFailing: true,
    });
  });

  it("does not nudge when a non-net failure breaks the net:: streak", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    const netFail = () => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));
      return Promise.resolve();
    };
    const httpFail = () => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(new Error("HttpError: 500"));
      return Promise.resolve();
    };

    autoUpdater.checkForUpdates.mockImplementation(netFail);
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    autoUpdater.checkForUpdates.mockImplementation(httpFail);
    await module.startAutoUpdates(stateDir);
    autoUpdater.checkForUpdates.mockImplementation(netFail);
    await module.startAutoUpdates(stateDir);

    // net, net, non-net, net → the streak resets on the non-net failure, so the
    // lone trailing net error stays below the threshold (#3526). Four failed
    // checks is still four failed checks, so the generic flag trips.
    expect(module.getUpdateStatus().staleCheckNudge).toBeUndefined();
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      checksFailing: true,
    });
  });

  it("counts one failure when an automatic check both emits error and rejects", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));
      return Promise.reject(new Error("net::ERR_FAILED"));
    });

    // Two checks that each surface the failure twice must not reach the
    // threshold of three.
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus()).toEqual({ state: "idle" });

    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
      checksFailing: true,
    });
  });

  it("surfaces the nudge when automatic checks reject without an error event", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      return Promise.reject(new Error("net::ERR_FAILED"));
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    // Restored to the pre-check status (not stuck on "checking"), and no nudge
    // below the threshold.
    expect(module.getUpdateStatus()).toEqual({ state: "idle" });

    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
      checksFailing: true,
    });
  });

  it("stamps the nudge on getUpdateStatus even when no broadcast carried it", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();
    // No checking-for-update: there is no prior status to restore, so nothing
    // is broadcast until the streak itself becomes news.
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);

    // One broadcast, from crossing the threshold, carrying the unchanged state.
    expect(statusMessages().map((message) => message.payload)).toEqual([
      expect.objectContaining({
        state: "idle",
        staleCheckNudge: true,
        checksFailing: true,
      }),
    ]);
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
      checksFailing: true,
    });
  });

  it("clears the nudge once a check succeeds again", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
      checksFailing: true,
    });

    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("update-not-available")?.();
      return Promise.resolve();
    });
    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus()).toEqual({ state: "not-available", checkedAt: expect.any(Number) });
  });

  it("logs settings failures during automatic checks and retries on later ticks", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const readUpdateSettings = vi
      .fn<() => Promise<UpdateSettings>>()
      .mockRejectedValueOnce(new Error("settings locked"))
      .mockResolvedValue({
        enabled: true,
        channel: "latest",
        nightlyAck: false,
        feature: null,
      });
    const { module, autoUpdater } = await importAutoUpdater(readUpdateSettings);

    await expect(module.startAutoUpdates(stateDir)).resolves.toBeUndefined();
    expect(autoUpdater.checkForUpdates).not.toHaveBeenCalled();
    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "auto-update check failed:",
      expect.any(Error),
    );
    const { delay } = latestInterval(setIntervalSpy);

    await vi.advanceTimersByTimeAsync(delay);

    expect(readUpdateSettings).toHaveBeenCalledTimes(4);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("restores automatic download behavior on every automatic retry after a manual check", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, autoUpdater } = await importAutoUpdater();

    await module.startAutoUpdates(stateDir);
    const { delay } = latestInterval(setIntervalSpy);
    await module.checkForUpdatesNow(stateDir);
    expect(autoUpdater.autoDownload).toBe(false);

    await vi.advanceTimersByTimeAsync(delay);

    expect(autoUpdater.autoDownload).toBe(true);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(3);
  });

  it("waits for an in-flight manual check before a periodic automatic check restores autoDownload", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const manualCheck = deferred();
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates
      .mockResolvedValueOnce(undefined)
      .mockReturnValueOnce(manualCheck.promise)
      .mockResolvedValueOnce(undefined);

    await module.startAutoUpdates(stateDir);
    const { delay } = latestInterval(setIntervalSpy);
    const manualPromise = module.checkForUpdatesNow(stateDir);
    await flushMicrotasks();
    expect(autoUpdater.autoDownload).toBe(false);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(delay);
    await flushMicrotasks();
    expect(autoUpdater.autoDownload).toBe(false);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);

    manualCheck.resolve();
    await manualPromise;
    await flushMicrotasks();

    expect(autoUpdater.autoDownload).toBe(true);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(3);
  });

  it("preserves concurrent settings changes while clearing the same retired feature pin", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const retirementLookup = deferred();
    let current: UpdateSettings = {
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: { pr: 2709 },
    };
    const readUpdateSettings = vi.fn(() => Promise.resolve(current));
    const reconcileFeaturePin = vi
      .fn<
        (
          settings: UpdateSettings,
        ) => Promise<{ settings: UpdateSettings; cleared: boolean }>
      >()
      .mockResolvedValueOnce({ settings: current, cleared: false })
      .mockImplementationOnce(async (snapshot) => {
        await retirementLookup.promise;
        return { settings: { ...snapshot, feature: null }, cleared: true };
      });
    const { module, updateUpdateSettings } = await importAutoUpdater(
      readUpdateSettings,
      { reconcileFeaturePin },
    );
    updateUpdateSettings.mockImplementation(
      async (
        _stateDir: string,
        update: (
          settings: UpdateSettings,
        ) => UpdateSettings | Promise<UpdateSettings>,
      ) => {
        current = await update(current);
        return current;
      },
    );

    await module.startAutoUpdates(stateDir);
    intervalWithDelay(setIntervalSpy, 30 * 60 * 1000)();
    await flushMicrotasks();
    current = {
      enabled: true,
      channel: "nightly",
      nightlyAck: true,
      feature: { pr: 2709 },
    };
    retirementLookup.resolve();
    await flushMicrotasks();

    expect(current).toEqual({
      enabled: true,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });
  });

  it("does not clear a newly selected feature after an older pin retires", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const retirementLookup = deferred();
    let current: UpdateSettings = {
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: { pr: 2709 },
    };
    const readUpdateSettings = vi.fn(() => Promise.resolve(current));
    const reconcileFeaturePin = vi
      .fn<
        (
          settings: UpdateSettings,
        ) => Promise<{ settings: UpdateSettings; cleared: boolean }>
      >()
      .mockResolvedValueOnce({ settings: current, cleared: false })
      .mockImplementationOnce(async (snapshot) => {
        await retirementLookup.promise;
        return { settings: { ...snapshot, feature: null }, cleared: true };
      });
    const { module, updateUpdateSettings } = await importAutoUpdater(
      readUpdateSettings,
      { reconcileFeaturePin },
    );
    updateUpdateSettings.mockImplementation(
      async (
        _stateDir: string,
        update: (
          settings: UpdateSettings,
        ) => UpdateSettings | Promise<UpdateSettings>,
      ) => {
        current = await update(current);
        return current;
      },
    );

    await module.startAutoUpdates(stateDir);
    intervalWithDelay(setIntervalSpy, 30 * 60 * 1000)();
    await flushMicrotasks();
    current = {
      enabled: true,
      channel: "nightly",
      nightlyAck: true,
      feature: { pr: 2710 },
    };
    retirementLookup.resolve();
    await flushMicrotasks();

    expect(current).toEqual({
      enabled: true,
      channel: "nightly",
      nightlyAck: true,
      feature: { pr: 2710 },
    });
  });

  it("coalesces retirement ticks queued behind a long updater operation", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const automaticCheck = deferred();
    const settings: UpdateSettings = {
      enabled: true,
      channel: "latest",
      nightlyAck: false,
      feature: { pr: 2709 },
    };
    const reconcileFeaturePin = vi.fn((current: UpdateSettings) =>
      Promise.resolve({ settings: current, cleared: false }),
    );
    const { module, autoUpdater } = await importAutoUpdater(settings, {
      reconcileFeaturePin,
    });
    autoUpdater.checkForUpdates.mockReturnValueOnce(automaticCheck.promise);

    const startPromise = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(reconcileFeaturePin).toHaveBeenCalledTimes(1));

    const runRetirementPoll = intervalWithDelay(setIntervalSpy, 30 * 60 * 1000);
    runRetirementPoll();
    runRetirementPoll();
    runRetirementPoll();
    await flushMicrotasks();
    expect(reconcileFeaturePin).toHaveBeenCalledTimes(1);

    automaticCheck.resolve();
    await startPromise;
    await flushMicrotasks();
    expect(reconcileFeaturePin).toHaveBeenCalledTimes(2);

    runRetirementPoll();
    await flushMicrotasks();
    expect(reconcileFeaturePin).toHaveBeenCalledTimes(3);
  });

  it("applies feature settings and owns its check after an in-flight automatic check", async () => {
    const automaticCheck = deferred();
    const featureSettings: UpdateSettings = {
      enabled: true,
      channel: "latest",
      nightlyAck: false,
      feature: { pr: 2709 },
    };
    const { module, autoUpdater, updaterEvents, writeUpdateSettings } =
      await importAutoUpdater();
    autoUpdater.checkForUpdates
      .mockReturnValueOnce(automaticCheck.promise)
      .mockImplementationOnce(() => {
        expect(writeUpdateSettings).toHaveBeenCalledWith(
          stateDir,
          featureSettings,
        );
        expect(autoUpdater.channel).toBe("pr2709");
        updaterEvents.get("update-available")?.({ version: "2.0.0-pr2709.1" });
        return Promise.resolve();
      });

    const startPromise = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1));
    const featureCheck = module.checkForUpdatesNow(stateDir, {
      settings: featureSettings,
      requestId: "feature-2709",
    });
    await flushMicrotasks();

    updaterEvents.get("update-available")?.({ version: "1.9.0" });
    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "1.9.0",
      checkedAt: expect.any(Number),
    });

    automaticCheck.resolve();
    await Promise.all([startPromise, featureCheck]);

    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "2.0.0-pr2709.1",
      requestId: "feature-2709",
      checkedAt: expect.any(Number),
    });
  });

  it("keeps feature request ownership through downloaded escalation rebroadcasts", async () => {
    vi.useFakeTimers();
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("update-downloaded")?.({ version: "2.0.0-pr2709.1" });
      return Promise.resolve();
    });

    await module.checkForUpdatesNow(stateDir, { requestId: "feature-2709" });
    await flushMicrotasks();

    expect(module.getUpdateStatus()).toEqual(
      expect.objectContaining({
        state: "downloaded",
        version: "2.0.0-pr2709.1",
        requestId: "feature-2709",
      }),
    );
  });

  it("reconciles the automatic scheduler when settings change at runtime", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval");
    let current: UpdateSettings = {
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    };
    const readUpdateSettings = vi.fn(() => Promise.resolve(current));
    const { module, autoUpdater, writeUpdateSettings } =
      await importAutoUpdater(readUpdateSettings);
    writeUpdateSettings.mockImplementation(
      async (_stateDir: string, next: UpdateSettings) => {
        current = next;
      },
    );

    await module.startAutoUpdates(stateDir);
    expect(setIntervalSpy).toHaveBeenCalledTimes(2);

    await module.setUpdateSettings(stateDir, { ...current, enabled: true });
    expect(setIntervalSpy.mock.calls.map(([, delay]) => delay)).toContain(
      60 * 60 * 1000,
    );

    await module.setUpdateSettings(stateDir, {
      ...current,
      channel: "nightly",
      nightlyAck: true,
    });
    expect(latestInterval(setIntervalSpy).delay).toBe(15 * 60 * 1000);

    await module.setUpdateSettings(stateDir, { ...current, enabled: false });
    expect(clearIntervalSpy).toHaveBeenCalled();
    expect(latestInterval(setIntervalSpy).delay).toBe(15 * 60 * 1000);
    await vi.advanceTimersByTimeAsync(15 * 60 * 1000);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
    expect(autoUpdater.autoDownload).toBe(false);
  });

  it("does not let a stale disabled check clear a concurrently enabled scheduler", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const disabledRead = deferred<UpdateSettings>();
    let current: UpdateSettings = {
      enabled: true,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    };
    const readUpdateSettings = vi
      .fn<() => Promise<UpdateSettings>>()
      .mockResolvedValueOnce(current)
      .mockReturnValueOnce(disabledRead.promise)
      .mockImplementation(() => Promise.resolve(current));
    const { module, autoUpdater, writeUpdateSettings } =
      await importAutoUpdater(readUpdateSettings);
    writeUpdateSettings.mockImplementation(
      async (_stateDir: string, next: UpdateSettings) => {
        current = next;
      },
    );

    await module.startAutoUpdates(stateDir);
    intervalWithDelay(setIntervalSpy, 60 * 60 * 1000)();
    await flushMicrotasks();
    const enable = module.setUpdateSettings(stateDir, {
      ...current,
      enabled: true,
    });
    disabledRead.resolve({ ...current, enabled: false });
    await enable;
    await flushMicrotasks();

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(3);
  });

  it("coalesces hourly ticks while an automatic check is still running", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const slowCheck = deferred();
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates
      .mockResolvedValueOnce(undefined)
      .mockReturnValueOnce(slowCheck.promise)
      .mockResolvedValueOnce(undefined);

    await module.startAutoUpdates(stateDir);
    const runHourly = intervalWithDelay(setIntervalSpy, 60 * 60 * 1000);
    runHourly();
    await flushMicrotasks();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);

    runHourly();
    runHourly();
    runHourly();
    await flushMicrotasks();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);

    slowCheck.resolve();
    await flushMicrotasks();
    runHourly();
    await flushMicrotasks();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(3);
  });

  it("unrefs the periodic timer when the runtime supports it", async () => {
    const unref = vi.fn();
    const setIntervalStub = vi.fn((_callback: () => void, _delay?: number) => ({
      unref,
    }));
    vi.stubGlobal("setInterval", setIntervalStub);
    const { module } = await importAutoUpdater();

    await module.startAutoUpdates(stateDir);

    expect(unref).toHaveBeenCalledTimes(2);
  });

  // Regression: electron-updater does NOT treat "already in the cache" as done.
  // A cache hit still runs the download task's completion path, which on macOS
  // copies the whole zip to update.zip and hands Squirrel a fresh install
  // request. With autoDownload left on, that repeated on every check for as
  // long as the user went without quitting.
  it("suspends auto-download while a build is already staged", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const autoDownloadPerCheck: boolean[] = [];
    autoUpdater.checkForUpdates.mockImplementation(() => {
      autoDownloadPerCheck.push(autoUpdater.autoDownload);
      updaterEvents.get("update-available")?.({ version: "2.1.0" });
      return Promise.resolve({
        isUpdateAvailable: true,
        updateInfo: { version: "2.1.0" },
      });
    });

    await module.startAutoUpdates(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    await module.startAutoUpdates(stateDir);

    expect(autoDownloadPerCheck).toEqual([true, false]);
    expect(autoUpdater.downloadUpdate).not.toHaveBeenCalled();
  });

  it("still auto-downloads a build newer than the staged one", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() =>
      Promise.resolve({
        isUpdateAvailable: true,
        updateInfo: { version: "2.2.0" },
      }),
    );

    await module.startAutoUpdates(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    await module.startAutoUpdates(stateDir);

    // autoDownload was suspended for the staged 2.1.0, so the newer build has
    // to be fetched explicitly rather than silently skipped.
    expect(autoUpdater.autoDownload).toBe(false);
    expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
  });

  // Regression: the staged clock feeds the latest-channel 48h escalation rule.
  // Re-stamping it on every re-stage meant the clock was never more than one
  // check interval old, so that rule could never fire.
  it("keeps the original staged time when the same build is re-staged", async () => {
    vi.useFakeTimers();
    const stagedAt = new Date("2026-07-17T12:00:00.000Z").getTime();
    vi.setSystemTime(stagedAt);
    const { module, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    expect(module.getUpdateStatus().stagedAt).toBe(stagedAt);

    vi.setSystemTime(stagedAt + 60 * 60 * 1000);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    expect(module.getUpdateStatus().stagedAt).toBe(stagedAt);

    // A genuinely different build does restart the clock.
    updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
    expect(module.getUpdateStatus().stagedAt).toBe(stagedAt + 60 * 60 * 1000);
  });

  it("re-arms the escalation timer only for a newly staged build", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    const afterFirst = setIntervalSpy.mock.calls.length;

    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    // Re-arming on a re-stage would push the next evaluation out by another 30
    // minutes every time, and nightly re-stages every 15, the loop would never
    // get a turn.
    expect(setIntervalSpy.mock.calls.length).toBe(afterFirst);

    updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
    expect(setIntervalSpy.mock.calls.length).toBeGreaterThan(afterFirst);
  });

  // Regression: electron-updater returns the in-flight promise when a check is
  // already running, so a second caller's events were consumed elsewhere and
  // nothing ever moved the status off "checking". Settings keys its spinner and
  // its disabled Check button off that state, so the page wedged silently.
  it("settles a manual check that resolves without emitting any event", async () => {
    const { module, autoUpdater, statusMessages } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockResolvedValue({
      isUpdateAvailable: false,
      updateInfo: { version: "1.0.0" },
    });

    await module.checkForUpdatesNow(stateDir, { requestId: "manual-update-1" });

    expect(module.getUpdateStatus().state).toBe("not-available");
    expect(statusMessages().at(-1)?.payload).toMatchObject({
      state: "not-available",
      checkedAt: expect.any(Number),
    });
  });

  it("settles an event-less manual check as available when the feed has a build", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockResolvedValue({
      isUpdateAvailable: true,
      updateInfo: { version: "2.5.0" },
    });

    await module.checkForUpdatesNow(stateDir, { requestId: "manual-update-1" });

    expect(module.getUpdateStatus()).toMatchObject({
      state: "available",
      version: "2.5.0",
    });
  });

  it("settles an event-less manual check back to the staged build", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });

    autoUpdater.checkForUpdates.mockResolvedValue({
      isUpdateAvailable: false,
      updateInfo: { version: "2.1.0" },
    });
    await module.checkForUpdatesNow(stateDir, { requestId: "manual-update-2" });

    expect(module.getUpdateStatus()).toMatchObject({
      state: "downloaded",
      version: "2.1.0",
    });
  });

  // Regression: the sidebar's restart row keyed off `state`, which a routine
  // check drives through checking/available/not-available while the staged
  // build is untouched. The row blinked out of existence every 15 minutes.
  // Regression: stagedVersion/stagedChannel were module state, so a relaunch
  // that did NOT install came back knowing nothing about the build still armed
  // in the cache, and a channel switch after that restart could not be
  // recognised as stranding anything.
  it("remembers a staged build's provenance across a restart", async () => {
    const first = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });
    await first.module.checkForUpdatesNow(stateDir);
    first.updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });
    // The persist is fire-and-forget real I/O; flushing microtasks cannot land it.
    await new Promise((resolve) => setTimeout(resolve, 50));

    // A fresh process: module state is gone, only the file survives.
    const second = await importAutoUpdaterKeepingStagedFile({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });
    await second.module.startAutoUpdates(stateDir);
    expect(second.module.getUpdateStatus().staged).toMatchObject({ version: "2.1.0-nightly.1" });

    // And the switch that follows is now recognised as stranding it.
    const autoDownloadAtCheck: boolean[] = [];
    second.autoUpdater.checkForUpdates.mockImplementation(() => {
      autoDownloadAtCheck.push(second.autoUpdater.autoDownload);
      return Promise.resolve({ isUpdateAvailable: true, updateInfo: { version: "2.0.0" } });
    });
    await second.module.checkForUpdatesNow(stateDir, {
      settings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
    });
    expect(autoDownloadAtCheck).toEqual([true]);
  });

  it("drops persisted provenance once that build is the running version", async () => {
    const first = await importAutoUpdater();
    await first.module.checkForUpdatesNow(stateDir);
    // The harness reports app.getVersion() as 1.0.0.
    first.updaterEvents.get("update-downloaded")?.({ version: "1.0.0" });
    await new Promise((resolve) => setTimeout(resolve, 50));

    const second = await importAutoUpdaterKeepingStagedFile();
    await second.module.startAutoUpdates(stateDir);
    expect(second.module.getUpdateStatus().staged).toBeUndefined();
  });

  // Regression: electron-updater keeps its request open when a download stops
  // receiving bytes, so the last percentage stuck forever, the serialized
  // updater queue stayed occupied, and nothing offered a retry.
  it("cancels a download that stops advancing and offers a retry", async () => {
    vi.useFakeTimers();
    const { module, autoUpdater, updaterEvents, statusMessages } = await importAutoUpdater();
    const cancel = vi.fn();
    autoUpdater.downloadUpdate.mockImplementation((token: { cancel?: () => void } | undefined) => {
      if (token) token.cancel = cancel;
      return new Promise(() => undefined);
    });

    void module.downloadUpdateNow("manual-download-1");
    await flushMicrotasks();
    updaterEvents.get("download-progress")?.({ percent: 37 });
    expect(statusMessages().at(-1)?.payload).toMatchObject({ state: "downloading", percent: 37 });

    // Just short of the window: still considered alive.
    await vi.advanceTimersByTimeAsync(2 * 60 * 1000 - 1);
    expect(cancel).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(statusMessages().at(-1)?.payload).toMatchObject({
      state: "error",
      message: "Download stopped responding. Try again.",
    });
  });

  it("keeps a slow but advancing download alive", async () => {
    vi.useFakeTimers();
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    const cancel = vi.fn();
    autoUpdater.downloadUpdate.mockImplementation((token: { cancel?: () => void } | undefined) => {
      if (token) token.cancel = cancel;
      return new Promise(() => undefined);
    });

    void module.downloadUpdateNow();
    await flushMicrotasks();
    // Progress every 90 seconds for five minutes: slow, but never stalled.
    for (const percent of [10, 20, 30, 40]) {
      updaterEvents.get("download-progress")?.({ percent });
      await vi.advanceTimersByTimeAsync(90 * 1000);
    }
    expect(cancel).not.toHaveBeenCalled();
  });

  it("does not replace the stall message with a cancellation error", async () => {
    vi.useFakeTimers();
    const { module, autoUpdater, updaterEvents, statusMessages } = await importAutoUpdater();
    autoUpdater.downloadUpdate.mockImplementation((token: { cancel?: () => void } | undefined) => {
      if (token) token.cancel = () => undefined;
      return new Promise(() => undefined);
    });

    void module.downloadUpdateNow();
    await flushMicrotasks();
    updaterEvents.get("download-progress")?.({ percent: 12 });
    await vi.advanceTimersByTimeAsync(2 * 60 * 1000);

    // electron-updater surfaces our own cancellation as an error; the retry
    // wording already on screen is more useful than "cancelled".
    updaterEvents.get("error")?.(new Error("Cancelled"));
    expect(statusMessages().at(-1)?.payload).toMatchObject({
      message: "Download stopped responding. Try again.",
    });
  });

  it("stamps the staged build onto every status, including transient ones", async () => {
    vi.useFakeTimers();
    const stagedAt = new Date("2026-07-17T12:00:00.000Z").getTime();
    vi.setSystemTime(stagedAt);
    const { module, updaterEvents, statusMessages } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("checking-for-update")?.();

    expect(statusMessages().at(-1)?.payload).toMatchObject({
      state: "checking",
      staged: { version: "2.1.0", stagedAt, escalated: false },
    });
  });

  // Regression: staging is not reversible. A completed download hands the build
  // to Squirrel and the resulting ShipIt waits for the app to exit, so clearing
  // autoInstallOnAppQuit afterwards does not disarm it. Switching nightly ->
  // stable used to install the NIGHTLY on the next quit while Settings said
  // "Restart to switch to Stable". The only way out is to stage the right build
  // over it, so the replacement download is forced.
  it("forces the replacement download when a channel switch strands a staged build", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });
    expect(module.getUpdateStatus().state).toBe("downloaded");

    const autoDownloadAtCheck: boolean[] = [];
    autoUpdater.checkForUpdates.mockImplementation(() => {
      autoDownloadAtCheck.push(autoUpdater.autoDownload);
      return Promise.resolve({
        isUpdateAvailable: true,
        updateInfo: { version: "2.0.0" },
      });
    });

    await module.checkForUpdatesNow(stateDir, {
      settings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
      requestId: "channel-update-1",
    });

    // Downloaded even though automatic updates are off.
    expect(autoDownloadAtCheck).toEqual([true]);
    // And the stranded build stops being advertised as ready to install.
    expect(module.getUpdateStatus().state).not.toBe("downloaded");
    expect(module.getUpdateStatus().staged).toBeDefined();
  });

  it("leaves auto-download off for a manual check on the staged build's own channel", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });

    const autoDownloadAtCheck: boolean[] = [];
    autoUpdater.checkForUpdates.mockImplementation(() => {
      autoDownloadAtCheck.push(autoUpdater.autoDownload);
      return Promise.resolve({ isUpdateAvailable: false, updateInfo: { version: "2.1.0-nightly.1" } });
    });

    await module.checkForUpdatesNow(stateDir, {
      settings: { enabled: false, channel: "nightly", nightlyAck: true, feature: null },
      requestId: "manual-update-1",
    });

    expect(autoDownloadAtCheck).toEqual([false]);
    // Nothing was stranded, so the staged build survives the re-check.
    expect(module.getUpdateStatus().state).toBe("downloaded");
  });

  it("supersedes a pinned PR build when returning to the home channel", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater({
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: { pr: 4729 },
    });

    await module.checkForUpdatesNow(stateDir);
    expect(autoUpdater.channel).toBe("pr4729");
    updaterEvents.get("update-downloaded")?.({ version: "2.0.0-pr4729.1" });

    const autoDownloadAtCheck: boolean[] = [];
    autoUpdater.checkForUpdates.mockImplementation(() => {
      autoDownloadAtCheck.push(autoUpdater.autoDownload);
      return Promise.resolve({ isUpdateAvailable: true, updateInfo: { version: "2.0.0" } });
    });

    await module.returnToHome(stateDir, "feature-update-1");

    expect(autoUpdater.channel).toBe("latest");
    expect(autoDownloadAtCheck).toEqual([true]);
    expect(module.getUpdateStatus().staged).toBeDefined();
  });

  it("keeps A provenance while blocking restart into a stranded build", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, updaterEvents } = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });
    const armed = setIntervalSpy.mock.results.length;
    expect(armed).toBeGreaterThan(0);

    await module.checkForUpdatesNow(stateDir, {
      settings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
    });

    // The discarded build must not keep an escalation loop alive nudging the
    // user to restart into a channel they left.
    expect(module.getUpdateStatus().staged).toBeDefined();
    module.quitAndInstallUpdate();
    expect(module.getUpdateStatus().installDisabledReason).toBeDefined();
  });
});

describe("returnToHome", () => {
  // stateDir comes from the per-test beforeEach above.

  it("clears only the feature pin, preserves home channel/prefs, and checks", async () => {
    const { module, autoUpdater, updateUpdateSettings } =
      await importAutoUpdater({
        enabled: true,
        channel: "nightly",
        nightlyAck: true,
        feature: { pr: 2270 },
      });

    await module.returnToHome(stateDir, "req-1");

    // The pin is cleared via a single read-modify-write; applying that updater
    // preserves every field except feature.
    expect(updateUpdateSettings).toHaveBeenCalledTimes(1);
    const clear = updateUpdateSettings.mock.calls[0]?.[1] as (
      c: UpdateSettings,
    ) => UpdateSettings;
    expect(
      clear({
        enabled: true,
        channel: "nightly",
        nightlyAck: true,
        feature: { pr: 2270 },
      }),
    ).toEqual({
      enabled: true,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });
    // The feed resolves the home channel (not the pr<N> feed) and a check runs.
    expect(autoUpdater.channel).toBe("nightly");
    expect(autoUpdater.allowPrerelease).toBe(true);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("checks the home channel even when nothing is pinned", async () => {
    const { module, autoUpdater, updateUpdateSettings } =
      await importAutoUpdater({
        enabled: true,
        channel: "latest",
        nightlyAck: false,
        feature: null,
      });

    await module.returnToHome(stateDir);

    const clear = updateUpdateSettings.mock.calls[0]?.[1] as (
      c: UpdateSettings,
    ) => UpdateSettings;
    const current: UpdateSettings = {
      enabled: true,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    };
    expect(clear(current)).toBe(current); // no pin -> unchanged
    expect(autoUpdater.channel).toBe("latest");
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("reports unsupported in dev (unpackaged) without touching settings", async () => {
    const { module, autoUpdater, updateUpdateSettings } =
      await importAutoUpdater(
        {
          enabled: true,
          channel: "latest",
          nightlyAck: false,
          feature: { pr: 2270 },
        },
        { isPackaged: false },
      );

    await module.returnToHome(stateDir, "req-2");

    expect(updateUpdateSettings).not.toHaveBeenCalled();
    expect(autoUpdater.checkForUpdates).not.toHaveBeenCalled();
  });
});

// stubProcess swaps process.platform/execPath for one test. The install
// preflight reads both at call time, so no module re-import is needed after
// the swap, but the restore MUST run even when the assertion throws.
function stubProcess(platform: NodeJS.Platform, execPath: string): () => void {
  const originalPlatform = Object.getOwnPropertyDescriptor(process, "platform")!;
  const originalExecPath = Object.getOwnPropertyDescriptor(process, "execPath")!;
  Object.defineProperty(process, "platform", { value: platform });
  Object.defineProperty(process, "execPath", { value: execPath });
  return () => {
    Object.defineProperty(process, "platform", originalPlatform);
    Object.defineProperty(process, "execPath", originalExecPath);
  };
}

// Builds a real bundle-shaped tree so the writability checks run against the
// filesystem rather than a stub. Returns the exec path inside it.
function makeBundle(): { root: string; bundle: string; execPath: string } {
  const root = mkdtempSync(nodePath.join(os.tmpdir(), "ao-updater-perm-"));
  const bundle = nodePath.join(root, "Agent Orchestrator.app");
  mkdirSync(nodePath.join(bundle, "Contents", "MacOS"), { recursive: true });
  return {
    root,
    bundle,
    execPath: nodePath.join(bundle, "Contents", "MacOS", "agent-orchestrator"),
  };
}

const TRANSLOCATED_EXEC_PATH =
  "/private/var/folders/hg/vkmz93d1T/T/AppTranslocation/0AC4-11EE/d/Agent Orchestrator.app/Contents/MacOS/agent-orchestrator";

describe("quitAndInstallUpdate", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.resetModules();
  });

  it("shows an actionable dialog instead of installing when running translocated on macOS", async () => {
    const restore = stubProcess("darwin", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(dialog.showMessageBox).toHaveBeenCalledTimes(1);
      const box = dialog.showMessageBox.mock.calls[0][0] as {
        message: string;
        detail: string;
      };
      expect(box.detail).toContain("/Applications");
    } finally {
      restore();
    }
  });

  it("shows the dialog when the app bundle is not writable", async () => {
    const { root, bundle, execPath } = makeBundle();
    chmodSync(bundle, 0o555);
    const restore = stubProcess("darwin", execPath);
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(dialog.showMessageBox).toHaveBeenCalledTimes(1);
    } finally {
      restore();
      chmodSync(bundle, 0o755);
      rmSync(root, { recursive: true, force: true });
    }
  });

  // The correction to #3529's check: ShipIt swaps by moving the bundle aside
  // and moving the new one in, so a writable bundle inside an unwritable
  // PARENT still fails, silently, exactly like the case above.
  it("shows the dialog when the enclosing directory is not writable", async () => {
    const { root, bundle, execPath } = makeBundle();
    chmodSync(root, 0o555);
    const restore = stubProcess("darwin", execPath);
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(dialog.showMessageBox).toHaveBeenCalledTimes(1);
      const box = dialog.showMessageBox.mock.calls[0][0] as { detail: string };
      expect(box.detail).toContain(root);
      // Must NOT tell a user already sitting in /Applications to move there.
      expect(box.detail).not.toContain("move Agent Orchestrator.app into /Applications");
    } finally {
      restore();
      chmodSync(root, 0o755);
      chmodSync(bundle, 0o755);
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("installs when both the bundle and its parent are writable", async () => {
    const { root, execPath } = makeBundle();
    const restore = stubProcess("darwin", execPath);
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(dialog.showMessageBox).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledWith(false, true);
    } finally {
      restore();
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("fails open and installs when the derived bundle path does not exist", async () => {
    const restore = stubProcess(
      "darwin",
      "/nonexistent-ao-test/Agent Orchestrator.app/Contents/MacOS/agent-orchestrator",
    );
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(dialog.showMessageBox).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledWith(false, true);
    } finally {
      restore();
    }
  });

  // Under `npm start` and in tests execPath is a bare node/electron binary, so
  // the derived path is an unrelated ancestor dir. Its permissions must not
  // decide anything.
  it("fails open when execPath is not inside a .app bundle", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(dialog.showMessageBox).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledWith(false, true);
    } finally {
      restore();
    }
  });

  it("never blocks off macOS, even for translocation-looking paths", async () => {
    const restore = stubProcess("win32", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater();

      module.quitAndInstallUpdate();

      expect(dialog.showMessageBox).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledWith(false, true);
    } finally {
      restore();
    }
  });

  it("does nothing when the app is not packaged", async () => {
    const restore = stubProcess("darwin", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater, dialog } = await importAutoUpdater({
        enabled: true,
        channel: "latest",
        nightlyAck: false,
        feature: null,
      }, { isPackaged: false });

      module.quitAndInstallUpdate();

      expect(dialog.showMessageBox).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  it("blocks AO-triggered restart while a replacement is incomplete", async () => {
    const stateDir = mkdtempSync(nodePath.join(os.tmpdir(), "ao-replacement-install-"));
    try {
      const { module, autoUpdater, dialog, updaterEvents } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "1.0.1" });
      updaterEvents.get("update-available")?.({ version: "1.0.2" });

      module.quitAndInstallUpdate();

      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(dialog.showMessageBox).toHaveBeenCalledWith(expect.objectContaining({
        detail: expect.stringContaining("Quitting outside AO may still install 1.0.1"),
      }));
    } finally {
      rmSync(stateDir, { recursive: true, force: true });
    }
  });
});

// The other half of #3527: the button was guarded but install-on-quit was not,
// so quitting stayed a silent dead end. Every check path must reflect the same
// verdict the button does.
describe("install-on-quit policy", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.resetModules();
  });

  it("disables install-on-quit when the location cannot be installed to", async () => {
    const restore = stubProcess("darwin", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater } = await importAutoUpdater();

      await module.startAutoUpdates("/tmp/ao-state");

      expect(autoUpdater.autoInstallOnAppQuit).toBe(false);
    } finally {
      restore();
    }
  });

  // Regression: dropping a stale staged build stopped AO advertising it, but
  // the build itself stayed in the cache with install-on-quit armed. On Windows
  // and Linux BaseUpdater.addQuitHandler re-reads autoInstallOnAppQuit at quit
  // time, so quitting before the replacement landed installed the channel the
  // user had just left.
  it("disables install-on-quit while a stale staged build awaits replacement", async () => {
    const restore = stubProcess("win32", process.execPath);
    try {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });
    expect(autoUpdater.autoInstallOnAppQuit).toBe(true);

    await module.checkForUpdatesNow(stateDir, {
      settings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
    });

    expect(autoUpdater.autoInstallOnAppQuit).toBe(false);
    } finally { restore(); }
  });

  it("re-enables install-on-quit once the replacement is staged", async () => {
    const restore = stubProcess("win32", process.execPath);
    try {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater({
      enabled: false,
      channel: "nightly",
      nightlyAck: true,
      feature: null,
    });

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });
    await module.checkForUpdatesNow(stateDir, {
      settings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
    });
    expect(autoUpdater.autoInstallOnAppQuit).toBe(false);

    updaterEvents.get("update-downloaded")?.({ version: "2.0.0" });
    expect(autoUpdater.autoInstallOnAppQuit).toBe(true);
    } finally { restore(); }
  });

  it("keeps install-on-quit off for an uninstallable location even once replaced", async () => {
    const restore = stubProcess("darwin", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater, updaterEvents } = await importAutoUpdater({
        enabled: false,
        channel: "nightly",
        nightlyAck: true,
        feature: null,
      });

      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0-nightly.1" });
      await module.checkForUpdatesNow(stateDir, {
        settings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
      });
      updaterEvents.get("update-downloaded")?.({ version: "2.0.0" });

      // The location blocker outranks the replacement: nothing installs from a
      // translocated bundle whatever is staged.
      expect(autoUpdater.autoInstallOnAppQuit).toBe(false);
    } finally {
      restore();
    }
  });

  it("leaves install-on-quit on for an installable location", async () => {
    const { root, execPath } = makeBundle();
    const restore = stubProcess("darwin", execPath);
    try {
      const { module, autoUpdater } = await importAutoUpdater();

      await module.startAutoUpdates("/tmp/ao-state");

      expect(autoUpdater.autoInstallOnAppQuit).toBe(true);
    } finally {
      restore();
      rmSync(root, { recursive: true, force: true });
    }
  });
});


describe("PR 4905 upstream integration", () => {
  afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });
  it("retains A on Windows until the handoff promise settles", async () => {
    const restore = stubProcess("win32", "/usr/bin/node");
    try {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    const handoff = deferred();
    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("update-available")?.({ version: "2.2.0" });
      return Promise.resolve({ isUpdateAvailable: true, updateInfo: { version: "2.2.0" }, downloadPromise: handoff.promise });
    });
    const run = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(module.getUpdateStatus().state).toBe("replacing"));
    updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
    expect(module.getUpdateStatus()).toMatchObject({ state: "replacing", stagedCandidate: { version: "2.1.0" }, replacementCandidate: { version: "2.2.0" } });
    module.quitAndInstallUpdate();
    expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    handoff.resolve();
    await run;
    expect(module.getUpdateStatus()).toMatchObject({ state: "downloaded", version: "2.2.0" });
    } finally { restore(); }
  });
  it("retains A if macOS resolves B download without performing native handoff", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      const handoff = deferred();
      autoUpdater.checkForUpdates.mockImplementationOnce(() => {
        updaterEvents.get("update-available")?.({ version: "2.2.0" });
        return Promise.resolve({ downloadPromise: handoff.promise });
      });
      const run = module.startAutoUpdates(stateDir);
      await vi.waitFor(() => expect(module.getUpdateStatus().state).toBe("replacing"));
      autoUpdater.autoInstallOnAppQuit = false;
      updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
      handoff.resolve();
      await run;
      expect(module.getUpdateStatus()).toMatchObject({ state: "replacement-failed", stagedCandidate: { version: "2.1.0" }, replacementCandidate: { version: "2.2.0" } });
    } finally { restore(); }
  });
  it("retains failed replacement identity when a subsequent check finds nothing", async () => {
    const { module, updaterEvents } = await importAutoUpdater();
    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("update-available")?.({ version: "2.2.0" });
    updaterEvents.get("error")?.(new Error("offline"));
    updaterEvents.get("update-not-available")?.();
    expect(module.getUpdateStatus()).toMatchObject({ state: "replacement-failed", stagedCandidate: { version: "2.1.0" }, replacementCandidate: { version: "2.2.0" } });
    await module.setUpdateSettings(stateDir, { enabled: false, channel: "latest", nightlyAck: false, feature: null });
  });
});


describe("PR 4905 review boundaries", () => {
let restore: () => void;
beforeEach(() => { restore = stubProcess("darwin", "/usr/bin/node"); });
afterEach(() => { restore(); vi.restoreAllMocks(); });
const settings = { enabled: true, channel: "latest" as const, nightlyAck: false, feature: null };
async function stageA() {
  const h = await importAutoUpdater();
  await h.module.checkForUpdatesNow(stateDir);
  h.updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
  await h.module.setUpdateSettings(stateDir, settings);
  return h;
}
function disk() { return JSON.parse(readFileSync(nodePath.join(stateDir, "staged-update-journal.json"), "utf8")); }
it("retains A and blocks install after a late native failure", async () => {
  const h = await stageA();
  h.autoUpdater.checkForUpdates.mockImplementationOnce(() => {
    h.updaterEvents.get("update-available")?.({ version: "2.2.0" });
    h.updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
    return Promise.resolve({ downloadPromise: Promise.resolve() });
  });
  await h.module.checkForUpdatesNow(stateDir);
  expect(disk().staged.version).toBe("2.1.0");
  h.updaterEvents.get("error")?.(new Error("Squirrel signature validation failed"));
  await h.module.setUpdateSettings(stateDir, settings);
  expect(disk().staged.version).toBe("2.1.0");
  expect(h.module.getUpdateStatus()).toMatchObject({state:"replacement-failed",stagedCandidate:{version:"2.1.0"},replacementCandidate:{version:"2.2.0"}});
  h.module.quitAndInstallUpdate();
  expect(h.autoUpdater.quitAndInstall).not.toHaveBeenCalled();
});
it("keeps queued journal recovery coherent after one failed write", async () => {
  const h = await stageA();
  const { StagedUpdateJournalStore } = await import("./staged-update-journal");
  vi.spyOn(StagedUpdateJournalStore.prototype, "write").mockRejectedValueOnce(new Error("ENOSPC"));
  h.updaterEvents.get("update-available")?.({ version: "2.2.0" });
  await h.module.setUpdateSettings(stateDir, settings);
  expect(disk().state).toBe("replacing");
  expect(disk().replacement.version).toBe("2.2.0");
  h.module.quitAndInstallUpdate();
  expect(h.autoUpdater.quitAndInstall).not.toHaveBeenCalled();
});
it("respects download opt-out for same-channel discovery", async () => {
  const h = await stageA();
  h.autoUpdater.checkForUpdates.mockImplementationOnce(() => {
    h.updaterEvents.get("update-available")?.({ version: "2.2.0" });
    return Promise.resolve({ isUpdateAvailable:true, updateInfo:{ version:"2.2.0" } });
  });
  await h.module.checkForUpdatesNow(stateDir, {settings:{...settings, enabled:false}});
  expect(h.autoUpdater.autoDownload).toBe(false);
  expect(h.autoUpdater.downloadUpdate).not.toHaveBeenCalled();
  await h.module.downloadUpdateNow();
  expect(h.autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
});
it("abandons B before native handoff when the selected channel returns A", async () => {
  const h = await stageA();
  h.autoUpdater.channel = "nightly";
  h.updaterEvents.get("update-available")?.({version:"2.2.0"});
  h.updaterEvents.get("error")?.(new Error("offline"));
  await h.module.setUpdateSettings(stateDir, settings);
  h.autoUpdater.checkForUpdates.mockImplementationOnce(() => {
    h.updaterEvents.get("update-available")?.({version:"2.1.0"});
    return Promise.resolve({isUpdateAvailable:true, updateInfo:{version:"2.1.0"}});
  });
  await h.module.checkForUpdatesNow(stateDir);
  expect(h.module.getUpdateStatus().state).toBe("downloaded");
  expect(h.module.getUpdateStatus().replacementCandidate).toBeUndefined();
  expect(disk().state).toBe("native-possibly-staged");
});

it("keeps A when real MacUpdater delivers its ZIP before native rejection", async () => {
  const { createRequire } = await import("node:module");
  const { EventEmitter } = await import("node:events");
  const { MacUpdater } = createRequire(import.meta.url)("electron-updater/out/MacUpdater.js");
  const h = await stageA();
  const zip = nodePath.join(stateDir, "b.zip");
  writeFileSync(zip, "zip payload");
  const native = new EventEmitter() as any;
  const fetched = deferred();
  native.setFeedURL = (feed:any) => { native.feed = feed; };
  native.checkForUpdates = () => {
    void (async () => {
      const response = await fetch(native.feed.url, {headers:native.feed.headers});
      const manifest = await response.json() as any;
      await (await fetch(manifest.url)).arrayBuffer();
      fetched.resolve();
    })();
  };
  native.on("error", (error:Error) => h.updaterEvents.get("error")?.(error));
  const updater:any = {nativeUpdater:native, autoInstallOnAppQuit:true, squirrelDownloadedUpdate:true,
    _logger:{info:()=>{},warn:()=>{}}, debug:()=>{},
    closeServerIfExists:MacUpdater.prototype.closeServerIfExists,
    dispatchUpdateDownloaded:(info:any)=>h.updaterEvents.get("update-downloaded")?.(info)};
  h.autoUpdater.checkForUpdates.mockImplementationOnce(() => {
    h.updaterEvents.get("update-available")?.({version:"2.2.0"});
    const promise = MacUpdater.prototype.updateDownloaded.call(updater,
      {url:new URL("https://example.invalid/b.zip"), info:{size:11}},
      {version:"2.2.0",downloadedFile:zip});
    return Promise.resolve({downloadPromise:promise});
  });
  try {
    await h.module.checkForUpdatesNow(stateDir);
    await fetched.promise;
    expect(disk().staged.version).toBe("2.1.0");
    expect(native.listenerCount("error")).toBe(1);
    native.emit("error", new Error("native validation rejected B after transfer"));
    expect(disk().staged.version).toBe("2.1.0");
    h.module.quitAndInstallUpdate();
    expect(h.autoUpdater.quitAndInstall).not.toHaveBeenCalled();
  } finally { updater.closeServerIfExists(); }
});

it("keeps replacing identity through a no-update broadcast", async () => {
  const h = await stageA();
  h.updaterEvents.get("update-available")?.({version:"2.2.0"});
  await h.module.setUpdateSettings(stateDir, settings);
  expect(disk().state).toBe("replacing");
  h.updaterEvents.get("update-not-available")?.();
  expect(h.module.getUpdateStatus()).toMatchObject({state:"replacing",version:"2.2.0",stagedCandidate:{version:"2.1.0"},replacementCandidate:{version:"2.2.0"}});
  await h.module.setUpdateSettings(stateDir, settings);
});

it("keeps installation blocked if the latest queued snapshot fails", async () => {
  const h = await stageA();
  const { StagedUpdateJournalStore } = await import("./staged-update-journal");
  const write = StagedUpdateJournalStore.prototype.write;
  vi.spyOn(StagedUpdateJournalStore.prototype, "write")
    .mockImplementationOnce(write)
    .mockRejectedValueOnce(new Error("ENOSPC"));
  h.updaterEvents.get("update-available")?.({ version: "2.2.0" });
  await h.module.setUpdateSettings(stateDir, settings);
  expect(h.module.getUpdateStatus()).toMatchObject({ state: "replacing", installDisabledReason: "ENOSPC" });
  h.module.quitAndInstallUpdate();
  expect(h.autoUpdater.quitAndInstall).not.toHaveBeenCalled();
});

it("keeps automatic same-channel discovery opted out of downloading", async () => {
  const disabled = { ...settings, enabled: false };
  const h = await importAutoUpdater(disabled);
  await h.module.checkForUpdatesNow(stateDir);
  h.updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
  await h.module.setUpdateSettings(stateDir, disabled);
  h.autoUpdater.checkForUpdates.mockImplementationOnce(() => {
    h.updaterEvents.get("update-available")?.({ version: "2.2.0" });
    return Promise.resolve({ updateInfo: { version: "2.2.0" } });
  });
  await h.module.startAutoUpdates(stateDir);
  expect(h.autoUpdater.downloadUpdate).not.toHaveBeenCalled();
  expect(h.module.getUpdateStatus()).toMatchObject({ state: "replacing", replacementPhase: "checking" });
});

it("retargets A and retains uncertain native B across channel return and retry", async () => {
  const h = await stageA();
  h.autoUpdater.channel = "nightly";
  h.updaterEvents.get("update-available")?.({ version: "2.2.0" });
  h.updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
  await h.module.setUpdateSettings(stateDir, settings);
  h.autoUpdater.checkForUpdates.mockImplementationOnce(() => {
    h.updaterEvents.get("update-available")?.({ version: "2.1.0" });
    return Promise.resolve({ updateInfo: { version: "2.1.0" } });
  });
  await h.module.checkForUpdatesNow(stateDir);
  expect(disk()).toMatchObject({ state: "replacing", replacement: { version: "2.1.0", channel: "latest" }, nativeCandidates: [{ version: "2.2.0", channel: "nightly" }] });
  h.autoUpdater.downloadUpdate.mockImplementationOnce(() => {
    h.updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    return Promise.resolve();
  });
  await h.module.downloadUpdateNow();
  expect(disk()).toMatchObject({ state: "replacing", phase: "native-handoff", replacement: { version: "2.1.0" }, nativeCandidates: [{ version: "2.2.0" }] });
  h.module.quitAndInstallUpdate();
  expect(h.autoUpdater.quitAndInstall).not.toHaveBeenCalled();
});

});
