import { autoUpdater } from "electron-updater";
import { app, BrowserWindow, dialog } from "electron";
import { accessSync, constants, existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import path from "node:path";
import {
  readUpdateSettings,
  updateUpdateSettings,
  writeUpdateSettings,
  UPDATE_SETTINGS_FILE_NAME,
  type UpdateChannel,
  type UpdateSettings,
  type UpdateStatus,
} from "./update-settings";
import { reconcileFeaturePin } from "./feature-builds";
import { evaluateEscalation } from "./escalation-evaluator";
import {
  updateFailureOutcome,
  type UpdateOutcome,
  type UpdatePhase,
  type UpdateTrigger,
} from "../shared/update-telemetry";

// reconcileAndPersist clears a pinned feature build whose PR has been retired
// (merged/closed/deleted/expired) and persists the change, so the next check
// resolves the home channel and moves the user back automatically. A fetch
// failure keeps the pin (see reconcileFeaturePin). Returns the effective settings.
async function reconcileAndPersist(
  stateDir: string,
  settings: UpdateSettings,
): Promise<UpdateSettings> {
  const checkedPr = settings.feature?.pr;
  const rec = await reconcileFeaturePin(settings);
  if (!rec.cleared || checkedPr === undefined) return rec.settings;

  let cleared = false;
  const latest = await updateUpdateSettings(stateDir, (current) => {
    if (current.feature?.pr !== checkedPr) return current;
    cleared = true;
    return { ...current, feature: null };
  });
  if (cleared) {
    console.info(
      "[feature-builds] pinned PR retired; cleared pin, falling back to home channel",
    );
  }
  return latest;
}

// configureFeed sets the update channel on electron-updater. The repo/owner
// are loaded automatically from app-update.yml (written by forge.config.ts's
// postPackage hook into the app's Resources dir at build time). No runtime env
// or setFeedURL call is needed; electron-updater reads the bundled yml on first
// checkForUpdates.
//
// When settings.feature is set, the feed tracks the pr<N> prerelease channel
// (e.g. "pr2270") with allowPrerelease and allowDowngrade enabled so the user
// can switch back to stable after testing. Otherwise falls back to the home
// channel logic (latest vs nightly).
export function configureFeed(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): void {
  if (settings.feature !== null && settings.feature !== undefined) {
    // Feature build: pin to the pr<N> semver prerelease identifier channel.
    autoUpdater.channel = `pr${settings.feature.pr}`;
    autoUpdater.allowPrerelease = true;
    autoUpdater.allowDowngrade = true; // allows switching back to stable/nightly
    return;
  }

  const channel: UpdateChannel = settings.channel;
  autoUpdater.channel = channel; // "latest" | "nightly"
  // Nightly builds ship as GitHub *prereleases*. With allowPrerelease false
  // (the default) electron-updater only inspects the latest NON-prerelease
  // release and looks for nightly-mac.yml there, which 404s. Enable prerelease
  // scanning on the nightly channel only; stable must never pull prereleases.
  autoUpdater.allowPrerelease = channel === "nightly";
  autoUpdater.allowDowngrade = true; // permits a nightly -> stable channel switch
}

let lastStatus: UpdateStatus = { state: "idle" };
let independentStatusRevision = 0;
let eventsWired = false;

// Staged-update tracking for the escalation evaluator: set on update-downloaded,
// re-evaluated every 30 minutes while the update sits uninstalled. stateDir is
// captured from whichever entry point wired the events (both receive it).
let stagedVersion: string | undefined;
let stagedAtMs: number | undefined;
let stagedEscalated = false;
let stagedRequestId: string | undefined;
let escalationTimer: ReturnType<typeof setInterval> | undefined;
let escalationStateDir: string | undefined;
const AUTOMATIC_UPDATE_CHECK_INTERVAL_MS = 60 * 60 * 1000;
let automaticUpdateTimer: ReturnType<typeof setInterval> | undefined;
type UpdaterOperation =
  | "automatic-check"
  | "manual-check"
  | "manual-download"
  | "settings-write"
  | "return-home";
let activeUpdaterOperation: UpdaterOperation | undefined;
let activeUpdaterRequestId: string | undefined;
let automaticCheckPreviousStatus:
  { status: UpdateStatus; independentRevision: number } | undefined;
let updaterOperationQueue: Promise<void> = Promise.resolve();
let automaticCheckInFlight = false;
// Which stage the active operation reached, and what it was fetching. Tracked
// here because the renderer cannot know either: automatic failures never
// broadcast a status, and error statuses carry no version.
let activeUpdaterPhase: UpdatePhase = "check";
let pendingUpdateVersion: string | undefined;

// emitUpdateOutcome pushes an update outcome to renderers on a channel separate
// from "updates:status", so suppressing a status for UI reasons (as the
// automatic path does) never suppresses the telemetry for it.
function emitUpdateOutcome(outcome: UpdateOutcome): void {
  for (const win of BrowserWindow.getAllWindows()) {
    if (!win.isDestroyed()) win.webContents.send("updates:telemetry", outcome);
  }
}

function activeUpdateTrigger(): UpdateTrigger {
  return activeUpdaterOperation === "automatic-check" ? "automatic" : "manual";
}

function emitUpdateFailure(err: unknown): void {
  const message =
    err instanceof Error ? err.message : err === undefined ? undefined : String(err);
  emitUpdateOutcome(
    updateFailureOutcome(message, activeUpdaterPhase, activeUpdateTrigger(), pendingUpdateVersion),
  );
}

// broadcast pushes the latest update status to every renderer window so the
// Global Settings Updates section can reflect check/download progress live.
function broadcast(
  status: UpdateStatus,
  owner: "independent" | "automatic-operation" = "independent",
): void {
  if (owner === "independent") {
    independentStatusRevision += 1;
    if (
      activeUpdaterOperation === "automatic-check" &&
      automaticCheckPreviousStatus !== undefined
    ) {
      automaticCheckPreviousStatus = {
        status,
        independentRevision: independentStatusRevision,
      };
    }
  }
  lastStatus = status;
  for (const win of BrowserWindow.getAllWindows()) {
    if (!win.isDestroyed()) win.webContents.send("updates:status", status);
  }
}

function withActiveRequest(status: UpdateStatus): UpdateStatus {
  return activeUpdaterRequestId === undefined
    ? status
    : { ...status, requestId: activeUpdaterRequestId };
}

function broadcastUpdaterStatus(status: UpdateStatus): void {
  const ownedStatus = withActiveRequest(status);
  broadcast(
    ownedStatus,
    activeUpdaterOperation === "automatic-check"
      ? "automatic-operation"
      : "independent",
  );
}

// --- Read-only release-feed helpers (packaged app only; every failure is silent).
// These regex-parse flat keys out of electron-builder yml files on purpose: no
// yaml dependency, and a parse miss just means "no info", never an error state
// (see issue #2270 for why this path must not broadcast errors).

/** Owner/repo from the bundled app-update.yml; undefined in dev or on any failure. */
async function readAppUpdateYml(): Promise<
  { owner: string; repo: string } | undefined
> {
  if (!app.isPackaged) return undefined;
  try {
    const yml = await readFile(
      path.join(process.resourcesPath, "app-update.yml"),
      "utf8",
    );
    const owner = /^owner:\s*(.+)$/m.exec(yml)?.[1]?.trim();
    const repo = /^repo:\s*(.+)$/m.exec(yml)?.[1]?.trim();
    return owner && repo ? { owner, repo } : undefined;
  } catch {
    return undefined;
  }
}

/** Platform suffix matching the feed.mjs naming convention. */
function platformSuffix(): string {
  if (process.platform === "darwin") return "-mac";
  if (process.platform === "linux") return "-linux";
  return "";
}

/** Latest stable version via GitHub's /releases/latest redirect; undefined on any failure. */
async function fetchLatestStableVersion(
  owner: string,
  repo: string,
): Promise<string | undefined> {
  const url = `https://github.com/${owner}/${repo}/releases/latest/download/latest${platformSuffix()}.yml`;
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(10000) });
    if (!res.ok) return undefined;
    return (
      /^version:\s*(.+)$/m.exec(await res.text())?.[1]?.trim() || undefined
    );
  } catch {
    return undefined;
  }
}

