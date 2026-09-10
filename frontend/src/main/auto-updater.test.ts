// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import { EventEmitter } from "node:events";
import semver from "semver";
import { CancellationToken } from "builder-util-runtime";
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
  nativeReadyManually?: boolean;
  reconcileFeaturePin?: (
    settings: UpdateSettings,
  ) => Promise<{ settings: UpdateSettings; cleared: boolean }>;
  isPackaged?: boolean;
  version?: string;
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
  httpExecutor: { request: ReturnType<typeof vi.fn<(options: object, token?: CancellationToken) => Promise<string | null>>> };
  // electron-updater keeps its cached pending download behind this protected
  // member; the install-rejection path clears it through the same name.
  downloadedUpdateHelper: { clear: ReturnType<typeof vi.fn> };
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
    httpExecutor: { request: vi.fn(async () => null) },
    downloadedUpdateHelper: { clear: vi.fn(() => Promise.resolve()) },
  };
}

// The module persists staged provenance beside the update settings, and that
// write is fire-and-forget: on a shared state dir a write from one test could
// land after the next test's cleanup. One fresh directory per test removes the
// race outright rather than trying to time it.
let stateDir = "";
const hostPlatform = Object.getOwnPropertyDescriptor(process, "platform")!;
beforeEach(() => {
  Object.defineProperty(process, "platform", { value: "linux" });
  stateDir = mkdtempSync(nodePath.join(os.tmpdir(), "ao-updater-state-"));
});
afterEach(() => {
  Object.defineProperty(process, "platform", hostPlatform);
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
  const nativeUpdaterEvents = new Map<string, UpdaterEventHandler>();
  const nativeAutoUpdater = new EventEmitter();
  // Record each native handler so a test can fire it directly with explicit
  // args (nativeUpdaterEvents.get), while emit()/listenerCount() still work for
  // the macOS restart tests.
  const nativeEmitterOn = nativeAutoUpdater.on.bind(nativeAutoUpdater);
  (nativeAutoUpdater as unknown as { on: (event: string, handler: UpdaterEventHandler) => unknown }).on = (
    event,
    handler,
  ) => {
    nativeUpdaterEvents.set(event, handler);
    return nativeEmitterOn(event, handler);
  };
  const startMacUpdateProgress = vi.fn(async () => ({ assertAlive: vi.fn(), fail: vi.fn(async () => undefined) }));
  vi.doMock("./mac-update-progress", () => ({ startMacUpdateProgress }));
  autoUpdater.on.mockImplementation(
    (event: string, handler: UpdaterEventHandler) => {
      updaterEvents.set(event, (...args: any[]) => {
        handler(...args);
        if (event === "update-downloaded" && !options.nativeReadyManually) nativeAutoUpdater.emit("update-downloaded");
      });
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
  // The renderer is reached through the injected shell sink, never by walking
  // BrowserWindow.getAllWindows(): the AO shell is a BaseWindow hosting a
  // WebContentsView (#3750), so that registry is empty in the real app and
  // enumerating it silently dropped every push. Keeping the mock registry empty
  // here means every test in this file exercises the real delivery path.
  const rendererSend = vi.fn((channel: string, payload: unknown) => {
    sent.push({ channel, payload });
  });
  const BrowserWindow = {
    getAllWindows: vi.fn(() => [] as unknown[]),
  };
  const statusMessages = () => sent.filter((m) => m.channel === "updates:status");
  const telemetryMessages = () => sent.filter((m) => m.channel === "updates:telemetry");
  vi.doMock("electron-updater", () => ({ autoUpdater }));
  vi.doMock("electron", () => ({
    autoUpdater: nativeAutoUpdater,
    app: {
      isPackaged: options.isPackaged ?? true,
      getVersion: () => options.version ?? "1.0.0",
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
  module.setRendererSink(() => ({ send: rendererSend }));
  return {
    nativeAutoUpdater,
    startMacUpdateProgress,
    sent,
    rendererSend,
    statusMessages,
    telemetryMessages,
    module,
    autoUpdater,
    dialog,
    BrowserWindow,
    updaterEvents,
    nativeUpdaterEvents,
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
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("recovers a macOS ShipIt missing-file failure instead of retaining a ready update", async () => {
    const restore = stubProcess("darwin", "/usr/local/bin/node");
    try {
      const { module, autoUpdater, updaterEvents, nativeAutoUpdater } = await importAutoUpdater({ enabled: false, channel: "latest", nightlyAck: false, feature: null }, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      await flushMicrotasks();
      const failure = new Error(
        "ditto: Could not lstat /Users/test/Library/Caches/dev.agent-orchestrator.desktop.ShipIt/update.abc/Agent Orchestrator.app/Contents/Resources/acp-runtime/node_modules/.bin/node-which: No such file or directory",
      );
      updaterEvents.get("error")?.(failure);
      nativeAutoUpdater.emit("error", failure);
      expect(module.getUpdateStatus()).toMatchObject({ state: "error", message: expect.stringContaining("prepare the update") });
      expect(module.getUpdateStatus().staged).toBeUndefined();
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Check for updates/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      // A check and an explicit download can retry the SAME release, without
      // touching ShipIt's files or deleting a cache concurrently with a download.
      autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: "2.1.0" } });
      await module.checkForUpdatesNow(stateDir);
      expect(module.getUpdateStatus().state).toBe("available");
      autoUpdater.downloadUpdate.mockImplementation(async () => {
        updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      });
      const retry = module.downloadUpdateNow();
      await flushMicrotasks();
      // A retry is not installable until the native updater finishes preparing it.
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      nativeAutoUpdater.emit("update-downloaded");
      await retry;
      expect(module.getUpdateStatus().staged?.version).toBe("2.1.0");
      await module.quitAndInstallUpdate();
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledTimes(1);
    } finally {
      restore();
    }
  });

  it("keeps native preparation failures visible during an automatic check and retries next time", async () => {
    const restore = stubProcess("darwin", "/usr/local/bin/node");
    try {
      const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      const error = new Error("ditto: /cache/app.ShipIt/update.abc/AO.app/Contents/Resources/._app.asar__: No such file or directory");
      autoUpdater.checkForUpdates.mockImplementationOnce(async () => {
        updaterEvents.get("checking-for-update")?.();
        updaterEvents.get("error")?.(error);
        throw error;
      });
      await module.startAutoUpdates(stateDir);
      expect(module.getUpdateStatus()).toMatchObject({ state: "error", message: expect.stringContaining("prepare the update") });
      expect(module.getUpdateStatus().staged).toBeUndefined();
      await new Promise((resolve) => setTimeout(resolve, 50));
      const second = await importAutoUpdaterKeepingStagedFile();
      await second.module.startAutoUpdates(stateDir);
      expect(second.module.getUpdateStatus().staged).toBeUndefined();
      expect(second.autoUpdater.autoDownload).toBe(true);
    } finally {
      restore();
    }
  });

  it("handles a reject-only native failure before AO has recorded a staged build", async () => {
    const restore = stubProcess("darwin", "/usr/local/bin/node");
    try {
      const { module, autoUpdater } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      autoUpdater.downloadUpdate.mockRejectedValue(new Error(
        "ditto: /cache/app.ShipIt/update.abc/AO.app/Contents/Resources/._app.asar__: No such file or directory",
      ));
      await module.downloadUpdateNow("failed-download");
      expect(module.getUpdateStatus()).toMatchObject({ state: "error", requestId: "failed-download", message: expect.stringContaining("prepare the update") });
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Check for updates/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  it("does not let an in-flight escalation check restore a failed installation", async () => {
    const restore = stubProcess("darwin", "/usr/local/bin/node");
    try {
      const { module, updaterEvents, readUpdateSettings } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      const pendingSettings = deferred<UpdateSettings>();
      readUpdateSettings.mockReturnValueOnce(pendingSettings.promise);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      updaterEvents.get("error")?.(new Error(
        "ditto: /cache/app.ShipIt/update.abc/AO.app/Contents/Resources/._app.asar__: No such file or directory",
      ));
      pendingSettings.resolve({ enabled: true, channel: "latest", nightlyAck: false, feature: null });
      await flushMicrotasks();
      expect(module.getUpdateStatus().state).toBe("error");
      expect(module.getUpdateStatus().staged).toBeUndefined();
    } finally {
      restore();
    }
  });

  it("preserves replacement provenance when staging immediately follows a failure", async () => {
    const restore = stubProcess("darwin", "/usr/local/bin/node");
    try {
      const { module, updaterEvents } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      updaterEvents.get("error")?.(new Error(
        "ditto: /cache/app.ShipIt/update.abc/AO.app/Contents/Resources/._app.asar__: No such file or directory",
      ));
      updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
      await new Promise((resolve) => setTimeout(resolve, 50));
      const second = await importAutoUpdaterKeepingStagedFile();
      await second.module.startAutoUpdates(stateDir);
      expect(second.module.getUpdateStatus().staged?.version).toBe("2.2.0");
    } finally {
      restore();
    }
  });

  it.each(["download", "check", "return-home"])("allows retry after the previous %s rejects", async (operation) => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      const failure = new Error("ditto: /cache/app.ShipIt/update.abc/AO.app/Contents/Resources/._app.asar__: No such file or directory");
      autoUpdater.downloadUpdate.mockImplementation(async () => {
        updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
      });
      if (operation === "download") autoUpdater.downloadUpdate.mockRejectedValueOnce(failure);
      else autoUpdater.checkForUpdates.mockRejectedValueOnce(failure);
      const failed = operation === "download" ? module.downloadUpdateNow("failed")
        : operation === "check" ? module.checkForUpdatesNow(stateDir, { requestId: "failed" })
        : module.returnToHome(stateDir, "failed");
      await failed;
      await module.downloadUpdateNow("retry");
      expect(module.getUpdateStatus().staged?.version).toBe("2.2.0");
      expect(autoUpdater.autoInstallOnAppQuit).toBe(true);
    } finally { restore(); }
  });

  it("does not invalidate a staged update for an unrelated missing file", async () => {
    const { module, updaterEvents } = await importAutoUpdater();
    await module.startAutoUpdates(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    await flushMicrotasks();
    updaterEvents.get("error")?.(new Error("ENOENT: no such file or directory, open /tmp/download.zip"));
    expect(module.getUpdateStatus().staged?.version).toBe("2.1.0");
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

  it("revalidates release history without a second body and refreshes when a release changes", async () => {
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
        { status: 200, headers: { etag: '"release-v1"' } },
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
      fetchMock.mockResolvedValueOnce(new Response(null, { status: 304 }));
      await module.checkForUpdatesNow(stateDir);
      expect(fetchMock).toHaveBeenLastCalledWith(
        expect.any(String),
        expect.objectContaining({ headers: expect.objectContaining({ "If-None-Match": '"release-v1"' }) }),
      );
      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(3, expect.objectContaining({
        url: "https://github.com/Untrivial-ai/agent-orchestrator/releases/download/v1.0.1-nightly.202608231517",
      }));
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([{
        tag_name: "v1.0.2-nightly.202608241000", draft: false, prerelease: true,
        assets: [{ name: platformManifest }],
      }]), { status: 200, headers: { etag: '"release-v2"' } }));
      await module.checkForUpdatesNow(stateDir);
      expect(autoUpdater.setFeedURL).toHaveBeenNthCalledWith(5, expect.objectContaining({
        url: "https://github.com/Untrivial-ai/agent-orchestrator/releases/download/v1.0.2-nightly.202608241000",
      }));
      expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(3);
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
    const { module, autoUpdater, dialog, statusMessages } =
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
    expect(statusMessages()).toHaveLength(0);

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
    expect(module.getUpdateStatus()).toMatchObject({ state: "idle" });
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
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
      state: "available",
      version: "2.0.0",
      checkedAt: expect.any(Number),
    });
  });

  it("reports an automatic download failure after progress", async () => {
    vi
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

    expect(module.getUpdateStatus()).toMatchObject({ state: "error", message: "download failed" });

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
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
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

    // The state stays truthful — the suppressed failure never replaces it —
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
    await Promise.resolve();
    await Promise.resolve();

    vi.setSystemTime(stagedAt + 49 * 60 * 60 * 1000);
    runEscalation();
    await Promise.resolve();
    await Promise.resolve();
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
      state: "downloaded",
      version: "2.1.0",
      stagedAt,
      escalated: true,
      checkedAt: expect.any(Number),
      staged: { version: "2.1.0", stagedAt, escalated: true },
    });
  });

  it("reports the download failure and retains valid staged metadata", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const stagedAt = new Date("2026-07-17T12:00:00.000Z").getTime();
    vi.setSystemTime(stagedAt);
    const automaticDownload = deferred();
    vi
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
    await Promise.resolve();
    await Promise.resolve();

    vi.setSystemTime(stagedAt + 49 * 60 * 60 * 1000);
    runEscalation();
    await Promise.resolve();
    await Promise.resolve();
    updaterEvents.get("update-available")?.({ version: "2.2.0" });
    updaterEvents.get("download-progress")?.({ percent: 64 });
    expect(module.getUpdateStatus()).toEqual({
      state: "downloading",
      version: "2.2.0",
      percent: 64,
      checkedAt: expect.any(Number),
      // The staged 2.1.0 is still on disk while 2.2.0 downloads, so every
      // status carries it and the sidebar keeps an actionable row throughout.
      staged: { version: "2.1.0", stagedAt, escalated: true },
    });

    updaterEvents.get("error")?.(err);
    automaticDownload.resolve();
    await startPromise;

    expect(module.getUpdateStatus()).toMatchObject({ state: "error", message: "download failed" });
    expect(module.getUpdateStatus().staged).toEqual({ version: "2.1.0", stagedAt, escalated: true });

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
    await Promise.resolve();
    await Promise.resolve();
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
    const { module, rendererSend, updaterEvents } = await importAutoUpdater();
    const err = new Error("manual feed failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(err);

    expect(rendererSend).toHaveBeenCalledWith("updates:status", expect.anything());
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
        "Couldn't check for updates — the update information was not found on the server.",
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
      message: "Download failed — the update file was not found on the server.",
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
        "Couldn't check for updates — the update information was not found on the server.",
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
      message: "Download failed — the update file was not found on the server.",
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
    const checkedAt = module.getUpdateStatus().checkedAt;
    updaterEvents.get("error")?.(err);
    expect(module.getUpdateStatus().checkedAt).toBe(checkedAt);
    expect(module.getUpdateStatus().checkError).toBe(err.message);

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

  it("stops showing checking when an error event fires before the check promise settles", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockImplementation(() => new Promise(() => undefined));

    void module.checkForUpdatesNow(stateDir, { requestId: "manual-update-1" });
    await flushMicrotasks();
    expect(module.getUpdateStatus()).toMatchObject({ state: "checking" });

    updaterEvents.get("error")?.(new Error("net::ERR_SSL_PROTOCOL_ERROR"));

    expect(module.getUpdateStatus()).toMatchObject({
      state: "error",
      message: "net::ERR_SSL_PROTOCOL_ERROR",
      netError: true,
      requestId: "manual-update-1",
    });
  });

  it("keeps non-net manual check errors verbatim", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockRejectedValueOnce(new Error("boom"));

    await module.checkForUpdatesNow(stateDir);

    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "boom",
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
    expect(module.getUpdateStatus()).toMatchObject({ state: "idle" });

    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({ state: "idle" });

    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({ state: "idle" });

    await module.startAutoUpdates(stateDir);
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
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
    expect(module.getUpdateStatus()).toMatchObject({
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

    expect(module.getUpdateStatus()).toMatchObject({ state: "not-available", checkedAt: expect.any(Number) });
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
    await flushMicrotasks();
    expect(reconcileFeaturePin).toHaveBeenCalledTimes(1);

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
    await flushMicrotasks();
    const featureCheck = module.checkForUpdatesNow(stateDir, {
      settings: featureSettings,
      requestId: "feature-2709",
    });
    await flushMicrotasks();

    updaterEvents.get("update-available")?.({ version: "1.9.0" });
    expect(module.getUpdateStatus()).toEqual({
      state: "downloading",
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
    // minutes every time, and nightly re-stages every 15 — the loop would never
    // get a turn.
    expect(setIntervalSpy.mock.calls.length).toBe(afterFirst);

    updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
    expect(setIntervalSpy.mock.calls.length).toBeGreaterThan(afterFirst);
  });

  it.each([false, true])("settles an automatic check without a terminal event (available %s)", async (available) => {
    const h = await importAutoUpdater({ enabled: false, channel: "nightly", nightlyAck: true, feature: null });
    h.autoUpdater.checkForUpdates.mockImplementation(async () => {
      h.updaterEvents.get("checking-for-update")?.();
      return { isUpdateAvailable: available, updateInfo: { version: "2.0.0-nightly.1" } };
    });
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus().state).toBe(available ? "available" : "not-available");
  });

  it.each(["manual", "automatic", "return-home"] as const)("aborts a hung %s check before letting a queued retry run", async (kind) => {
    vi.useFakeTimers();
    const h = await importAutoUpdater();
    let requestAborted = false;
    let finishAborting!: () => void;
    const originalRequest = h.autoUpdater.httpExecutor.request;
    originalRequest.mockImplementation(async (_options, token) => {
      try {
        return await token!.createPromise<string>(() => {});
      } finally {
        requestAborted = true;
        await new Promise<void>((resolve) => { finishAborting = resolve; });
      }
    });
    h.autoUpdater.checkForUpdates.mockImplementationOnce(async () => {
      h.updaterEvents.get("checking-for-update")?.();
      await h.autoUpdater.httpExecutor.request({});
    }).mockResolvedValue({ isUpdateAvailable: false, updateInfo: { version: "1.0.0" } });
    const first = kind === "manual" ? h.module.checkForUpdatesNow(stateDir, { requestId: "hung" })
      : kind === "automatic" ? h.module.startAutoUpdates(stateDir) : h.module.returnToHome(stateDir, "hung");
    await vi.advanceTimersByTimeAsync(0);
    const retry = h.module.checkForUpdatesNow(stateDir, { requestId: "retry" });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(requestAborted).toBe(true);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "error", message: expect.stringContaining("timed out") });
    expect(h.autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
    finishAborting();
    await first;
    await retry;
    expect(h.autoUpdater.httpExecutor.request).toBe(originalRequest);
    expect(h.autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "not-available", requestId: "retry" });
  });

  it("clears the check deadline before an automatic download continues", async () => {
    vi.useFakeTimers();
    const h = await importAutoUpdater();
    const originalRequest = h.autoUpdater.httpExecutor.request;
    let finishDownload!: () => void;
    h.autoUpdater.checkForUpdates.mockResolvedValue({
      isUpdateAvailable: true, updateInfo: { version: "2.0.0" },
      downloadPromise: new Promise<void>((resolve) => { finishDownload = resolve; }),
    });
    const checking = h.module.startAutoUpdates(stateDir);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.autoUpdater.httpExecutor.request).toBe(originalRequest);
    finishDownload();
    await checking;
  });

  // A check that starts a download returns its promise WITHOUT awaiting it
  // (AppUpdater marks that `noinspection ES6MissingAwait`). Every path that can
  // force a download therefore has to own it, or the download, the localhost
  // handoff to Squirrel and the native staging behind it outlive the operation
  // that started them — and the queue lets the next one run on top.
  const deferredDownload = () => {
    let finish!: () => void;
    const downloadPromise = new Promise<void>((resolve) => { finish = resolve; });
    return { finish, result: {
      isUpdateAvailable: true,
      updateInfo: { version: "2.0.0" },
      downloadPromise,
    } };
  };
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

  it("owns a forced download on the automatic path with auto-download off", async () => {
    // The gate used to be `if (settings.enabled)`, but a stale staged build
    // forces a download precisely WHEN the preference is off — it has to be
    // superseded or quitting installs the channel the user left.
    const h = await importAutoUpdater({
      enabled: false, channel: "latest", nightlyAck: false, feature: null,
    });
    const { finish, result } = deferredDownload();
    h.autoUpdater.checkForUpdates.mockResolvedValue(result);

    let settled = false;
    const checking = h.module.startAutoUpdates(stateDir).then(() => { settled = true; });
    await settle();

    expect(h.autoUpdater.checkForUpdates).toHaveBeenCalled();
    expect(settled).toBe(false);

    finish();
    await checking;
    expect(settled).toBe(true);
  });

  it("holds a manual check open until a forced download finishes", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    const { finish, result } = deferredDownload();
    autoUpdater.checkForUpdates.mockResolvedValue(result);

    let settled = false;
    const checking = module.checkForUpdatesNow(stateDir).then(() => { settled = true; });
    await settle();
    expect(settled).toBe(false);

    finish();
    await checking;
    expect(settled).toBe(true);
  });

  it("holds return-home open until a forced download finishes", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    const { finish, result } = deferredDownload();
    autoUpdater.checkForUpdates.mockResolvedValue(result);

    let settled = false;
    const returning = module.returnToHome(stateDir).then(() => { settled = true; });
    await settle();
    expect(settled).toBe(false);

    finish();
    await returning;
    expect(settled).toBe(true);
  });

  it("blocks the next queued operation until a forced download finishes", async () => {
    // The point of owning the download: the queue must not hand the updater to
    // another operation while a handoff is still in flight.
    const { module, autoUpdater } = await importAutoUpdater();
    const { finish, result } = deferredDownload();
    autoUpdater.checkForUpdates
      .mockResolvedValueOnce(result)
      .mockResolvedValue({ isUpdateAvailable: false, updateInfo: { version: "2.0.0" } });

    const first = module.checkForUpdatesNow(stateDir, { requestId: "one" });
    await settle();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);

    const second = module.checkForUpdatesNow(stateDir, { requestId: "two" });
    await settle();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);

    finish();
    await Promise.all([first, second]);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
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

  it("downloads an event-less manual offer when automatic downloads are enabled when the feed has a build", async () => {
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockResolvedValue({
      isUpdateAvailable: true,
      updateInfo: { version: "2.5.0" },
    });

    await module.checkForUpdatesNow(stateDir, { requestId: "manual-update-1" });

    expect(module.getUpdateStatus()).toMatchObject({
      state: "downloading",
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

  it("drops an older staged build from the running channel", async () => {
    writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({
      version: "0.12.13-nightly.202609070711",
      stagedAt: Date.now(),
      channel: "nightly",
    }));

    const harness = await importAutoUpdaterKeepingStagedFile(
      { enabled: false, channel: "nightly", nightlyAck: true, feature: null },
      { version: "0.12.13-nightly.202609070850" },
    );
    await harness.module.startAutoUpdates(stateDir);

    expect(harness.module.getUpdateStatus().staged).toBeUndefined();
  });

  it("retains an older staged build from another channel", async () => {
    writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({
      version: "0.12.10-nightly.1",
      stagedAt: Date.now(),
      channel: "nightly",
    }));

    const harness = await importAutoUpdaterKeepingStagedFile(
      { enabled: false, channel: "nightly", nightlyAck: true, feature: null },
      { version: "0.12.11" },
    );
    await harness.module.startAutoUpdates(stateDir);

    expect(harness.module.getUpdateStatus().staged).toMatchObject({
      version: "0.12.10-nightly.1",
    });
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
    expect(module.getUpdateStatus().staged).toBeUndefined();
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
    expect(module.getUpdateStatus().staged).toBeUndefined();
  });

  it("keeps the escalation timer off once a stranded build is discarded", async () => {
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
    expect(module.getUpdateStatus().staged).toBeUndefined();
    expect(module.getUpdateStatus().stagedAt).toBeUndefined();
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

  it("requires new confirmation before downloading a changed remembered target", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({ version: "2.1.0", stagedAt: Date.now(), channel: "latest" }));
      const { module, autoUpdater, updaterEvents, startMacUpdateProgress } = await importAutoUpdater({ enabled: false, channel: "latest", nightlyAck: false, feature: null });
      await module.startAutoUpdates(stateDir);
      autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: "2.2.0", releaseNotes: "New release B" } });
      autoUpdater.downloadUpdate.mockImplementation(async () => {
        updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
        return [];
      });
      await expect(module.quitAndInstallUpdate("2.1.0")).resolves.toEqual({
        state: "confirmation-required", version: "2.2.0", releaseNotes: "New release B",
      });
      expect(autoUpdater.downloadUpdate).not.toHaveBeenCalled();
      expect(startMacUpdateProgress).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      await module.quitAndInstallUpdate("2.2.0");
      expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledTimes(1);
    } finally { restore(); }
  });

  it("does not install a newer build already staged after the dialog opened", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, autoUpdater, updaterEvents, startMacUpdateProgress } = await importAutoUpdater();
      await module.startAutoUpdates(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.2.0", releaseNotes: "Staged release B" });
      await expect(module.quitAndInstallUpdate("2.1.0")).resolves.toMatchObject({ state: "confirmation-required", version: "2.2.0", releaseNotes: "Staged release B" });
      expect(startMacUpdateProgress).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      await module.quitAndInstallUpdate("2.2.0");
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledTimes(1);
    } finally { restore(); }
  });

  it("requires confirmation again if the feed changes a second time", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({ version: "2.1.0", stagedAt: Date.now(), channel: "latest" }));
      const { module, autoUpdater } = await importAutoUpdater({ enabled: false, channel: "latest", nightlyAck: false, feature: null });
      await module.startAutoUpdates(stateDir);
      for (const [confirmed, offered] of [["2.1.0", "2.2.0"], ["2.2.0", "2.3.0"]]) {
        autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: offered } });
        await expect(module.quitAndInstallUpdate(confirmed)).resolves.toMatchObject({ state: "confirmation-required", version: offered });
      }
      expect(autoUpdater.downloadUpdate).not.toHaveBeenCalled();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally { restore(); }
  });

  it("prepares a remembered macOS update again before requesting restart", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({ version: "2.1.0", stagedAt: Date.now(), channel: "latest" }));
      const { module, autoUpdater, updaterEvents, nativeAutoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      const downloaded = deferred();
      autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: "2.1.0" } });
      autoUpdater.downloadUpdate.mockImplementation(async () => {
        updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
        await downloaded.promise;
      });
      const install = module.quitAndInstallUpdate();
      const duplicate = module.quitAndInstallUpdate();
      await flushMicrotasks();
      expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
      const afterDownloadedEvent = module.quitAndInstallUpdate();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      downloaded.resolve();
      await flushMicrotasks();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      nativeAutoUpdater.emit("update-downloaded");
      await Promise.all([install, duplicate, afterDownloadedEvent]);
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledTimes(1);
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledWith(false, true);
    } finally {
      restore();
    }
  });

  it.each(["no longer available", "download failed"])("keeps AO open when restart preparation is %s", async (failure) => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({ version: "2.1.0", stagedAt: Date.now(), channel: "latest" }));
      const { module, autoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: failure === "download failed", updateInfo: { version: "2.1.0" } });
      autoUpdater.downloadUpdate.mockRejectedValue(new Error("download failed"));
      await expect(module.quitAndInstallUpdate()).rejects.toThrow();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(module.getUpdateStatus().state).toBe("error");
    } finally {
      restore();
    }
  });

  it("rejects a translocated installation without quitting", async () => {
    const restore = stubProcess("darwin", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Applications/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally { restore(); }
  });

  it.each(["bundle", "parent"])("rejects an unwritable %s without quitting", async (location) => {
    const { root, bundle, execPath } = makeBundle();
    chmodSync(location === "bundle" ? bundle : root, 0o555);
    const restore = stubProcess("darwin", execPath);
    try {
      const { module, autoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/writ/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      restore(); chmodSync(root, 0o755); chmodSync(bundle, 0o755);
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("does not quit without a staged update even in a writable app", async () => {
    const { root, execPath } = makeBundle();
    const restore = stubProcess("darwin", execPath);
    try {
      const { module, autoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Check for updates/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally { restore(); rmSync(root, { recursive: true, force: true }); }
  });

  it.each(["error", "timeout"])("does not quit when native preparation ends with %s", async (outcome) => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    vi.useFakeTimers();
    try {
      const { module, autoUpdater, updaterEvents, nativeAutoUpdater, startMacUpdateProgress } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      const install = module.quitAndInstallUpdate();
      const assertion = expect(install).rejects.toThrow();
      await flushMicrotasks();
      if (outcome === "error") nativeAutoUpdater.emit("error", new Error("signature rejected"));
      else await vi.advanceTimersByTimeAsync(180_000);
      await assertion;
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(startMacUpdateProgress).not.toHaveBeenCalled();
      expect(nativeAutoUpdater.listenerCount("update-downloaded")).toBe(1);
      expect(nativeAutoUpdater.listenerCount("error")).toBe(1);
    } finally { vi.useRealTimers(); restore(); }
  });

  it("times out a remembered native transfer that never settles and rejects late retries", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    vi.useFakeTimers();
    try {
      writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({ version: "2.1.0", stagedAt: Date.now(), channel: "latest" }));
      const { module, autoUpdater, updaterEvents, nativeAutoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      const transfer = deferred();
      autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: "2.1.0" } });
      autoUpdater.downloadUpdate.mockImplementation(() => {
        updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
        return transfer.promise;
      });
      const assertion = expect(module.quitAndInstallUpdate()).rejects.toThrow(/Close and reopen AO/);
      await flushMicrotasks();
      await vi.advanceTimersByTimeAsync(180_000);
      await assertion;
      nativeAutoUpdater.emit("update-downloaded");
      transfer.resolve();
      await flushMicrotasks();
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Close and reopen AO/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
    } finally { vi.useRealTimers(); restore(); }
  });

  it("ignores duplicate downloads during native preparation and accepts a later replacement", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, autoUpdater, updaterEvents, nativeAutoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      autoUpdater.downloadUpdate
        .mockImplementationOnce(async () => { updaterEvents.get("update-downloaded")?.({ version: "2.1.0" }); })
        .mockImplementationOnce(async () => { updaterEvents.get("update-downloaded")?.({ version: "2.2.0" }); });
      const first = module.downloadUpdateNow();
      await flushMicrotasks();
      const second = module.downloadUpdateNow();
      await flushMicrotasks();
      expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
      nativeAutoUpdater.emit("update-downloaded");
      await first;
      await second;
      expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
      autoUpdater.autoDownload = false;
      updaterEvents.get("update-available")?.({ version: "2.2.0" });
      const replacement = module.downloadUpdateNow();
      await flushMicrotasks();
      expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(2);
      const install = module.quitAndInstallUpdate();
      await flushMicrotasks();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      nativeAutoUpdater.emit("update-downloaded");
      await Promise.all([replacement, install]);
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledTimes(1);
    } finally { restore(); }
  });

  it("serializes a stale-channel replacement even when automatic downloads are disabled", async () => {
    writeFileSync(nodePath.join(stateDir, "staged-update.json"), JSON.stringify({ version: "2.1.0", stagedAt: Date.now(), channel: "nightly" }));
    const { module, autoUpdater } = await importAutoUpdater({ enabled: false, channel: "latest", nightlyAck: false, feature: null });
    const transfer = deferred();
    autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: "2.1.0" }, downloadPromise: transfer.promise, cancellationToken: new CancellationToken() });
    const first = module.startAutoUpdates(stateDir);
    await vi.waitFor(() => expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1));
    expect(autoUpdater.autoDownload).toBe(true);
    const second = module.checkForUpdatesNow(stateDir);
    await flushMicrotasks();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(1);
    transfer.resolve();
    await Promise.all([first, second]);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
  });

  it.each(["check", "return-home"])("keeps provider-owned downloads serialized during %s", async (operation) => {
    const { module, autoUpdater } = await importAutoUpdater();
    await module.startAutoUpdates(stateDir);
    const transfer = deferred();
    autoUpdater.checkForUpdates.mockResolvedValue({ isUpdateAvailable: true, updateInfo: { version: "2.1.0" }, downloadPromise: transfer.promise, cancellationToken: new CancellationToken() });
    const before = autoUpdater.checkForUpdates.mock.calls.length;
    const first = operation === "check" ? module.checkForUpdatesNow(stateDir) : module.returnToHome(stateDir);
    await flushMicrotasks();
    const second = module.checkForUpdatesNow(stateDir);
    await flushMicrotasks();
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(before + 1);
    transfer.resolve();
    await Promise.all([first, second]);
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(before + 2);
  });

  it("recovers the window and helper when the native quit handoff fails asynchronously", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, updaterEvents, nativeAutoUpdater, startMacUpdateProgress } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      nativeAutoUpdater.emit("update-downloaded");
      const failed = vi.fn();
      module.setUpdateRestartFailureHandler(failed);
      await module.quitAndInstallUpdate();
      expect(module.isUpdateRestartRequested()).toBe(true);
      nativeAutoUpdater.emit("error", new Error("could not persist the installer request"));
      expect(module.isUpdateRestartRequested()).toBe(false);
      expect(failed).toHaveBeenCalledOnce();
      const progress = await startMacUpdateProgress.mock.results[0].value;
      expect(progress.fail).toHaveBeenCalledWith("could not persist the installer request");
    } finally { restore(); }
  });

  it("requires a visible helper before quitting an already native-ready update", async () => {
    const restore = stubProcess("darwin", "/usr/bin/node");
    try {
      const { module, autoUpdater, updaterEvents, nativeAutoUpdater, startMacUpdateProgress } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.startAutoUpdates(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
      nativeAutoUpdater.emit("update-downloaded");
      startMacUpdateProgress.mockRejectedValueOnce(new Error("helper unavailable"));
      await expect(module.quitAndInstallUpdate()).rejects.toThrow("helper unavailable");
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally { restore(); }
  });

  it.each(["win32", "linux"] as const)("rejects an install without a staged build on %s", async (platform) => {
    const restore = stubProcess(platform, "/usr/bin/node");
    try {
      const { module, autoUpdater } = await importAutoUpdater();
      await module.checkForUpdatesNow(stateDir);
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/not ready/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally { restore(); }
  });

  it("never blocks off macOS, even for translocation-looking paths", async () => {
    const restore = stubProcess("win32", TRANSLOCATED_EXEC_PATH);
    try {
      const { module, autoUpdater, dialog, updaterEvents } = await importAutoUpdater(undefined, { nativeReadyManually: true });

      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("update-downloaded")?.({ version: "2.0.0" });
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
  it.each(["darwin", "win32", "linux"] as const)("blocks explicit install while a stale build awaits replacement on %s", async (platform) => {
    const restore = stubProcess(platform, "/usr/bin/node");
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
      await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Check for updates/);
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  it("re-enables install-on-quit once the replacement is staged", async () => {
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

describe("channel downgrade safety", () => {
  const nightly = { enabled: false, channel: "nightly", nightlyAck: true, feature: null } as const;
  const stable = { enabled: false, channel: "latest", nightlyAck: false, feature: null } as const;
  const running = "0.12.11-nightly.202609051608";

  function serveVersion(harness: Awaited<ReturnType<typeof importAutoUpdater>>, version: string, installed = running) {
    harness.autoUpdater.checkForUpdates.mockImplementation(async () => {
      const available = semver.gt(version, installed) ||
        (harness.autoUpdater.allowDowngrade && semver.lt(version, installed));
      harness.updaterEvents.get(available ? "update-available" : "update-not-available")?.({ version });
      return { isUpdateAvailable: available, updateInfo: { version } };
    });
  }

  it.each([false, true])("honors saved Stable on startup and later checks (automatic download %s)", async (enabled) => {
    const h = await importAutoUpdater({ ...stable, enabled }, { version: running });
    serveVersion(h, "0.12.10");
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: enabled ? "downloading" : "available", version: "0.12.10" });
    expect(h.autoUpdater.autoDownload).toBe(enabled);
    await h.module.checkForUpdatesNow(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: enabled ? "downloading" : "available", version: "0.12.10" });
    expect(h.autoUpdater.allowDowngrade).toBe(true);
  });

  it("retains a saved channel switch after its first check fails", async () => {
    const h = await importAutoUpdater(nightly, { version: running });
    h.writeUpdateSettings.mockImplementation(async (_dir, settings) => {
      h.readUpdateSettings.mockResolvedValue(settings);
    });
    h.autoUpdater.checkForUpdates.mockRejectedValueOnce(new Error("network unavailable"));
    await h.module.checkForUpdatesNow(stateDir, { settings: stable });
    expect(h.module.getUpdateStatus().state).toBe("error");
    serveVersion(h, "0.12.10");
    await h.module.checkForUpdatesNow(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: "0.12.10" });
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: "0.12.10" });
  });

  it("stops allowing downgrades after relaunching into the selected Stable channel", async () => {
    const h = await importAutoUpdater(stable, { version: "0.12.10" });
    serveVersion(h, "0.12.9", "0.12.10");
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus().state).toBe("not-available");
    expect(h.autoUpdater.allowDowngrade).toBe(false);
  });

  it.each([
    { settings: stable, installed: "0.12.11", offered: "0.12.10" },
    { settings: { ...stable, feature: { pr: 4900 } }, installed: "0.12.11-pr4900.2", offered: "0.12.11-pr4900.1" },
  ])("rejects routine older releases on $installed", async ({ settings, installed, offered }) => {
    const h = await importAutoUpdater(settings, { version: installed });
    serveVersion(h, offered, installed);
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus().state).toBe("not-available");
    await h.module.checkForUpdatesNow(stateDir);
    expect(h.module.getUpdateStatus().state).toBe("not-available");
  });

  it("does not redownload a staged candidate after rejecting an older manifest", async () => {
    const h = await importAutoUpdater({ ...nightly, enabled: true }, { version: running });
    await h.module.checkForUpdatesNow(stateDir);
    h.updaterEvents.get("update-downloaded")?.({ version: "0.12.11-nightly.202609061608" });
    serveVersion(h, "0.12.11-nightly.202609041608");
    await h.module.startAutoUpdates(stateDir);
    expect(h.autoUpdater.autoDownload).toBe(false);
    expect(h.autoUpdater.downloadUpdate).not.toHaveBeenCalled();
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "downloaded", version: "0.12.11-nightly.202609061608" });
  });

  it.each([
    { settings: stable, installed: "0.12.10", offered: "0.12.11" },
    { settings: nightly, installed: running, offered: "0.12.11-nightly.202609061608" },
  ])("offers newer releases on $installed without downgrade permission", async ({ settings, installed, offered }) => {
    const h = await importAutoUpdater(settings, { version: installed });
    serveVersion(h, offered, installed);
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: offered });
    await h.module.checkForUpdatesNow(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: offered });
    expect(h.autoUpdater.allowDowngrade).toBe(false);
  });

  it("allows an explicit channel switch when the preference was saved before checking", async () => {
    const h = await importAutoUpdater(stable, { version: running });
    serveVersion(h, "0.12.10");
    await h.module.setUpdateSettings(stateDir, stable);
    await h.module.checkForUpdatesNow(stateDir, { settings: stable });
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: "0.12.10" });
  });

  it("returns an installed feature build home after its pin is retired", async () => {
    const installed = "0.12.11-pr4900.2";
    const h = await importAutoUpdater(stable, { version: installed });
    serveVersion(h, "0.12.10", installed);
    await h.module.startAutoUpdates(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: "0.12.10" });
  });

  it("permits an explicit return home from nightly", async () => {
    const h = await importAutoUpdater(stable, { version: running });
    serveVersion(h, "0.12.10");
    await h.module.returnToHome(stateDir);
    expect(h.module.getUpdateStatus()).toMatchObject({ state: "available", version: "0.12.10" });
  });

  it("does not permit a same-channel settings check to downgrade nightly", async () => {
    const h = await importAutoUpdater(nightly, { version: running });
    serveVersion(h, "0.12.11-nightly.202609041608");
    await h.module.checkForUpdatesNow(stateDir, { settings: nightly });
    expect(h.module.getUpdateStatus().state).toBe("not-available");
  });

  it("keeps first-run nightly on nightly when automatic downloads are declined", async () => {
    const h = await importAutoUpdater(stable, { version: running });
    h.dialog.showMessageBox.mockResolvedValue({ response: 1 });
    await h.module.ensureUpdatePrefs(stateDir);
    expect(h.writeUpdateSettings).toHaveBeenCalledWith(stateDir, nightly);
  });

  it("preselects nightly for the first-run channel choice on a nightly install", async () => {
    const h = await importAutoUpdater(stable, { version: running });
    h.dialog.showMessageBox
      .mockResolvedValueOnce({ response: 0 })
      .mockResolvedValueOnce({ response: 1 })
      .mockResolvedValueOnce({ response: 0 });
    await h.module.ensureUpdatePrefs(stateDir);
    expect(h.dialog.showMessageBox.mock.calls[1]?.[0]).toMatchObject({ defaultId: 1, cancelId: 1 });
    expect(h.writeUpdateSettings).toHaveBeenCalledWith(stateDir, { ...nightly, enabled: true });
  });

  it("preserves existing explicit stable preferences on a nightly install", async () => {
    const h = await importAutoUpdater(stable, { version: running });
    writeFileSync(nodePath.join(stateDir, "update-settings.json"), JSON.stringify(stable));
    await h.module.ensureUpdatePrefs(stateDir);
    expect(h.dialog.showMessageBox).not.toHaveBeenCalled();
    expect(h.writeUpdateSettings).not.toHaveBeenCalled();
  });
});

