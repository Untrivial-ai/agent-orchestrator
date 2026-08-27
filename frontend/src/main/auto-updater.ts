import { autoUpdater } from "electron-updater";
import { app, BrowserWindow, dialog } from "electron";
import { accessSync, constants as fsConstants, existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import path from "node:path";
import semver from "semver";
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
  isNetErrorMessage,
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
const STABLE_AUTOMATIC_UPDATE_CHECK_INTERVAL_MS = 60 * 60 * 1000;
const NIGHTLY_AUTOMATIC_UPDATE_CHECK_INTERVAL_MS = 15 * 60 * 1000;
let automaticUpdateTimer: ReturnType<typeof setInterval> | undefined;
let automaticUpdateTimerIntervalMs: number | undefined;
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
// Consecutive automatic-check failures from Chromium's network stack
// (net::ERR_*): a wedged stack fails every updater request until the app
// restarts, and automatic failures are UI-suppressed, so the install goes
// silently stale (#3526). At the threshold, statuses carry staleCheckNudge so
// the renderer can suggest a restart.
const STALE_CHECK_NUDGE_THRESHOLD = 3;
let consecutiveAutomaticNetFailures = 0;
// Consecutive automatic-check failures of ANY kind. The net:: streak above
// exists to suggest a restart, and it resets on every non-net error, so the
// failure mode that actually strands an install — a manifest 404 on every
// check — can never trip it. This counter does not care why the check failed:
// past the threshold the renderer is told, because an updater that has failed
// this many times in a row is indistinguishable from a healthy one otherwise.
const FAILING_CHECK_THRESHOLD = 3;
let consecutiveAutomaticCheckFailures = 0;
let automaticCheckFailureCounted = false;
let failingChecksPublished = false;
// One automatic check can both emit an "error" event and reject
// checkForUpdates(); count that as a single failure.
let automaticCheckNetFailureCounted = false;
// Which stage the active operation reached, and what it was fetching. Tracked
// here because the renderer cannot know either: automatic failures never
// broadcast a status, and error statuses carry no version.
let activeUpdaterPhase: UpdatePhase = "check";
let pendingUpdateVersion: string | undefined;
// Session-scoped time of the most recent completed feed check. Packaged apps
// check at launch when automatic updates are enabled, while disabled installs
// truthfully remain "not checked yet" until the user asks.
let lastCheckedAtMs: number | undefined;

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
  const statusWithCheckTime: UpdateStatus =
    lastCheckedAtMs === undefined || status.checkedAt !== undefined
      ? status
      : { ...status, checkedAt: lastCheckedAtMs };
  const stamped: UpdateStatus = {
    ...statusWithCheckTime,
    ...(consecutiveAutomaticNetFailures >= STALE_CHECK_NUDGE_THRESHOLD
      ? { staleCheckNudge: true }
      : {}),
    ...(consecutiveAutomaticCheckFailures >= FAILING_CHECK_THRESHOLD
      ? { checksFailing: true }
      : {}),
  };
  if (owner === "independent") {
    independentStatusRevision += 1;
    if (
      activeUpdaterOperation === "automatic-check" &&
      automaticCheckPreviousStatus !== undefined
    ) {
      automaticCheckPreviousStatus = {
        status: stamped,
        independentRevision: independentStatusRevision,
      };
    }
  }
  lastStatus = stamped;
  for (const win of BrowserWindow.getAllWindows()) {
    if (!win.isDestroyed()) win.webContents.send("updates:status", stamped);
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

function broadcastCompletedCheck(status: UpdateStatus): void {
  lastCheckedAtMs = Date.now();
  broadcastUpdaterStatus(status);
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

interface GitHubReleaseSummary {
  tag_name: string;
  draft: boolean;
  prerelease: boolean;
  assets?: Array<{ name?: string }>;
}

/**
 * Resolve a completed Nightly release through GitHub's API. electron-updater's
 * GitHub provider discovers prereleases through releases.atom, which can lag
 * behind a just-published release even when the release and manifest are ready.
 * This is used only for user-requested checks; failures fall back to the normal
 * provider so API rate limits or an outage never break update checks.
 */
async function fetchLatestCompletedNightlyTag(
  owner: string,
  repo: string,
): Promise<string | undefined> {
  try {
    const response = await fetch(
      `https://api.github.com/repos/${owner}/${repo}/releases?per_page=100`,
      {
        cache: "no-store",
        headers: {
          Accept: "application/vnd.github+json",
          "X-GitHub-Api-Version": "2022-11-28",
          "User-Agent": `ao-desktop/${app.getVersion()}`,
        },
        signal: AbortSignal.timeout(10000),
      },
    );
    if (!response.ok) return undefined;
    const releases = (await response.json()) as GitHubReleaseSummary[];
    const manifestName = `nightly${platformSuffix()}.yml`;
    return releases
      .filter((release) => {
        const parsed = semver.valid(release.tag_name);
        return (
          !release.draft &&
          release.prerelease &&
          parsed !== null &&
          semver.prerelease(parsed)?.[0] === "nightly" &&
          release.assets?.some((asset) => asset.name === manifestName) === true
        );
      })
      .sort((left, right) => semver.rcompare(left.tag_name, right.tag_name))[0]
      ?.tag_name;
  } catch {
    return undefined;
  }
}

function usesDirectNightlyFeed(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): boolean {
  return settings.channel === "nightly" && settings.feature === null;
}

/**
 * Point one Nightly check directly at the newest completed release. Applies to
 * automatic checks as well as manual ones: an atom feed that lags a fresh
 * release makes a background check answer "not-available" and the install goes
 * silently stale, and an entry whose manifest has not finished uploading 404s
 * a check whose error the automatic path deliberately swallows — in both cases
 * the sidebar never learns an update exists.
 * The returned reset restores the normal GitHub provider for later background
 * checks; electron-updater retains the direct provider with the discovered
 * update, so a subsequent Download action still uses the correct asset URLs.
 */
async function configureDirectNightlyFeed(
  settings: UpdateSettings,
): Promise<(() => void) | undefined> {
  if (!usesDirectNightlyFeed(settings)) return undefined;
  const coordinates = await readAppUpdateYml();
  if (!coordinates) return undefined;
  const tag = await fetchLatestCompletedNightlyTag(
    coordinates.owner,
    coordinates.repo,
  );
  if (!tag) return undefined;
  const runningVersion = app.getVersion();
  if (
    semver.valid(runningVersion) !== null &&
    semver.prerelease(runningVersion)?.[0] === "nightly" &&
    semver.lt(tag, runningVersion)
  ) {
    return undefined;
  }

  autoUpdater.setFeedURL({
    provider: "generic",
    url: `https://github.com/${coordinates.owner}/${coordinates.repo}/releases/download/${tag}`,
    channel: "nightly",
    useMultipleRangeRequest: false,
  });
  return () => {
    autoUpdater.setFeedURL({
      provider: "github",
      owner: coordinates.owner,
      repo: coordinates.repo,
    });
  };
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
    if (operation === "automatic-check") {
      automaticCheckNetFailureCounted = false;
      automaticCheckFailureCounted = false;
    }
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

// isNetError checks whether the error is a Chromium network-stack failure
// (net::ERR_*). When the network stack wedges, every updater request fails
// this way until the app restarts (#3526).
function isNetError(err: unknown): boolean {
  return isNetErrorMessage(
    err instanceof Error ? err.message : err === undefined ? undefined : String(err),
  );
}

// recordAutomaticNetFailure counts one net-level automatic-check failure,
// guarding against the same check surfacing as both an "error" event and a
// checkForUpdates() rejection. The flag is re-armed per operation in
// runSerializedUpdaterOperation.
function recordAutomaticNetFailure(): void {
  if (automaticCheckNetFailureCounted) return;
  automaticCheckNetFailureCounted = true;
  consecutiveAutomaticNetFailures += 1;
}

// recordAutomaticCheckFailure tallies one automatic-check failure against the
// consecutive net:: streak. A net error extends it; any other failure (HTTP
// status, manifest 404, signature error, …) proves the network stack reached a
// server and so breaks the streak — otherwise a single interleaving non-net
// error would let a stale streak still trip the nudge (#3526).
function recordAutomaticCheckFailure(err: unknown): void {
  if (isNetError(err)) recordAutomaticNetFailure();
  else consecutiveAutomaticNetFailures = 0;
  // Guarded like the net streak: one check can surface as both an "error" event
  // and a checkForUpdates() rejection, and that is one failure, not two.
  if (!automaticCheckFailureCounted) {
    automaticCheckFailureCounted = true;
    consecutiveAutomaticCheckFailures += 1;
  }
}

/** True once automatic checks have failed enough times to be worth surfacing. */
function automaticChecksAreFailing(): boolean {
  return consecutiveAutomaticCheckFailures >= FAILING_CHECK_THRESHOLD;
}

// publishFailingChecks re-sends the current status once the streak crosses the
// threshold. The suppressed automatic failure deliberately leaves the state
// alone — an error the user never asked for must not replace a truthful idle or
// not-available — but the flag itself is news, and restoring produces no
// broadcast at all when there was no prior status to restore. Without this the
// renderer only learns on its next mount, which is why a stranded install looks
// identical to a healthy one. Sent once per streak, not once per failure.
function publishFailingChecks(): void {
  if (!automaticChecksAreFailing() || failingChecksPublished) return;
  failingChecksPublished = true;
  broadcast(lastStatus);
}

// errorMessage extracts the user-facing message for an update error status,
// defaulting null/undefined to a generic label. Net-error restart guidance is
// localized in the renderer from the netError flag instead of being built here
// (#3526).
function errorMessage(err: unknown): string {
  return err instanceof Error
    ? err.message
    : err == null
      ? "Update check failed"
      : String(err);
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
    // A successful check proves the network stack is healthy.
    consecutiveAutomaticNetFailures = 0;
    consecutiveAutomaticCheckFailures = 0;
    failingChecksPublished = false;
    // A manual re-check reports the already-staged build as merely "available"
    // (autoDownload is off on that path). It is still in cache and installs on
    // quit, so keep the richer downloaded status instead of hiding the row.
    if (stagedAtMs !== undefined && info?.version === stagedVersion) {
      broadcastCompletedCheck(stagedDownloadedStatus());
      return;
    }
    pendingUpdateVersion = info?.version;
    broadcastCompletedCheck({ state: "available", version: info?.version });
  });
  autoUpdater.on("update-not-available", () => {
    // A successful check proves the network stack is healthy.
    consecutiveAutomaticNetFailures = 0;
    consecutiveAutomaticCheckFailures = 0;
    failingChecksPublished = false;
    broadcastCompletedCheck({ state: "not-available" });
    // The staged build outlives a "nothing newer" answer (e.g. after a channel
    // switch); follow up so the restart row returns.
    if (stagedAtMs !== undefined)
      broadcastUpdaterStatus(stagedDownloadedStatus());
  });
  autoUpdater.on("download-progress", (p) => {
    // Any progress proves the network stack is healthy and the check
    // succeeded, so a later error is a download failure even when the
    // operation began life as a check.
    consecutiveAutomaticNetFailures = 0;
    consecutiveAutomaticCheckFailures = 0;
    failingChecksPublished = false;
    activeUpdaterPhase = "download";
    return broadcastUpdaterStatus({
      state: "downloading",
      version: pendingUpdateVersion,
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
    // A one-off automatic failure restores the previous status so the UI does
    // not flash an error the user never asked for. That suppression is a UI
    // decision and must not suppress the telemetry: automatic checks are the
    // main way an install goes silently stale.
    emitUpdateFailure(err);
    if (activeUpdaterOperation === "automatic-check") {
      console.error("auto-update check failed:", err);
      recordAutomaticCheckFailure(err);
      restoreAutomaticCheckPreviousStatus();
      publishFailingChecks();
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
        broadcastCompletedCheck(stagedDownloadedStatus());
      } else {
        broadcastCompletedCheck({
          state: "error",
          message:
            "Couldn't check for updates — the update information was not found on the server.",
        });
      }
      return;
    }
    // All other errors: broadcast so the user knows something went wrong.
    // Chromium network-stack failures carry a netError flag so the renderer can
    // localize restart guidance instead of showing the raw net:: string (#3526).
    const status: UpdateStatus = {
      state: "error",
      message: errorMessage(err),
      ...(isNetError(err) ? { netError: true } : {}),
    };
    if (activeUpdaterPhase === "check") broadcastCompletedCheck(status);
    else broadcast(withActiveRequest(status));
  });
}

export function getUpdateStatus(): UpdateStatus {
  // Derive the nudge at read time: a streak can cross the threshold without
  // any broadcast (no checking-for-update → restore no-ops), and Settings
  // seeds from this getter (#3526).
  return {
    ...lastStatus,
    ...(consecutiveAutomaticNetFailures >= STALE_CHECK_NUDGE_THRESHOLD
      ? { staleCheckNudge: true }
      : {}),
    ...(consecutiveAutomaticCheckFailures >= FAILING_CHECK_THRESHOLD
      ? { checksFailing: true }
      : {}),
  };
}

function automaticUpdateCheckInterval(settings: UpdateSettings): number {
  return settings.channel === "nightly" && settings.feature === null
    ? NIGHTLY_AUTOMATIC_UPDATE_CHECK_INTERVAL_MS
    : STABLE_AUTOMATIC_UPDATE_CHECK_INTERVAL_MS;
}

async function runAutomaticUpdateCheck(
  stateDir: string,
): Promise<number | undefined> {
  let shouldSchedule = true;
  let nextIntervalMs =
    automaticUpdateTimerIntervalMs ?? STABLE_AUTOMATIC_UPDATE_CHECK_INTERVAL_MS;
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
      nextIntervalMs = automaticUpdateCheckInterval(settings);

      escalationStateDir = stateDir;
      wireUpdaterEvents();
      configureFeed(settings);
      autoUpdater.autoDownload = true;
      applyInstallOnQuitPolicy();
      // Only nightly resolves a direct feed. Skipping the await entirely on the
      // other channels keeps this check's event ordering exactly as it was.
      const restoreFeed = usesDirectNightlyFeed(settings)
        ? await configureDirectNightlyFeed(settings)
        : undefined;
      try {
        const result = await autoUpdater.checkForUpdates();
        if (result?.downloadPromise) await result.downloadPromise;
      } catch (err) {
        // electron-updater normally also emits "error" (handled in
        // wireUpdaterEvents); a reject-only failure must still restore the
        // pre-check status so the renderer is neither stuck on "checking" nor
        // denied the stale-check nudge once the streak crosses the threshold
        // (#3526). Record before restoring so the restore broadcast is stamped.
        recordAutomaticCheckFailure(err);
        restoreAutomaticCheckPreviousStatus();
        publishFailingChecks();
        throw err;
      } finally {
        // After the download too: the staged build is already resolved against
        // the direct provider, and later background checks start from the
        // normal GitHub feed again.
        restoreFeed?.();
      }
    });
  } catch (err) {
    console.error("auto-update check failed:", err);
  }
  return shouldSchedule ? nextIntervalMs : undefined;
}