/** important flag on the staged nightly's release yml; false when absent, 404, or any failure. */
async function fetchNightlyImportant(
  owner: string,
  repo: string,
  version: string,
): Promise<boolean> {
  const url = `https://github.com/${owner}/${repo}/releases/download/v${version}/nightly${platformSuffix()}.yml`;
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(10000) });
    if (!res.ok) return false;
    return /^important:\s*true\s*$/m.test(await res.text());
  } catch {
    return false;
  }
}

// stagedDownloadedStatus rebuilds the enriched downloaded status from module
// state, so transient check states can restore the row without recomputing.
function stagedDownloadedStatus(): UpdateStatus {
  return {
    state: "downloaded",
    version: stagedVersion,
    stagedAt: stagedAtMs,
    escalated: stagedEscalated,
    ...(stagedRequestId === undefined ? {} : { requestId: stagedRequestId }),
  };
}

// runEscalationCheck re-reads settings and feeds, then rebroadcasts the
// downloaded status with a fresh escalated flag. The timer is keyed on a build
// being staged (stagedAtMs set), NOT on lastStatus: a manual re-check flips
// lastStatus through checking/available while the build stays staged, and that
// must not kill the loop. Never broadcasts an error state: every failure
// degrades to escalated staying put.
async function runEscalationCheck(): Promise<void> {
  if (stagedAtMs === undefined) {
    stopEscalationTimer();
    return;
  }
  if (escalationStateDir === undefined) return;
  // A newer build is being pulled; let its progress own the status stream.
  if (lastStatus.state === "downloading") return;
  try {
    const settings = await readUpdateSettings(escalationStateDir);
    let important = false;
    let latestStableVersion: string | undefined;
    const coords = await readAppUpdateYml();
    if (coords && settings.channel === "nightly") {
      // stagedVersion is only needed by the important-flag fetch; the
      // latest-channel 48h rule (and the behind-stable check) work without it.
      [latestStableVersion, important] = await Promise.all([
        fetchLatestStableVersion(coords.owner, coords.repo),
        stagedVersion !== undefined
          ? fetchNightlyImportant(coords.owner, coords.repo, stagedVersion)
          : Promise.resolve(false),
      ]);
    }
    stagedEscalated = evaluateEscalation({
      channel: settings.channel,
      stagedAt: stagedAtMs,
      now: Date.now(),
      important,
      runningVersion: app.getVersion(),
      latestStableVersion,
    });
    broadcast(stagedDownloadedStatus());
  } catch (err) {
    console.debug("escalation check skipped:", err);
  }
}