// #4254: macOS auto-update failed at ShipIt's code-signature validation and
// then failed identically on every retry. MacUpdater re-emits native Squirrel
// failures onto electron-updater's "error" event, and
// DownloadedUpdateHelper.validateDownloadedPath re-serves an
// already-downloaded file on existence alone for the lifetime of the process,
// so the retry never re-downloads and never re-verifies.
describe("staged install rejection", () => {
  const rejection = new Error(
    "Code signature at URL file:///Users/x/Library/Caches/dev.agent-orchestrator.desktop.ShipIt/" +
      "update.M9ZvE0X/Agent%20Orchestrator.app/ did not pass validation: " +
      "code failed to satisfy specified code requirement(s)",
  );

  it("keeps the verified download on a first failure and re-stages instead", async () => {
    // Squirrel verifies the copy it extracted, in-process, before ShipIt exists.
    // A rejection therefore indicts the EXTRACTION, not the zip — which
    // electron-updater already checked against the feed sha512. Observed on a
    // real failure: the cached zip was byte-identical to the feed and the next
    // attempt extracted it cleanly. Purging here would force a 176 MB
    // re-download to fix a bad untar.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);

    expect(autoUpdater.downloadedUpdateHelper.clear).not.toHaveBeenCalled();
    // Still disarmed: that copy cannot install, and leaving it staged would
    // promise a restart that fails. This also re-enables auto-download, which
    // is what drives the re-extraction.
    expect(module.getUpdateStatus().staged).toBeUndefined();
    expect(statusMessages().at(-1)?.payload).toMatchObject({
      state: "error",
      message: expect.stringContaining("prepare it again"),
    });
    consoleErrorSpy.mockRestore();
  });

  it("discards the download once the same build fails a second time", async () => {
    // A re-extraction failing too is the first real evidence the bytes are
    // suspect, so now the zip goes.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);

    // Deferred, not fired and forgotten: the clear is queued on the operation
    // chain, so it has not run at the instant the rejection is handled.
    expect(autoUpdater.downloadedUpdateHelper.clear).not.toHaveBeenCalled();
    expect(statusMessages().at(-1)?.payload).toMatchObject({
      state: "error",
      message: expect.stringContaining("stopped retrying on its own"),
    });

    // ...and the next operation cannot begin until it has. Awaiting one drains
    // the queue behind the cleanup, which is the property that stops a download
    // starting into a pending directory that is still being emptied.
    await module.checkForUpdatesNow(stateDir);
    expect(autoUpdater.downloadedUpdateHelper.clear).toHaveBeenCalledTimes(1);
    consoleErrorSpy.mockRestore();
  });

  // Disarming a rejected build re-enables auto-download, which is what buys the
  // cheap re-preparation. Unbounded, that is also a loop: fetch 176 MB, fail
  // verification, discard, fetch again, on every check for as long as the app
  // runs. These three cover the bound and both of its resets.
  const failTwice = (
    updaterEvents: Map<string, (...args: unknown[]) => unknown>,
  ) => {
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);
  };

  it("stops automatically re-downloading a build that failed twice", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    failTwice(updaterEvents);

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.autoDownload).toBe(false);
    consoleErrorSpy.mockRestore();
  });

  it("gives a different build its own recovery attempts", async () => {
    // The budget answers "has THIS target exhausted its retries". Something
    // newer must not inherit the previous build's exhaustion.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    failTwice(updaterEvents);
    updaterEvents.get("update-available")?.({ version: "2.2.0" });

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.autoDownload).toBe(true);
    consoleErrorSpy.mockRestore();
  });

  // Squirrel's checkForUpdatesCommand is a RACCommand that does not allow
  // concurrent execution, so a second native handoff while one is staging is
  // REFUSED with RACCommandErrorDomain/1 rather than queued — and Electron puts
  // that code and domain on the JS Error. It is not an update failure, and its
  // native wording ("The command is disabled and cannot be executed") is
  // meaningless to a user.
  const nativeBusyError = Object.assign(
    new Error("The command is disabled and cannot be executed"),
    { code: 1, domain: "RACCommandErrorDomain" },
  );

  it("does not report a refused native handoff as an update failure", async () => {
    const consoleInfoSpy = vi
      .spyOn(console, "info")
      .mockImplementation(() => undefined);
    const { module, updaterEvents, statusMessages } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    const beforeRefusal = statusMessages().at(-1)?.payload;

    updaterEvents.get("error")?.(nativeBusyError);

    expect(statusMessages().at(-1)?.payload).toEqual(beforeRefusal);
    expect(module.getUpdateStatus().state).not.toBe("error");
    consoleInfoSpy.mockRestore();
  });

  it("settles a manual check whose native handoff was refused", async () => {
    // The manual path broadcasts "checking" before it starts, so swallowing the
    // rejection outright would wedge the Settings spinner.
    const consoleInfoSpy = vi
      .spyOn(console, "info")
      .mockImplementation(() => undefined);
    const { module, autoUpdater } = await importAutoUpdater();
    autoUpdater.checkForUpdates.mockRejectedValue(nativeBusyError);

    await module.checkForUpdatesNow(stateDir, { requestId: "manual-1" });

    expect(module.getUpdateStatus().state).toBe("not-available");
    consoleInfoSpy.mockRestore();
  });

  it("restores the budget when the user checks again", async () => {
    // The exhausted message tells the user to check for updates again, so that
    // has to actually do something.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    failTwice(updaterEvents);
    await module.checkForUpdatesNow(stateDir);

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.autoDownload).toBe(true);
    consoleErrorSpy.mockRestore();
  });

  it("keeps the actionable message when one rejection is delivered twice", async () => {
    // MacUpdater re-emits every native Squirrel error onto electron-updater's
    // own "error" event, and the operation promise can reject with that same
    // error. Handling the first delivery disarms the staged build, so without a
    // dedupe the second misses the branch, falls through to generic handling,
    // and replaces the actionable message with the raw signature dump.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const consoleDebugSpy = vi
      .spyOn(console, "debug")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);
    const afterFirstDelivery = statusMessages().at(-1)?.payload;

    updaterEvents.get("error")?.(rejection);

    expect(statusMessages().at(-1)?.payload).toEqual(afterFirstDelivery);
    expect(statusMessages().at(-1)?.payload).toMatchObject({
      state: "error",
      message: expect.stringContaining("prepare it again"),
    });
    // The repeat must not be miscounted as a genuine second failure, which
    // would discard a download that has only actually failed once.
    expect(autoUpdater.downloadedUpdateHelper.clear).not.toHaveBeenCalled();
    consoleDebugSpy.mockRestore();
    consoleErrorSpy.mockRestore();
  });

  it("does not carry a previous build's failure over to a new one", async () => {
    // The count answers "has THIS build failed before". A newer build that
    // fails once must still get its cheap retry.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(rejection);
    updaterEvents.get("update-downloaded")?.({ version: "2.2.0" });
    updaterEvents.get("error")?.(rejection);

    expect(autoUpdater.downloadedUpdateHelper.clear).not.toHaveBeenCalled();
    consoleErrorSpy.mockRestore();
  });

  it("surfaces the failure even on an automatic check", async () => {
    // The automatic path suppresses one-off failures so the UI does not flash
    // an error nobody asked for. That suppression must not swallow this class:
    // an install the user cannot retry out of is exactly what has to be shown.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });

    autoUpdater.checkForUpdates.mockImplementationOnce(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("error")?.(rejection);
      return Promise.resolve();
    });
    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus().state).toBe("error");
    expect(module.getUpdateStatus().staged).toBeUndefined();
    consoleErrorSpy.mockRestore();
  });

  // The 2026-09-05 v0.12.10 report carries the OTHER Security-framework
  // wording for the same "the staged copy is not installable" outcome. Both
  // reach us through the same SQRLCodeSignature.m prefix, and both have to
  // clear the cache. Reproduced locally against the published nightly, driving
  // SecStaticCodeCheckValidityWithErrors with the flags Electron's patched
  // Squirrel.Mac actually uses (nested | strict | all-architectures): this
  // string is errSecCSUnsigned, which a staged bundle reports when its root
  // executable OR a nested helper-app/framework binary is missing or unsigned.
  // Seal damage and sealed-resource damage give different codes. No retry of
  // the same staged bytes can fix any of them.
  it("also handles the 'not signed at all' wording", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(
      new Error(
        "Code signature at URL file:///Users/graycup/Library/Caches/" +
          "dev.agent-orchestrator.desktop.ShipIt/update.lmaIGgc/Agent%20Orchestrator.app/ " +
          "did not pass validation: code object is not signed at all",
      ),
    );

    expect(module.getUpdateStatus().staged).toBeUndefined();
    expect(module.getUpdateStatus().state).toBe("error");
    consoleErrorSpy.mockRestore();
  });

  // The other failure Squirrel raises for the same outcome
  // (SQRLCodeSignature.m:116, CouldNotCreateStaticCode): the staged bundle is
  // damaged badly enough that a code object cannot even be constructed.
  it("also handles 'failed to get static code for bundle'", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(
      new Error(
        "Failed to get static code for bundle file:///Users/x/Library/Caches/" +
          "dev.agent-orchestrator.desktop.ShipIt/update.M9ZvE0X/Agent%20Orchestrator.app/",
      ),
    );

    expect(module.getUpdateStatus().staged).toBeUndefined();
    expect(module.getUpdateStatus().state).toBe("error");
    consoleErrorSpy.mockRestore();
  });

  // Merely mentioning the staging path must NOT trip this. The staging path
  // literally contains "ShipIt", so a pattern loose enough to match the path
  // would fire on unrelated failures that a re-download cannot fix.
  it("ignores an unrelated error that only mentions the ShipIt path", async () => {
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(
      new Error(
        "EACCES: permission denied, open '/Users/x/Library/Caches/" +
          "dev.agent-orchestrator.desktop.ShipIt/ShipItState.plist'",
      ),
    );

    expect(autoUpdater.downloadedUpdateHelper.clear).not.toHaveBeenCalled();
    expect(module.getUpdateStatus().staged).toBeDefined();
    consoleErrorSpy.mockRestore();
  });

  it("leaves an ordinary download failure alone", async () => {
    // Same "error" event, nothing staged to reject: the existing suppress-and-
    // restore behaviour has to survive untouched.
    const consoleErrorSpy = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(new Error("net::ERR_CONNECTION_RESET"));

    expect(autoUpdater.downloadedUpdateHelper.clear).not.toHaveBeenCalled();
    consoleErrorSpy.mockRestore();
  });
});