function schedulePeriodicAutomaticUpdateCheck(
  stateDir: string,
  intervalMs: number,
): void {
  if (
    automaticUpdateTimer !== undefined &&
    automaticUpdateTimerIntervalMs === intervalMs
  ) {
    return;
  }
  stopPeriodicAutomaticUpdateCheck();
  automaticUpdateTimerIntervalMs = intervalMs;
  automaticUpdateTimer = setInterval(() => {
    void requestAutomaticUpdateCheck(stateDir).then((nextIntervalMs) => {
      if (nextIntervalMs !== undefined)
        schedulePeriodicAutomaticUpdateCheck(stateDir, nextIntervalMs);
    });
  }, intervalMs);
  automaticUpdateTimer.unref?.();
}

function stopPeriodicAutomaticUpdateCheck(): void {
  if (automaticUpdateTimer === undefined) return;
  clearInterval(automaticUpdateTimer);
  automaticUpdateTimer = undefined;
  automaticUpdateTimerIntervalMs = undefined;
}

function reconcileAutomaticUpdateSchedule(
  stateDir: string,
  settings: UpdateSettings,
): void {
  if (settings.enabled)
    schedulePeriodicAutomaticUpdateCheck(
      stateDir,
      automaticUpdateCheckInterval(settings),
    );
  else stopPeriodicAutomaticUpdateCheck();
}