function stopEscalationTimer(): void {
  if (escalationTimer !== undefined) {
    clearInterval(escalationTimer);
    escalationTimer = undefined;
  }
}

function restoreAutomaticCheckPreviousStatus(): void {
  if (automaticCheckPreviousStatus === undefined) return;
  const { status, independentRevision } = automaticCheckPreviousStatus;
  automaticCheckPreviousStatus = undefined;
  if (independentStatusRevision !== independentRevision) return;
  broadcast(status);
}

async function runSerializedUpdaterOperation(
  operation: UpdaterOperation,
  runOperation: () => Promise<void>,
  requestId?: string,
): Promise<void> {
  const run = async () => {
    activeUpdaterOperation = operation;
    activeUpdaterRequestId = requestId;
    activeUpdaterPhase = operation === "manual-download" ? "download" : "check";
    pendingUpdateVersion = undefined;
    try {
      await runOperation();
    } finally {
      activeUpdaterOperation = undefined;
      activeUpdaterRequestId = undefined;
      if (operation === "automatic-check")
        automaticCheckPreviousStatus = undefined;
    }
  };
  const queued = updaterOperationQueue.then(run, run);
  updaterOperationQueue = queued.catch(() => undefined);
  await queued;
}

// Feature-pin retirement polling: while a build is pinned to a pr<N> channel,
// re-check every 30 minutes whether that PR has since been retired, so a
// long-running session notices a merge/close without waiting for a relaunch.
let retirementPollTimer: ReturnType<typeof setInterval> | undefined;
let retirementPollInFlight = false;