// #3288 workstream 0: scripts/e2e-mac-update.mjs waits on this listener to know
// an update actually STAGED. It was deleted by an unrelated refactor in #3012
// and the macOS update-hop e2e job silently stopped being runnable, which is
// how #4254 reached users with no update-hop coverage at all.
describe("e2e staging sentinel", () => {
  const originalSentinel = process.env.AO_E2E_UPDATE_SENTINEL;
  afterEach(() => {
    if (originalSentinel === undefined) delete process.env.AO_E2E_UPDATE_SENTINEL;
    else process.env.AO_E2E_UPDATE_SENTINEL = originalSentinel;
  });

  it("writes the sentinel from the NATIVE updater's update-downloaded", async () => {
    const sentinel = nodePath.join(stateDir, "sentinel.json");
    process.env.AO_E2E_UPDATE_SENTINEL = sentinel;
    const { module, nativeUpdaterEvents, updaterEvents } =
      await importAutoUpdater(undefined, { nativeReadyManually: true });

    await module.checkForUpdatesNow(stateDir);
    // electron-updater's own update-downloaded fires BEFORE Squirrel is told to
    // fetch, so keying the harness off it would stage nothing. It must not
    // write the sentinel.
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    expect(existsSync(sentinel)).toBe(false);

    nativeUpdaterEvents.get("update-downloaded")?.({}, "notes", "2.1.0");
    expect(JSON.parse(readFileSync(sentinel, "utf8"))).toEqual({
      stagedAt: expect.any(Number),
      releaseName: "2.1.0",
    });
  });

  it("registers no sentinel writer when the env var is unset", async () => {
    delete process.env.AO_E2E_UPDATE_SENTINEL;
    const sentinel = nodePath.join(stateDir, "sentinel.json");
    const { module, nativeUpdaterEvents } = await importAutoUpdater();
    await module.checkForUpdatesNow(stateDir);
    // The sentinel was never wired at import (env unset), so a native
    // update-downloaded writes nothing even if the env var is set afterwards.
    // (Other native handlers, e.g. the macOS restart handler, may exist.)
    process.env.AO_E2E_UPDATE_SENTINEL = sentinel;
    nativeUpdaterEvents.get("update-downloaded")?.({}, "notes", "2.1.0");
    delete process.env.AO_E2E_UPDATE_SENTINEL;
    expect(existsSync(sentinel)).toBe(false);
  });
});

