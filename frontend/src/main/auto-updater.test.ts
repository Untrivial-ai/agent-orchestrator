// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { chmodSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
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
    channel: "",
    allowPrerelease: false,
    allowDowngrade: false,
    autoDownload: false,
    autoInstallOnAppQuit: false,
  };
}

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
  const stateDir = "/tmp/ao-state";

  afterEach(() => {
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

  it("schedules the next automatic check only after the fixed 1-2 hour cadence", async () => {
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

  it("schedules only feature-pin retirement polling when automatic updates are disabled", async () => {
    vi.useFakeTimers();
    const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
    const { module, autoUpdater } = await importAutoUpdater({
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    });

    await module.startAutoUpdates(stateDir);

    expect(autoUpdater.checkForUpdates).not.toHaveBeenCalled();
    expect(setIntervalSpy).toHaveBeenCalledTimes(1);
    expect(setIntervalSpy).toHaveBeenCalledWith(
      expect.any(Function),
      30 * 60 * 1000,
    );
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
      percent: 42,
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
    });
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
    expect(module.getUpdateStatus()).toEqual({
      state: "downloaded",
      version: "2.1.0",
      stagedAt,
      escalated: true,
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
      percent: 64,
    });

    updaterEvents.get("error")?.(err);
    automaticDownload.resolve();
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
    const { module, BrowserWindow, updaterEvents } = await importAutoUpdater();
    const err = new Error("manual feed failed");

    await module.checkForUpdatesNow(stateDir);
    updaterEvents.get("error")?.(err);

    expect(BrowserWindow.getAllWindows).toHaveBeenCalled();
    expect(module.getUpdateStatus()).toEqual({
      state: "error",
      message: "manual feed failed",
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
    expect(module.getUpdateStatus()).toEqual({ state: "idle" });

    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
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

    expect(module.getUpdateStatus()).toEqual({ state: "idle" });
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
    // lone trailing net error stays below the threshold (#3526).
    expect(module.getUpdateStatus()).toEqual({ state: "idle" });
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
    });
  });

  it("stamps the nudge on getUpdateStatus even when no broadcast carried it", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { module, autoUpdater, updaterEvents, statusMessages } =
      await importAutoUpdater();
    // No checking-for-update: there is no prior status to restore, so the
    // failures produce no status broadcast at all.
    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("error")?.(new Error("net::ERR_FAILED"));
      return Promise.resolve();
    });

    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);
    await module.startAutoUpdates(stateDir);

    expect(statusMessages()).toEqual([]);
    expect(module.getUpdateStatus()).toEqual({
      state: "idle",
      staleCheckNudge: true,
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
    });

    autoUpdater.checkForUpdates.mockImplementation(() => {
      updaterEvents.get("checking-for-update")?.();
      updaterEvents.get("update-not-available")?.();
      return Promise.resolve();
    });
    await module.startAutoUpdates(stateDir);

    expect(module.getUpdateStatus()).toEqual({ state: "not-available" });
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
      state: "available",
      version: "1.9.0",
    });

    automaticCheck.resolve();
    await Promise.all([startPromise, featureCheck]);

    expect(module.getUpdateStatus()).toEqual({
      state: "available",
      version: "2.0.0-pr2709.1",
      requestId: "feature-2709",
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

  it("starts and stops the hourly scheduler when settings are enabled at runtime", async () => {
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
    expect(setIntervalSpy).toHaveBeenCalledTimes(1);

    await module.setUpdateSettings(stateDir, { ...current, enabled: true });
    expect(setIntervalSpy.mock.calls.map(([, delay]) => delay)).toContain(
      60 * 60 * 1000,
    );

    await module.setUpdateSettings(stateDir, { ...current, enabled: false });
    expect(clearIntervalSpy).toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
    expect(autoUpdater.checkForUpdates).not.toHaveBeenCalled();
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
    expect(autoUpdater.checkForUpdates).toHaveBeenCalledTimes(2);
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
});

describe("returnToHome", () => {
  const stateDir = "/tmp/ao-state";

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