// startRetirementPollTimer is idempotent (guards against stacking multiple
// intervals across repeated startAutoUpdates calls) and runs independently of
// the auto-update opt-in, since a disabled user can still be pinned.
// ponytail: fixed 30-min cadence, not an aggressive poll; runRetirementPoll
// returns immediately whenever there's no pin, so idle cost is one settings read.
function startRetirementPollTimer(stateDir: string): void {
  if (retirementPollTimer !== undefined) return;
  retirementPollTimer = setInterval(
    () => void requestRetirementPoll(stateDir),
    30 * 60 * 1000,
  );
  retirementPollTimer.unref?.();
}

async function requestRetirementPoll(stateDir: string): Promise<void> {
  if (retirementPollInFlight) return;
  retirementPollInFlight = true;
  try {
    await runRetirementPoll(stateDir);
  } finally {
    retirementPollInFlight = false;
  }
}

async function runRetirementPoll(stateDir: string): Promise<void> {
  try {
    await runSerializedUpdaterOperation("settings-write", async () => {
      const before = await readUpdateSettings(stateDir);
      if (before.feature === null || before.feature === undefined) return;
      const settings = await reconcileAndPersist(stateDir, before);
      if (settings.feature === null || settings.feature === undefined) {
        // Pin was cleared: drop the now-dead pr<N> channel right away instead of
        // waiting for the next manual or launch-time check to notice.
        configureFeed(settings);
      }
    });
  } catch (err) {
    // Background poll: never throw, just skip this round.
    console.debug("retirement poll skipped:", err);
  }
}

// isManifest404Error checks whether the error is a 404 on a release
// manifest YAML file — a routine condition that should not be surfaced
// to users as an error dialog.
function isManifest404Error(err: unknown): boolean {
  const e = err as Error & { code?: string };
  if (e.code === "ERR_UPDATER_CHANNEL_FILE_NOT_FOUND") return true;
  const msg = e.message ?? "";
  return msg.includes("HttpError: 404") && /\.yml\b/i.test(msg);
}