// The AO shell is a BaseWindow hosting the UI in a WebContentsView (#3750), and
// BrowserWindow.getAllWindows() only ever returns BrowserWindow instances. The
// updater walked that registry to push status, so from 2026-08-09 it matched
// nothing and every push was dropped. `invoke` still answered its own sender, so
// updates:getStatus kept working and it read as a caching bug: "Last checked"
// was correct when Settings was reopened and never moved while it was open.
describe("renderer delivery does not depend on the window registry", () => {
  it("pushes status and telemetry through the shell sink, never through BrowserWindow", async () => {
    const { module, autoUpdater, updaterEvents, rendererSend, BrowserWindow, statusMessages, telemetryMessages } =
      await importAutoUpdater();

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    updaterEvents.get("error")?.(new Error("net::ERR_CONNECTION_RESET"));

    // The registry is empty here exactly as it is in the packaged app, so any
    // code that reaches for it delivers nothing.
    expect(BrowserWindow.getAllWindows).not.toHaveBeenCalled();
    expect(statusMessages().length).toBeGreaterThan(0);
    expect(telemetryMessages().length).toBeGreaterThan(0);
    expect(rendererSend).toHaveBeenCalledWith("updates:status", expect.anything());
    expect(rendererSend).toHaveBeenCalledWith("updates:telemetry", expect.anything());
    expect(autoUpdater.checkForUpdates).toHaveBeenCalled();
  });

  it("resolves the sink per send, so a recreated shell still receives pushes", async () => {
    // Caching the first WebContents would silently stop delivery after the
    // window is recreated - the same class of failure, one step later.
    const { module, updaterEvents } = await importAutoUpdater();
    const first: unknown[] = [];
    const second: unknown[] = [];
    let current = first;
    module.setRendererSink(() => ({ send: (_c: string, p: unknown) => current.push(p) }));

    await module.checkForUpdatesNow(stateDir);
    expect(first.length).toBeGreaterThan(0);

    current = second;
    updaterEvents.get("update-downloaded")?.({ version: "2.1.0" });
    expect(second.length).toBeGreaterThan(0);
  });

  it("does not throw when no sink is installed yet", async () => {
    const { module } = await importAutoUpdater();
    module.setRendererSink(() => null);
    await expect(module.checkForUpdatesNow(stateDir)).resolves.toBeUndefined();
  });
});