async function requestAutomaticUpdateCheck(
  stateDir: string,
): Promise<number | undefined> {
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
  const intervalMs = await requestAutomaticUpdateCheck(stateDir);
  if (intervalMs !== undefined)
    schedulePeriodicAutomaticUpdateCheck(stateDir, intervalMs);
}

async function persistUpdaterSettings(
  stateDir: string,
  settings: UpdateSettings,
): Promise<void> {
  await writeUpdateSettings(stateDir, settings);
  configureFeed(settings);
  reconcileAutomaticUpdateSchedule(stateDir, settings);
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
        reconcileAutomaticUpdateSchedule(stateDir, settings);
        configureFeed(settings);
        autoUpdater.autoDownload = false;
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        const restoreFeed = await configureDirectNightlyFeed(settings);
        try {
          await autoUpdater.checkForUpdates();
        } finally {
          restoreFeed?.();
        }
      },
      options.requestId,
    );
  } catch (err) {
    if (isManifest404Error(err)) {
      console.info("manual update check failed:", err);
      broadcastCompletedCheck({
        state: "error",
        message:
          "Couldn't check for updates — the update information was not found on the server.",
        ...(options.requestId === undefined ? {} : { requestId: options.requestId }),
      });
      if (stagedAtMs !== undefined) broadcast(stagedDownloadedStatus());
    } else {
      broadcastCompletedCheck({
        state: "error",
        message: errorMessage(err),
        ...(isNetError(err) ? { netError: true } : {}),
        ...(options.requestId === undefined ? {} : { requestId: options.requestId }),
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
        reconcileAutomaticUpdateSchedule(stateDir, settings);
        configureFeed(settings);
        autoUpdater.autoDownload = false;
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        await autoUpdater.checkForUpdates();
      },
      requestId,
    );
  } catch (err) {
    broadcast({
      state: "error",
      message: (err as Error)?.message ?? "Return failed",
      ...(requestId === undefined ? {} : { requestId }),
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

// getMacInstallBlocker is the macOS install preflight. An app launched straight
// from where it was downloaded runs under App Translocation: a randomized
// READ-ONLY mount beneath /private/var/folders/.../AppTranslocation. Squirrel
// cannot replace that bundle, so quitAndInstall() silently does nothing: no
// restart, no error, a dead button (#3527). The same dead end applies to any
// bundle the user cannot write to, and to a writable bundle in a directory the
// user cannot write to: ShipIt swaps by moving the bundle aside and moving the
// new one in, so the PARENT is what has to be writable, not just the bundle.
// Returns the user-facing explanation when installing cannot work from here,
// undefined when the install may proceed. Fails open: only a positively
// identified blocker suppresses the attempt.
//
// This is a backstop, not the primary fix. main.ts now hands off to an
// equal-or-newer install rather than running from a stale location at all
// (see main/relocation.ts); this catches what is left, such as a first launch
// with nothing yet installed in /Applications.
export function getMacInstallBlocker(): string | undefined {
  if (process.platform !== "darwin") return undefined;
  // .../Agent Orchestrator.app/Contents/MacOS/<binary> -> the .app bundle root
  const bundle = path.resolve(process.execPath, "..", "..", "..");
  // Everything below assumes that shape. Under `npm start`, and in tests,
  // execPath is a bare node/electron binary and this resolves to some unrelated
  // ancestor directory whose permissions say nothing about installability, so
  // fail open rather than guess from it.
  if (!bundle.endsWith(".app")) return undefined;
  if (bundle.includes("/AppTranslocation/")) {
    return (
      "macOS is running Agent Orchestrator from a temporary read-only location " +
      "because it was opened straight from where it was downloaded. Quit the app, " +
      "move Agent Orchestrator.app into /Applications, reopen it from there, and " +
      "then restart to update."
    );
  }
  if (!existsSync(bundle)) return undefined;
  try {
    accessSync(bundle, fsConstants.W_OK);
    // ShipIt writes into the enclosing directory, not just the bundle.
    accessSync(path.dirname(bundle), fsConstants.W_OK);
  } catch {
    // Deliberately does NOT say "move it to /Applications": the app may already
    // be there, and telling someone to do what they have done reads as a bug.
    return (
      "The update can't be installed because Agent Orchestrator's location isn't " +
      `writable: ${path.dirname(bundle)}. Fix that folder's permissions, or move ` +
      "Agent Orchestrator.app somewhere you can write to, reopen it, and then " +
      "restart to update."
    );
  }
  return undefined;
}

// applyInstallOnQuitPolicy keeps autoInstallOnAppQuit honest. Every check path
// sets it to true, and the "downloaded" status row tells the user the build
// installs on quit. When the install cannot work from this location that is a
// lie in both directions: the quit-time install fails as silently as the button
// did, and #3527's dialog only ever covered the button. Turning it off makes
// the staged build wait for a location it can actually install from.
function applyInstallOnQuitPolicy(): void {
  const blocker = getMacInstallBlocker();
  autoUpdater.autoInstallOnAppQuit = blocker === undefined;
  if (blocker !== undefined) {
    console.warn(
      "install-on-quit disabled; the update cannot be installed from here:",
      blocker,
    );
  }
}

// quitAndInstallUpdate installs a downloaded update and relaunches. isSilent
// false keeps the installer UI on Windows; isForceRunAfter relaunches the app.
export function quitAndInstallUpdate(): void {
	if (!app.isPackaged) return;
  const blocker = getMacInstallBlocker();
  if (blocker !== undefined) {
    console.warn("update install blocked:", blocker);
    // A dialog, not a status broadcast: the click came from the sidebar row,
    // and replacing the "downloaded" status would hide that row (losing the
    // retry affordance) without guaranteeing the user ever sees the message.
    // The staged build stays staged; after the user moves the app the same
    // row installs it.
    void dialog.showMessageBox({
      type: "warning",
      message: "The update can't be installed from this location",
      detail: blocker,
      buttons: ["OK"],
    });
    return;
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