// wireUpdaterEvents registers electron-updater listeners once and forwards each
// to the renderer as an UpdateStatus. Idempotent: safe to call on every entry
// point (launch auto-check and manual check).
function wireUpdaterEvents(): void {
  if (eventsWired) return;
  eventsWired = true;
  // With a build staged, "checking" briefly hides the sidebar restart row; that
  // is acceptable and self-healing: the available / not-available handlers below
  // restore the enriched downloaded status right after.
  autoUpdater.on("checking-for-update", () => {
    if (
      activeUpdaterOperation === "automatic-check" &&
      automaticCheckPreviousStatus === undefined
    ) {
      const status = lastStatus;
      broadcastUpdaterStatus({ state: "checking" });
      automaticCheckPreviousStatus = {
        status,
        independentRevision: independentStatusRevision,
      };
      return;
    }
    broadcastUpdaterStatus({ state: "checking" });
  });
  autoUpdater.on("update-available", (info) => {
    // A manual re-check reports the already-staged build as merely "available"
    // (autoDownload is off on that path). It is still in cache and installs on
    // quit, so keep the richer downloaded status instead of hiding the row.
    if (stagedAtMs !== undefined && info?.version === stagedVersion) {
      broadcastUpdaterStatus(stagedDownloadedStatus());
      return;
    }
    pendingUpdateVersion = info?.version;
    broadcastUpdaterStatus({ state: "available", version: info?.version });
  });
  autoUpdater.on("update-not-available", () => {
    broadcastUpdaterStatus({ state: "not-available" });
    // The staged build outlives a "nothing newer" answer (e.g. after a channel
    // switch); follow up so the restart row returns.
    if (stagedAtMs !== undefined)
      broadcastUpdaterStatus(stagedDownloadedStatus());
  });
  autoUpdater.on("download-progress", (p) => {
    // Any progress proves the check succeeded, so a later error is a download
    // failure even when the operation began life as a check.
    activeUpdaterPhase = "download";
    return broadcastUpdaterStatus({
      state: "downloading",
      percent: Math.max(0, Math.min(100, Math.round(p?.percent ?? 0))),
    });
  });
  autoUpdater.on("update-downloaded", (info) => {
    emitUpdateOutcome({
      event: "ao.renderer.update_downloaded",
      phase: "download",
      trigger: activeUpdateTrigger(),
      ...(info?.version ? { to_version: info.version } : {}),
    });
    stagedVersion = info?.version;
    stagedAtMs = Date.now();
    stagedEscalated = false;
    stagedRequestId = activeUpdaterRequestId;
    automaticCheckPreviousStatus = undefined;
    // A completed automatic download advances the independent baseline; a
    // renderer-requested download additionally carries its request ownership.
    broadcast(withActiveRequest(stagedDownloadedStatus()));
    // Evaluate now (nightly can escalate immediately), then every 30 minutes
    // while the update sits uninstalled. unref so the timer never holds the
    // process open on quit.
    void runEscalationCheck();
    stopEscalationTimer();
    escalationTimer = setInterval(
      () => void runEscalationCheck(),
      30 * 60 * 1000,
    );
    escalationTimer.unref?.();
  });
  autoUpdater.on("error", (err) => {
    // Never crash on update failure (offline, unsigned macOS, etc.).
    // Automatic failures restore the previous status so the UI does not flash
    // an error the user never asked for. That suppression is a UI decision and
    // must not suppress the telemetry: automatic checks run hourly and are the
    // main way an install goes silently stale.
    emitUpdateFailure(err);
    if (activeUpdaterOperation === "automatic-check") {
      console.error("auto-update check failed:", err);
      restoreAutomaticCheckPreviousStatus();
      return;
    }
    // Manifest 404 (missing latest-mac.yml etc.) is a routine condition,
    // not an actionable error — log and broadcast a terminal state so
    // the renderer does not hang.
    if (isManifest404Error(err)) {
      console.info("update check failed (404, manifest not found):", err);
      if (activeUpdaterOperation === "manual-download") {
        broadcast(
          withActiveRequest({
            state: "error",
            message:
              "Download failed — the update file was not found on the server.",
          }),
        );
      } else if (stagedAtMs !== undefined) {
        broadcast(stagedDownloadedStatus());
      } else {
        broadcast(
          withActiveRequest({
            state: "error",
            message:
              "Couldn't check for updates — the update information was not found on the server.",
          }),
        );
      }
      return;
    }
    // All other errors: broadcast so the user knows something went wrong.
    broadcast(
      withActiveRequest({
        state: "error",
        message: err?.message ?? String(err),
      }),
    );
  });
}

export function getUpdateStatus(): UpdateStatus {
  return lastStatus;
}

async function runAutomaticUpdateCheck(stateDir: string): Promise<boolean> {
  let shouldSchedule = true;
  try {
    await runSerializedUpdaterOperation("automatic-check", async () => {
      const settings = await reconcileAndPersist(
        stateDir,
        await readUpdateSettings(stateDir),
      );
      if (!settings.enabled) {
        shouldSchedule = false;
        // Stop while this serialized snapshot still owns updater ordering. A
        // later settings write that enables updates can then schedule safely.
        stopPeriodicAutomaticUpdateCheck();
        return;
      }

      escalationStateDir = stateDir;
      wireUpdaterEvents();
      configureFeed(settings);
      autoUpdater.autoDownload = true;
      autoUpdater.autoInstallOnAppQuit = true;
      const result = await autoUpdater.checkForUpdates();
      if (result?.downloadPromise) await result.downloadPromise;
    });
  } catch (err) {
    console.error("auto-update check failed:", err);
  }
  return shouldSchedule;
}

function schedulePeriodicAutomaticUpdateCheck(stateDir: string): void {
  if (automaticUpdateTimer !== undefined) return;
  automaticUpdateTimer = setInterval(
    () => void requestAutomaticUpdateCheck(stateDir),
    AUTOMATIC_UPDATE_CHECK_INTERVAL_MS,
  );
  automaticUpdateTimer.unref?.();
}

function stopPeriodicAutomaticUpdateCheck(): void {
  if (automaticUpdateTimer === undefined) return;
  clearInterval(automaticUpdateTimer);
  automaticUpdateTimer = undefined;
}