describe("live download-to-install flow", () => {
  it("publishes starting immediately and refuses duplicate download requests", async () => {
    const { module, autoUpdater, updaterEvents } = await importAutoUpdater();
    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-available")?.({ version: "2.0.0" });
    let finish!: () => void;
    autoUpdater.downloadUpdate.mockImplementation(() => new Promise<void>((resolve) => { finish = resolve; }));
    const download = module.downloadUpdateNow();
    expect(module.getUpdateStatus()).toMatchObject({ state: "downloading", version: "2.0.0" });
    expect(module.getUpdateStatus().percent).toBeUndefined();
    await module.downloadUpdateNow();
    expect(autoUpdater.downloadUpdate).toHaveBeenCalledTimes(1);
    updaterEvents.get("download-progress")?.({ percent: 42.8, transferred: 428, total: 1000 });
    expect(module.getUpdateStatus()).toMatchObject({ state: "downloading", percent: 42, transferred: 428, total: 1000 });
    finish();
    await download;
  });

  it("does not offer installation at 100% or before native macOS readiness", async () => {
    const restore = stubProcess("darwin", process.execPath);
    try {
      const { module, updaterEvents, nativeUpdaterEvents, autoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
      await module.checkForUpdatesNow(stateDir);
      updaterEvents.get("download-progress")?.({ percent: 100, transferred: 1000, total: 1000 });
      expect(module.getUpdateStatus().state).toBe("preparing");
      updaterEvents.get("update-downloaded")?.({ version: "2.0.0" });
      expect(module.getUpdateStatus()).toMatchObject({ state: "preparing", staged: { ready: false } });
      const install = module.quitAndInstallUpdate();
      expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
      nativeUpdaterEvents.get("update-downloaded")?.({}, "notes", "2.0.0");
      expect(module.getUpdateStatus().state).toBe("downloaded");
      await install;
      expect(autoUpdater.quitAndInstall).toHaveBeenCalledTimes(1);
    } finally { restore(); }
  });

  it("never advances the successful check timestamp for a failure", async () => {
    const { module, updaterEvents } = await importAutoUpdater();
    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-not-available")?.();
    const checkedAt = module.getUpdateStatus().checkedAt;
    updaterEvents.get("error")?.(new Error("offline"));
    expect(module.getUpdateStatus()).toMatchObject({ state: "error", checkedAt });
  });
});

it("keeps timed-out native preparation non-installable even after a late event", async () => {
  vi.useFakeTimers();
  const restore = stubProcess("darwin", process.execPath);
  try {
    const { module, updaterEvents, nativeUpdaterEvents, autoUpdater } = await importAutoUpdater(undefined, { nativeReadyManually: true });
    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("update-downloaded")?.({ version: "2.0.0" });
    await vi.advanceTimersByTimeAsync(3 * 60_000);
    expect(module.getUpdateStatus()).toMatchObject({ state: "error", staged: { ready: false } });
    expect(module.getUpdateStatus().message).toContain("stopped responding while preparing");
    await expect(module.quitAndInstallUpdate()).rejects.toThrow(/Close and reopen AO/);
    expect(autoUpdater.quitAndInstall).not.toHaveBeenCalled();
    nativeUpdaterEvents.get("update-downloaded")?.({}, "notes", "2.0.0");
    expect(module.getUpdateStatus().state).toBe("error");
  } finally { restore(); vi.useRealTimers(); }
});