function reconcileAutomaticUpdateSchedule(
  stateDir: string,
  enabled: boolean,
): void {
  if (enabled) schedulePeriodicAutomaticUpdateCheck(stateDir);
  else stopPeriodicAutomaticUpdateCheck();
}

async function requestAutomaticUpdateCheck(
  stateDir: string,
): Promise<boolean | undefined> {
  if (automaticCheckInFlight) return undefined;
  automaticCheckInFlight = true;
  try {
    return await runAutomaticUpdateCheck(stateDir);
  } finally {
    automaticCheckInFlight = false;
  }
}

// startAutoUpdates configures electron-updater from the user's ~/.ao settings.
// It is a thin shell: all policy (channel, opt-in) comes from update-settings.
// Caller guards on app.isPackaged.
export async function startAutoUpdates(stateDir: string): Promise<void> {
  startRetirementPollTimer(stateDir);
  const shouldSchedule = await requestAutomaticUpdateCheck(stateDir);
  if (shouldSchedule === true) schedulePeriodicAutomaticUpdateCheck(stateDir);
}

async function persistUpdaterSettings(
  stateDir: string,
  settings: UpdateSettings,
): Promise<void> {
  await writeUpdateSettings(stateDir, settings);
  configureFeed(settings);
  reconcileAutomaticUpdateSchedule(stateDir, settings.enabled);
}

/** Persist settings and reconcile the live updater feed/timer as one updater operation. */
export async function setUpdateSettings(
  stateDir: string,
  settings: UpdateSettings,
): Promise<void> {
  await runSerializedUpdaterOperation("settings-write", () =>
    persistUpdaterSettings(stateDir, settings),
  );
}

export interface UpdateCheckOptions {
  settings?: UpdateSettings;
  requestId?: string;
}

// checkForUpdatesNow runs a manual update check regardless of the auto-update
// opt-in, so a user who never enabled auto-updates can still pull the latest
// build from Settings. It does NOT auto-download — the user clicks Update — and
// reports progress via the broadcast status. Updates only work in the packaged,
// signed app; in dev electron-updater has no feed, so surface that plainly.
export async function checkForUpdatesNow(
  stateDir: string,
  options: UpdateCheckOptions = {},
): Promise<void> {
  escalationStateDir = stateDir;
  wireUpdaterEvents();
  if (!app.isPackaged) {
    emitUpdateOutcome({
      event: "ao.renderer.update_unsupported",
      phase: activeUpdaterPhase,
      trigger: activeUpdateTrigger(),
      error_category: "not_supported",
    });
    broadcast({
      state: "unsupported",
      message: "Updates are only available in the installed app.",
      requestId: options.requestId,
    });
    return;
  }
  try {
    await runSerializedUpdaterOperation(
      "manual-check",
      async () => {
        if (options.settings)
          await writeUpdateSettings(stateDir, options.settings);
        const settings = await reconcileAndPersist(
          stateDir,
          options.settings ?? (await readUpdateSettings(stateDir)),
        );
        reconcileAutomaticUpdateSchedule(stateDir, settings.enabled);
        configureFeed(settings);
        autoUpdater.autoDownload = false;
        autoUpdater.autoInstallOnAppQuit = true;
        broadcastUpdaterStatus({ state: "checking" });
        await autoUpdater.checkForUpdates();
      },
      options.requestId,
    );
  } catch (err) {
    if (isManifest404Error(err)) {
      console.info("manual update check failed:", err);
      broadcast({
        state: "error",
        message:
          "Couldn't check for updates — the update information was not found on the server.",
        requestId: options.requestId,
      });
      if (stagedAtMs !== undefined) broadcast(stagedDownloadedStatus());
    } else {
      broadcast({
        state: "error",
        message: (err as Error)?.message ?? "Update check failed",
        requestId: options.requestId,
      });
    }
  }
}

// returnToHome clears any pinned feature build and resolves the home channel in a
// SINGLE updater-serialized operation. Clearing and checking must share one
// operation on updaterOperationQueue: a separate clear (on the settings queue)
// could interleave with a queued settings-write or an in-flight check and see the
// stale pin restored, leaving the app on the pr<N> feed. The pin is cleared against
// persisted state, so this never depends on renderer form hydration.
export async function returnToHome(
  stateDir: string,
  requestId?: string,
): Promise<void> {
  escalationStateDir = stateDir;
  wireUpdaterEvents();
  if (!app.isPackaged) {
    emitUpdateOutcome({
      event: "ao.renderer.update_unsupported",
      phase: activeUpdaterPhase,
      trigger: activeUpdateTrigger(),
      error_category: "not_supported",
    });
    broadcast({
      state: "unsupported",
      message: "Updates are only available in the installed app.",
      requestId,
    });
    return;
  }
  try {
    await runSerializedUpdaterOperation(
      "return-home",
      async () => {
        const cleared = await updateUpdateSettings(stateDir, (current) =>
          current.feature ? { ...current, feature: null } : current,
        );
        const settings = await reconcileAndPersist(stateDir, cleared);
        reconcileAutomaticUpdateSchedule(stateDir, settings.enabled);
        configureFeed(settings);
        autoUpdater.autoDownload = false;
        autoUpdater.autoInstallOnAppQuit = true;
        broadcastUpdaterStatus({ state: "checking" });
        await autoUpdater.checkForUpdates();
      },
      requestId,
    );
  } catch (err) {
    broadcast({
      state: "error",
      message: (err as Error)?.message ?? "Return failed",
      requestId,
    });
  }
}

// downloadUpdateNow starts downloading the update found by checkForUpdatesNow.
export async function downloadUpdateNow(requestId?: string): Promise<void> {
  wireUpdaterEvents();
  if (!app.isPackaged) {
    emitUpdateOutcome({
      event: "ao.renderer.update_unsupported",
      phase: activeUpdaterPhase,
      trigger: activeUpdateTrigger(),
      error_category: "not_supported",
    });
    broadcast({
      state: "unsupported",
      message: "Updates are only available in the installed app.",
      requestId,
    });
    return;
  }
  try {
    await runSerializedUpdaterOperation(
      "manual-download",
      async () => {
        await autoUpdater.downloadUpdate();
      },
      requestId,
    );
  } catch (err) {
    if (isManifest404Error(err)) {
      console.error("update download failed:", err);
      broadcast({
        state: "error",
        message:
          "Download failed — the update file was not found on the server.",
        requestId,
      });
    } else {
      broadcast({
        state: "error",
        message: (err as Error)?.message ?? "Download failed",
        requestId,
      });
    }
  }
}

// --- macOS App Translocation / read-only bundle guard ---------------------
// Squirrel.Mac (electron-updater's macOS install backend) replaces the running
// .app bundle in place, so it needs write access to the bundle's real,
// installed location. Two situations break that silently: the app running
// translocated (macOS "App Translocation" runs an unmoved, freshly-downloaded
// .app from a randomized read-only mirror under
// /private/var/folders/.../AppTranslocation/<uuid>/d/ instead of its real
// path) or a bundle that is otherwise non-writable (read-only volume, missing
// permission bit). In either case quitAndInstall() silently no-ops: no
// restart, no error, no feedback (#3527). quitAndInstallUpdate() below checks
// both before installing.
//
// Kept as small, pure functions that take the path as a parameter instead of
// reading process.execPath internally, so they're unit-testable with
// fabricated path strings without a real macOS bundle -- the same
// dependency-injection convention keybinding-settings.ts's
// coerceKeybindingOverrides(raw, isMac) and daemon-launch.ts's
// resolveDaemonLaunch(...) already use in this directory.

/** True when execPath is running out of macOS's App Translocation mirror. */
export function isTranslocatedPath(execPath: string): boolean {
  return execPath.includes("/AppTranslocation/");
}

// Walk up from the executable to the enclosing `.app` bundle root:
// .../Agent Orchestrator.app/Contents/MacOS/<exe> -> .../Agent Orchestrator.app.
// Same three-level walk as resolveBundlePath() in main.ts, for the same
// reason (app.getAppPath() would return the app.asar path inside the bundle,
// not the bundle directory itself) -- but done via "/"-delimited string
// splitting rather than node:path, since macOS bundle paths are always POSIX
// and path.resolve/path.join follow the *host* OS's separator rules, which
// would make this non-deterministic under test on a non-POSIX host. Same
// reasoning as daemon-launch.ts's joinPath in this repo.
export function resolveMacBundleRoot(execPath: string): string {
  const segments = execPath.split("/");
  return segments.slice(0, Math.max(segments.length - 3, 0)).join("/");
}

// True unless the bundle root cannot be written to (read-only volume, missing
// permission bit) -- the other way Squirrel.Mac silently fails besides
// translocation. checkAccess is injected (defaulting to a real fs.accessSync
// check) so tests can fabricate both outcomes without a real filesystem.
export function isBundleWritable(
  bundleRoot: string,
  checkAccess: (targetPath: string) => void = (p) =>
    accessSync(p, constants.W_OK),
): boolean {
  try {
    checkAccess(bundleRoot);
    return true;
  } catch {
    return false;
  }
}

// quitAndInstallUpdate installs a downloaded update and relaunches. isSilent
// false keeps the installer UI on Windows; isForceRunAfter relaunches the app.
//
// On macOS specifically, guard against the silent-no-op cases above: if the
// bundle is translocated or otherwise non-writable, show an actionable dialog
// instead of calling quitAndInstall(). The staged update and update settings
// are left untouched (nothing here writes state), so the install can simply
// be retried once the user has moved the app to /Applications and relaunched.
//
// isMac/execPath/checkAccess default to the real process so the existing call
// site (main.ts's "updates:install" IPC handler, invoked with no arguments)
// behaves exactly as before; tests override them to exercise the guard
// without a real macOS bundle.
export function quitAndInstallUpdate(
  isMac: boolean = process.platform === "darwin",
  execPath: string = process.execPath,
  checkAccess?: (targetPath: string) => void,
): void {
  if (!app.isPackaged) return;
  if (isMac) {
    const bundleRoot = resolveMacBundleRoot(execPath);
    if (
      isTranslocatedPath(execPath) ||
      !isBundleWritable(bundleRoot, checkAccess)
    ) {
      void dialog.showMessageBox({
        type: "warning",
        buttons: ["OK"],
        message: "Can't install this update yet",
        detail:
          "Agent Orchestrator is running from a read-only location -- this usually means it launched straight from a DMG or Downloads without being moved to Applications. Move Agent Orchestrator.app to your Applications folder, relaunch it, and Restart to update again.",
      });
      return;
    }
  }
  autoUpdater.quitAndInstall(false, true);
}

// ensureUpdatePrefs prompts once (first run, before any settings file exists)
// for auto-update opt-in + channel, with a nightly instability disclaimer.
export async function ensureUpdatePrefs(stateDir: string): Promise<void> {
  if (existsSync(path.join(stateDir, UPDATE_SETTINGS_FILE_NAME))) return;

  const optIn = await dialog.showMessageBox({
    type: "question",
    buttons: ["Enable auto-updates", "Not now"],
    defaultId: 0,
    cancelId: 1,
    message: "Keep Agent Orchestrator up to date automatically?",
    detail: "You can change this later in Settings.",
  });
  if (optIn.response !== 0) {
    await writeUpdateSettings(stateDir, {
      enabled: false,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    });
    return;
  }

  const chan = await dialog.showMessageBox({
    type: "question",
    buttons: ["Stable", "Nightly"],
    defaultId: 0,
    cancelId: 0,
    message: "Which update channel?",
    detail: "Stable is released and tested. Nightly is the newest daily build.",
  });
  if (chan.response !== 1) {
    await writeUpdateSettings(stateDir, {
      enabled: true,
      channel: "latest",
      nightlyAck: false,
      feature: null,
    });
    return;
  }

  const ack = await dialog.showMessageBox({
    type: "warning",
    buttons: ["I understand, use Nightly", "Use Stable instead"],
    defaultId: 1,
    cancelId: 1,
    message: "Nightly builds can be unstable",
    detail:
      "Nightly is built every day and may be broken or lose data. Only use it if you are comfortable with that.",
  });
  await writeUpdateSettings(
    stateDir,
    ack.response === 0
      ? { enabled: true, channel: "nightly", nightlyAck: true, feature: null }
      : { enabled: true, channel: "latest", nightlyAck: false, feature: null },
  );
}
