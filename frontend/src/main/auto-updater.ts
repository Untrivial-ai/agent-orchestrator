import { autoUpdater } from "electron-updater";
import { CancellationToken } from "builder-util-runtime";
import { app, BrowserWindow, dialog } from "electron";
import { accessSync, constants as fsConstants, existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";
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
  normalizeReleaseNotes,
  updateFailureOutcome,
  type UpdateOutcome,
  type UpdatePhase,
  type UpdateTrigger,
} from "../shared/update-telemetry";
import { StagedUpdateJournalStore } from "./staged-update-journal";
import { candidateFromUpdateInfo, effectiveUpdateChannel, journalToUpdateStatus } from "./staged-update-adapter";
import { transitionStagedUpdate, type StagedUpdateEvent, type StagedUpdateJournal, type UpdateCandidate } from "./staged-update-state";

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
// Release notes for the build currently on offer or staged, already
// sanitized. Held here because only the updater events carry it, and the
// renderer needs it on every subsequent status too, not just the one event.
let offeredReleaseNotes: string | undefined;
// Notes resolved out-of-band for a feed whose provider cannot carry them.
// Used only as a fallback, so a provider that does supply notes always wins.
let directFeedReleaseNotes: string | undefined;
// Which feed channel the staged build came from. A build staged from one
// channel is already armed with the OS installer, so switching channels has
// to notice that it no longer belongs (see stagedBuildIsStale).
let stagedChannel: string | undefined;
let stagedAtMs: number | undefined;
let stagedEscalated = false;
let stagedRequestId: string | undefined;
let stagedJournal: StagedUpdateJournal = { schemaVersion: 1, state: "none" };
let stagedJournalWritesPending = 0;
let stagedJournalStore: StagedUpdateJournalStore | undefined;
let stagedJournalDir: string | undefined;
let stagedJournalQueue: Promise<void> = Promise.resolve();
let journalPersistenceError: string | undefined;
let activeUpdaterOperationId: string | undefined;
let pendingDownloadedInfo: any;
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
// failure mode that actually strands an install, a manifest 404 on every
// check, can never trip it. This counter does not care why the check failed:
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
// Stalled-download watchdog. electron-updater keeps its request open when a
// download stops receiving bytes, so AO kept the last percentage forever, held
// the updater queue occupied, and offered nothing to retry. Bytes that are
// genuinely slow still advance the percentage, so inactivity is the signal, not
// elapsed time.
const DOWNLOAD_STALL_TIMEOUT_MS = 2 * 60 * 1000;
let downloadStallTimer: ReturnType<typeof setTimeout> | undefined;
let activeDownloadCancellation: CancellationToken | undefined;
let downloadStalled = false;

function clearDownloadStallWatchdog(): void {
  if (downloadStallTimer !== undefined) {
    clearTimeout(downloadStallTimer);
    downloadStallTimer = undefined;
  }
  activeDownloadCancellation = undefined;
}

/**
 * (Re)arm the watchdog. Called on every progress event, so the deadline only
 * expires when nothing has advanced for the whole window.
 */
function armDownloadStallWatchdog(): void {
  if (downloadStallTimer !== undefined) clearTimeout(downloadStallTimer);
  downloadStallTimer = setTimeout(() => {
    downloadStallTimer = undefined;
    downloadStalled = true;
    if (stagedJournal.state === "replacing") persistJournalTransition({ type: "replacement-failed", operationId: stagedJournal.replacement.operationId, at: Date.now(), message: "Download stopped responding. Try again." });
    console.error("update download stalled; cancelling");
    // Cancel so electron-updater releases its request and the serialized
    // operation can finish. Without this the queue stays blocked and even a
    // manual retry would just wait behind the dead download.
    activeDownloadCancellation?.cancel();
    activeDownloadCancellation = undefined;
    emitUpdateOutcome(
      updateFailureOutcome("download stalled", "download", activeUpdateTrigger(), pendingUpdateVersion),
    );
    broadcast(
      withActiveRequest({
        state: "error",
        message: "Download stopped responding. Try again.",
        ...(pendingUpdateVersion === undefined ? {} : { version: pendingUpdateVersion }),
      }),
    );
  }, DOWNLOAD_STALL_TIMEOUT_MS);
  downloadStallTimer.unref?.();
}

// Session-scoped time of the most recent completed feed check. Packaged apps
// check the selected channel at launch regardless of whether automatic
// downloading is enabled.
let lastCheckedAtMs: number | undefined;

async function ensureStagedJournal(stateDir: string): Promise<void> {
  if (stagedJournalStore !== undefined && stagedJournalDir === stateDir) {
    return;
  }
  stagedJournalDir = stateDir;
  stagedJournalStore = new StagedUpdateJournalStore(stateDir);
  try {
    stagedJournal = await stagedJournalStore.read(app.getVersion());
    await stagedJournalStore.write(stagedJournal);
  } catch (error) {
    console.error("staged update journal could not be restored:", error);
    journalPersistenceError = errorMessage(error);
    broadcast({ state: "error", message: "Update history could not be restored. A previous build may still install on quit.", installDisabledReason: journalPersistenceError });
  }
  if (stagedJournal.state === "native-possibly-staged") {
    stagedVersion = stagedJournal.staged.version;
    stagedChannel = stagedJournal.staged.channel;
    stagedAtMs = stagedJournal.stagedAt;
    broadcast(journalToUpdateStatus(stagedJournal));
  } else if (stagedJournal.state !== "none") {
    stagedVersion = stagedJournal.staged.version;
    stagedChannel = stagedJournal.staged.channel;
    stagedAtMs = stagedJournal.stagedAt;
    awaitingStagedReplacement = stagedJournal.state === "replacing" || stagedJournal.state === "replacement-failed";
    broadcast(journalToUpdateStatus(stagedJournal));
  }
}

function persistJournalTransition(event: StagedUpdateEvent): void {
  const store = stagedJournalStore;
  if (store === undefined) return;
  stagedJournal = transitionStagedUpdate(stagedJournal, event);
  const journal = stagedJournal;
  stagedJournalWritesPending += 1;
  stagedJournalQueue = stagedJournalQueue.then(async () => {
    await store.write(journal);
    // Only the latest snapshot can clear a persistence failure. Earlier queued
    // successes must not authorize installation of newer, unwritten state.
    if (journal === stagedJournal && journalPersistenceError) {
      journalPersistenceError = undefined;
      broadcast({ ...lastStatus, installDisabledReason: undefined, ...journalToUpdateStatus(stagedJournal) });
    }
  }).catch((error) => {
    journalPersistenceError = errorMessage(error);
    // Keep the live transition chain intact. Queued snapshots depend on it.
    // Installation stays blocked by this error until the latest state is saved.
    console.error("staged update journal transition failed:", error);
    broadcast({ state: "error", message: "Update history could not be saved. A previous build may still install on quit." });
  }).finally(() => { stagedJournalWritesPending -= 1; });
}

function candidateFromUpdater(info: any): UpdateCandidate | undefined {
  if (typeof info?.version !== "string" || info.version.length === 0) return undefined;
  const channel = effectiveUpdateChannel(autoUpdater.channel);
  const file = Array.isArray(info.files) ? info.files[0] : undefined;
  const feedUrl = typeof autoUpdater.getFeedURL === "function"
    ? autoUpdater.getFeedURL()
    : undefined;
  return candidateFromUpdateInfo({
    version: info.version,
    channel,
    ...(typeof info.releaseName === "string" ? { releaseTag: info.releaseName } : {}),
    ...(typeof feedUrl === "string" ? { feedUrl } : {}),
    ...(typeof file?.url === "string" ? { assetName: path.basename(file.url) } : {}),
    ...(typeof file?.sha512 === "string" ? { sha512: file.sha512 } : {}),
    operationId: activeUpdaterOperationId ?? randomUUID(),
  });
}

function hasPendingReplacement(): boolean {
  return stagedJournal.state === "replacing" || stagedJournal.state === "replacement-failed";
}

function selectedChannelRequiresReplacement(): boolean {
  if (stagedJournal.state === "none") return false;
  return stagedJournal.staged.channel !== effectiveUpdateChannel(autoUpdater.channel);
}

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
  const describesAnOffer =
    status.state === "available" ||
    status.state === "downloading" ||
    status.state === "downloaded";
  const stamped: UpdateStatus = {
    ...statusWithCheckTime,
    ...stagedStamp(),
    ...((stagedJournal.state === "replacing" || stagedJournal.state === "replacement-failed") ? journalToUpdateStatus(stagedJournal, status) : {}),
    ...(journalPersistenceError || (awaitingStagedReplacement && !hasPendingReplacement()) ? { installDisabledReason: journalPersistenceError ?? `The selected channel has not finished staging. Quitting may still install ${stagedVersion}.` } : {}),
    // Only on statuses that actually describe a build on offer: "not-available"
    // carrying notes for a build the user already has would read as news.
    ...(describesAnOffer && offeredReleaseNotes !== undefined && status.releaseNotes === undefined
      ? { releaseNotes: offeredReleaseNotes }
      : {}),
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
  body?: string | null;
  assets?: Array<{ name?: string }>;
}

/**
 * Resolve a completed Nightly release through GitHub's API. electron-updater's
 * GitHub provider discovers prereleases through releases.atom, which can lag
 * behind a just-published release even when the release and manifest are ready.
 * This is used only for user-requested checks; failures fall back to the normal
 * provider so API rate limits or an outage never break update checks.
 */
async function fetchLatestCompletedPrereleaseTag(
  owner: string,
  repo: string,
  channel: string,
): Promise<{ tag: string; body?: string } | undefined> {
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
    const manifestName = `${channel}${platformSuffix()}.yml`;
    const newest = releases
      .filter((release) => {
        const parsed = semver.valid(release.tag_name);
        return (
          !release.draft &&
          release.prerelease &&
          parsed !== null &&
          semver.prerelease(parsed)?.[0] === channel &&
          release.assets?.some((asset) => asset.name === manifestName) === true
        );
      })
      .sort((left, right) => semver.rcompare(left.tag_name, right.tag_name))[0];
    if (newest === undefined) return undefined;
    // The body comes back on the same response, so carrying it costs nothing.
    // Every channel resolved this way needs it: the direct feed below is
    // electron-updater's GENERIC provider, which never populates releaseNotes
    // (only GitHubProvider does), and the channel manifests have no field for
    // them. Without this the "what's new" section could never say anything on
    // nightly or on a pinned feature build.
    return {
      tag: newest.tag_name,
      ...(typeof newest.body === "string" ? { body: newest.body } : {}),
    };
  } catch {
    return undefined;
  }
}

function directPrereleaseChannel(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): string | undefined {
  if (settings.feature) return `pr${settings.feature.pr}`;
  return settings.channel === "nightly" ? "nightly" : undefined;
}

/**
 * Point one Nightly check directly at the newest completed release. Applies to
 * automatic checks as well as manual ones: an atom feed that lags a fresh
 * release makes a background check answer "not-available" and the install goes
 * silently stale, and an entry whose manifest has not finished uploading 404s
 * a check whose error the automatic path deliberately swallows, in both cases
 * the sidebar never learns an update exists.
 * The returned reset restores the normal GitHub provider for later background
 * checks; electron-updater retains the direct provider with the discovered
 * update, so a subsequent Download action still uses the correct asset URLs.
 */
async function configureDirectPrereleaseFeed(
  settings: UpdateSettings,
): Promise<(() => void) | undefined> {
  const channel = directPrereleaseChannel(settings);
  if (!channel) return undefined;
  const coordinates = await readAppUpdateYml();
  if (!coordinates) return undefined;
  const release = await fetchLatestCompletedPrereleaseTag(
    coordinates.owner,
    coordinates.repo,
    channel,
  );
  if (!release) return undefined;
  const { tag } = release;
  const runningVersion = app.getVersion();
  if (
    semver.valid(runningVersion) !== null &&
    semver.prerelease(runningVersion)?.[0] === channel &&
    semver.lt(tag, runningVersion)
  ) {
    return undefined;
  }

  // Stand in for what the generic provider cannot supply. Overwritten by the
  // real thing if a later event does carry notes.
  directFeedReleaseNotes = normalizeReleaseNotes(release.body);
  autoUpdater.setFeedURL({
    provider: "generic",
    url: `https://github.com/${coordinates.owner}/${coordinates.repo}/releases/download/${tag}`,
    channel,
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

/**
 * The `staged` stamp every status carries while a build waits to install, so a
 * transient checking/available/not-available state cannot make the sidebar's
 * restart row disappear mid-check. Empty when nothing is staged.
 */
function stagedStamp(): Pick<UpdateStatus, "staged"> {
  if (stagedAtMs === undefined) return {};
  return {
    staged: {
      ...(stagedVersion === undefined ? {} : { version: stagedVersion }),
      stagedAt: stagedAtMs,
      escalated: stagedEscalated,
    },
  };
}

/** The feed channel a settings object resolves to. Mirrors configureFeed. */
function effectiveChannel(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): string {
  return settings.feature ? `pr${settings.feature.pr}` : settings.channel;
}

/**
 * True when the staged build belongs to a channel the user is no longer on.
 *
 * This matters because staging is not reversible. On macOS a completed download
 * hands the build to Squirrel (MacUpdater calls nativeUpdater.checkForUpdates()
 * when autoInstallOnAppQuit is set), and the resulting ShipIt process sits
 * waiting for the app to exit. Clearing autoInstallOnAppQuit afterwards does not
 * disarm it: quitting still installs that build. Switching from nightly to
 * stable therefore used to install the NIGHTLY on the next quit, while Settings
 * said "Restart to switch to Stable".
 *
 * The only reliable way out is to stage the correct build over it, because each
 * completed download issues a fresh install request that supersedes the last.
 * So a stale staged build forces a download on the next check regardless of the
 * automatic-download preference.
 */
function stagedBuildIsStale(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): boolean {
  return (
    (stagedAtMs !== undefined && stagedChannel !== undefined && stagedChannel !== effectiveChannel(settings)) || selectedChannelRequiresReplacement()
  );
}

// #4922: Windows/Linux consult this flag at quit. It does not revoke a
// Squirrel request on macOS, so A remains recorded until replacement succeeds.
let awaitingStagedReplacement = false;

/** A build is downloaded and waiting to install, and we know which one. */
function hasStagedBuild(): boolean {
  return stagedAtMs !== undefined && stagedVersion !== undefined;
}

/** The feed is offering something other than what is already staged. */
function supersedesStagedBuild(version: string | undefined): boolean {
  return hasStagedBuild() && version !== undefined && version !== stagedVersion;
}

type UpdateCheckOutcome = Awaited<ReturnType<typeof autoUpdater.checkForUpdates>>;

/**
 * Land a terminal status when a check resolved without emitting one.
 *
 * electron-updater can do exactly that: `checkForUpdates()` called while another
 * check is in flight returns the in-flight promise, and that check's events were
 * already consumed under a different operation. Nothing else ever moves the
 * status off "checking", and the Settings row keys its spinner and its disabled
 * Check button off that state, so the page wedges with no visible explanation.
 * Called only on renderer-requested checks, which are the ones a user is waiting on.
 */
function settleCheckStatus(result: UpdateCheckOutcome): void {
  if (lastStatus.state !== "checking") return;
  const version = result?.updateInfo?.version;
  if (result?.isUpdateAvailable === true && version !== undefined) {
    broadcastCompletedCheck({ state: "available", version });
    return;
  }
  broadcastCompletedCheck(
    hasStagedBuild() ? stagedDownloadedStatus() : { state: "not-available" },
  );
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
    activeUpdaterOperationId = randomUUID();
    activeUpdaterRequestId = requestId;
    activeUpdaterPhase = operation === "manual-download" ? "download" : "check";
    pendingUpdateVersion = undefined;
    if (operation === "automatic-check") {
      automaticCheckNetFailureCounted = false;
      automaticCheckFailureCounted = false;
    }
    try {
      await runOperation();
      if (pendingDownloadedInfo) finishDownloadedUpdate(pendingDownloadedInfo);
    } catch (error) {
      if (stagedJournal.state === "replacing") {
        persistJournalTransition({ type: "replacement-failed", operationId: stagedJournal.replacement.operationId, at: Date.now(), message: errorMessage(error) });
        broadcast(journalToUpdateStatus(stagedJournal));
      }
      throw error;
    } finally {
      pendingDownloadedInfo = undefined;
      await stagedJournalQueue;
      activeUpdaterOperation = undefined;
      activeUpdaterOperationId = undefined;
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
// server and so breaks the streak, otherwise a single interleaving non-net
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
// alone, an error the user never asked for must not replace a truthful idle or
// not-available, but the flag itself is news, and restoring produces no
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
// manifest YAML file, a routine condition that should not be surfaced
// to users as an error dialog.
function isManifest404Error(err: unknown): boolean {
  const e = err as Error & { code?: string };
  if (e.code === "ERR_UPDATER_CHANNEL_FILE_NOT_FOUND") return true;
  const msg = e.message ?? "";
  return msg.includes("HttpError: 404") && /\.yml\b/i.test(msg);
}

function finishDownloadedUpdate(info: any): void {
  const downloadedAt = Date.now();
  const downloaded = candidateFromUpdater(info);
  if ((stagedJournal.state === "replacing" || stagedJournal.state === "replacement-failed") &&
    (downloaded?.version !== stagedJournal.replacement.version || downloaded?.channel !== stagedJournal.replacement.channel ||
      (activeUpdaterOperationId && activeUpdaterOperationId !== stagedJournal.replacement.operationId))) return;
  if (stagedJournal.state === "replacement-failed") return;
  if (stagedJournal.state === "replacing" && process.platform === "darwin" && !autoUpdater.autoInstallOnAppQuit) {
    // A resolved download is not a handoff when MacUpdater skipped Squirrel
    // because the current location cannot be installed to.
    persistJournalTransition({ type: "replacement-failed", operationId: stagedJournal.replacement.operationId, at: downloadedAt, message: "The replacement could not be handed to the native installer from this location. The previous update may still install on quit." });
    broadcast(journalToUpdateStatus(stagedJournal));
    return;
  }
  if (downloaded) {
    if (stagedJournal.state === "replacing" && stagedJournal.replacement.version === downloaded.version) {
      const operationId = stagedJournal.replacement.operationId;
      persistJournalTransition({ type: "replacement-phase", operationId, phase: "native-handoff" });
      broadcast(journalToUpdateStatus(stagedJournal));
      // MacUpdater resolves when its ZIP response finishes, before Squirrel
      // validates the replacement. Its native events lack operation identity.
      // Keep both candidates and the install guard until restart reconciliation
      // or the verified native boundary supplied by PR3.
      if (process.platform === "darwin") return;
      persistJournalTransition({ type: "handoff-succeeded", operationId, at: downloadedAt });
    } else if (stagedJournal.state === "none" || stagedJournal.state === "version-mismatch") {
      persistJournalTransition({ type: "initial-handoff-succeeded", candidate: downloaded, at: downloadedAt });
    }
  }
  // Re-staging the SAME build must not restart the staged clock. electron-updater
  // re-runs its download task whenever a check finds a version it has already
  // cached, so this event repeats on every automatic check until the user quits.
  // Resetting stagedAtMs there would mean the latest-channel 48h escalation rule
  // could never fire, because the clock is only ever minutes old.
  const restaged = stagedAtMs !== undefined && info?.version === stagedVersion;
  stagedVersion = info?.version;
  stagedChannel = autoUpdater.channel ?? undefined;
  offeredReleaseNotes =
    normalizeReleaseNotes(info?.releaseNotes) ?? offeredReleaseNotes ?? directFeedReleaseNotes;
  if (!restaged) {
    stagedAtMs = Date.now();
    stagedEscalated = false;
  }
  stagedRequestId = activeUpdaterRequestId;
  // A build is staged again, so install-on-quit has something correct to run.
  awaitingStagedReplacement = false;
  applyInstallOnQuitPolicy();
  automaticCheckPreviousStatus = undefined;
  // A completed automatic download advances the independent baseline; a
  // renderer-requested download additionally carries its request ownership.
  broadcast(withActiveRequest(stagedDownloadedStatus()));
  // Evaluate now (nightly can escalate immediately), then every 30 minutes
  // while the update sits uninstalled. unref so the timer never holds the
  // process open on quit.
  void runEscalationCheck();
  // Re-arming on a re-stage would push the next evaluation out by another 30
  // minutes every time, and the nightly channel re-stages every 15, the loop
  // would never get a turn. Leave the running timer alone in that case.
  if (!restaged || escalationTimer === undefined) {
    stopEscalationTimer();
    escalationTimer = setInterval(
      () => void runEscalationCheck(),
      30 * 60 * 1000,
    );
    escalationTimer.unref?.();
  }
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
    if (stagedAtMs !== undefined && info?.version === stagedVersion && stagedChannel === effectiveUpdateChannel(autoUpdater.channel)) {
      if (stagedJournal.state === "replacing" || stagedJournal.state === "replacement-failed") {
        const offered = candidateFromUpdater(info);
        if (offered) persistJournalTransition({ type: "replacement-discovered", replacement: offered, at: Date.now() });
        awaitingStagedReplacement = hasPendingReplacement();
        applyInstallOnQuitPolicy();
      }
      broadcastCompletedCheck(stagedDownloadedStatus());
      return;
    }
    pendingUpdateVersion = info?.version;
    offeredReleaseNotes = normalizeReleaseNotes(info?.releaseNotes) ?? directFeedReleaseNotes;
    const offered = candidateFromUpdater(info);
    if (offered && stagedJournal.state !== "none") {
      const currentlyStaged = stagedJournal.staged;
      if (offered.version !== currentlyStaged.version || offered.channel !== currentlyStaged.channel) {
        persistJournalTransition({ type: "replacement-discovered", replacement: offered, at: Date.now() });
        const offeredFile = Array.isArray(info?.files) ? info.files[0] : undefined;
        persistJournalTransition({
          type: "replacement-phase",
          operationId: offered.operationId,
          phase: !autoUpdater.autoDownload ? "checking" : typeof offeredFile?.blockMapSize === "number" ? "differential" : "full-fallback",
        });
        broadcastCompletedCheck(journalToUpdateStatus(stagedJournal));
        return;
      }
    }
    broadcastCompletedCheck({ state: "available", version: info?.version });
  });
  autoUpdater.on("update-cancelled", () => {
    clearDownloadStallWatchdog();
  });
  autoUpdater.on("update-not-available", () => {
    // A successful check proves the network stack is healthy.
    consecutiveAutomaticNetFailures = 0;
    consecutiveAutomaticCheckFailures = 0;
    failingChecksPublished = false;
    if (stagedJournal.state !== "none") persistJournalTransition({ type: "no-update" });
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
    armDownloadStallWatchdog();
    if (stagedJournal.state === "replacing") {
      if (stagedJournal.phase === "checking") {
        persistJournalTransition({ type: "replacement-phase", operationId: stagedJournal.replacement.operationId, phase: "full-fallback" });
      }
      return broadcastUpdaterStatus(journalToUpdateStatus(stagedJournal, {
        percent: Math.max(0, Math.min(100, Math.round(p?.percent ?? 0))),
        transferred: p?.transferred,
        total: p?.total,
        bytesPerSecond: p?.bytesPerSecond,
      }));
    }
    return broadcastUpdaterStatus({
      state: "downloading",
      version: pendingUpdateVersion,
      percent: Math.max(0, Math.min(100, Math.round(p?.percent ?? 0))),
    });
  });
  autoUpdater.on("update-downloaded", (info) => {
    clearDownloadStallWatchdog();
    downloadStalled = false;
    emitUpdateOutcome({
      event: "ao.renderer.update_downloaded",
      phase: "download",
      trigger: activeUpdateTrigger(),
      ...(info?.version ? { to_version: info.version } : {}),
    });
    if (activeUpdaterOperation && stagedJournal.state === "replacing") {
      // MacUpdater emits this before starting Squirrel's fetch. Keep A until
      // the operation completes; on macOS even ZIP delivery is not validation.
      pendingDownloadedInfo = info;
      persistJournalTransition({ type: "replacement-phase", operationId: stagedJournal.replacement.operationId, phase: "verifying" });
      broadcast(journalToUpdateStatus(stagedJournal));
      return;
    }
    finishDownloadedUpdate(info);
  });
  autoUpdater.on("error", (err) => {
    clearDownloadStallWatchdog();
    if (downloadStalled) {
      // Our own cancellation surfacing as an error. The stall status is already
      // published and is more useful than "cancelled"; replacing it would lose
      // the retry wording.
      downloadStalled = false;
      console.info("update download cancelled after stalling:", err);
      return;
    }
    // Never crash on update failure (offline, unsigned macOS, etc.).
    // A one-off automatic failure restores the previous status so the UI does
    // not flash an error the user never asked for. That suppression is a UI
    // decision and must not suppress the telemetry: automatic checks are the
    // main way an install goes silently stale.
    emitUpdateFailure(err);
    if (stagedJournal.state === "replacing") {
      persistJournalTransition({ type: "replacement-failed", operationId: stagedJournal.replacement.operationId, at: Date.now(), message: errorMessage(err) });
      broadcast(journalToUpdateStatus(stagedJournal));
      return;
    }
    if (activeUpdaterOperation === "automatic-check") {
      console.error("auto-update check failed:", err);
      recordAutomaticCheckFailure(err);
      restoreAutomaticCheckPreviousStatus();
      publishFailingChecks();
      return;
    }
    // Manifest 404 (missing latest-mac.yml etc.) is a routine condition,
    // not an actionable error, log and broadcast a terminal state so
    // the renderer does not hang.
    if (isManifest404Error(err)) {
      console.info("update check failed (404, manifest not found):", err);
      if (activeUpdaterOperation === "manual-download") {
        broadcast(
          withActiveRequest({
            state: "error",
            message:
              "Download failed, the update file was not found on the server.",
          }),
        );
      } else if (stagedAtMs !== undefined) {
        broadcastCompletedCheck(stagedDownloadedStatus());
      } else {
        broadcastCompletedCheck({
          state: "error",
          message:
            "Couldn't check for updates, the update information was not found on the server.",
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
    ...stagedStamp(),
    ...(offeredReleaseNotes !== undefined && lastStatus.releaseNotes === undefined &&
      (lastStatus.state === "available" || lastStatus.state === "downloading" || lastStatus.state === "downloaded")
      ? { releaseNotes: offeredReleaseNotes }
      : {}),
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
): Promise<number> {
  await ensureStagedJournal(stateDir);
  let nextIntervalMs =
    automaticUpdateTimerIntervalMs ?? STABLE_AUTOMATIC_UPDATE_CHECK_INTERVAL_MS;
  try {
    await runSerializedUpdaterOperation("automatic-check", async () => {
      const settings = await reconcileAndPersist(
        stateDir,
        await readUpdateSettings(stateDir),
      );
      nextIntervalMs = automaticUpdateCheckInterval(settings);

      escalationStateDir = stateDir;
      wireUpdaterEvents();
      configureFeed(settings);
      // Discovery is always on for the selected release channel. This preference
      // controls only whether electron-updater downloads the discovered build or
      // leaves it in `available` for the sidebar action.
      //
      // A build that is already staged suspends auto-download for this check.
      // electron-updater does not treat "already in the cache" as done: a cache
      // hit still runs the download task's completion path, which on macOS copies
      // the whole zip to update.zip and hands Squirrel a fresh install request.
      // With autoDownload on, that repeated for every check for as long as the
      // user went without quitting, 175 MB of copying and a ShipIt spawn every
      // 15 minutes on nightly. Anything genuinely newer than the staged build is
      // still fetched, below.
      // A staged build from a channel the user has left is already armed with
      // the OS installer; the replacement must be fetched even when automatic
      // downloading is off, or quitting installs the build they moved away from.
      const staleStaged = stagedBuildIsStale(settings);
      if (staleStaged) awaitingStagedReplacement = true;
      autoUpdater.autoDownload =
        staleStaged || (settings.enabled && !hasStagedBuild());
      applyInstallOnQuitPolicy();
      // Only prerelease channels resolve a direct feed. Skipping the await on
      // stable keeps that check's event ordering exactly as it was.
      const restoreFeed = directPrereleaseChannel(settings)
        ? await configureDirectPrereleaseFeed(settings)
        : undefined;
      try {
        const result = await autoUpdater.checkForUpdates();
        if (settings.enabled || staleStaged) {
          if (result?.downloadPromise) {
            // The provider owns this download's token; hand it to the watchdog
            // so a stall can actually be cancelled rather than just reported.
            activeDownloadCancellation = result.cancellationToken;
            await result.downloadPromise;
          } else if (supersedesStagedBuild(result?.updateInfo?.version)) {
            // autoDownload was suspended for the staged build, but this is a
            // different version, so it still has to be fetched automatically.
            activeUpdaterPhase = "download";
            pendingUpdateVersion = result?.updateInfo?.version;
            const token = new CancellationToken();
            activeDownloadCancellation = token;
            await autoUpdater.downloadUpdate(token);
          }
        }
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
  return nextIntervalMs;
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
  schedulePeriodicAutomaticUpdateCheck(
    stateDir,
    automaticUpdateCheckInterval(settings),
  );
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
// Channel controls discovery; enabled controls whether a discovered build is
// downloaded automatically. Both preferences come from update-settings.
// Caller guards on app.isPackaged.
export async function startAutoUpdates(stateDir: string): Promise<void> {
  escalationStateDir = stateDir;
  await ensureStagedJournal(stateDir);
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
// build from Settings. It does NOT auto-download, the user clicks Update, and
// reports progress via the broadcast status. Updates only work in the packaged,
// signed app; in dev electron-updater has no feed, so surface that plainly.
export async function checkForUpdatesNow(
  stateDir: string,
  options: UpdateCheckOptions = {},
): Promise<void> {
  await ensureStagedJournal(stateDir);
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
        // Same reason as the automatic path: a channel switch leaves the old
        // channel's build armed, and only staging the new one over it helps.
        const staleStaged = stagedBuildIsStale(settings);
        if (staleStaged) awaitingStagedReplacement = true;
        autoUpdater.autoDownload = staleStaged;
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        const restoreFeed = await configureDirectPrereleaseFeed(settings);
        try {
          const result = await autoUpdater.checkForUpdates();
          if (result?.downloadPromise) {
            activeDownloadCancellation = result.cancellationToken;
            await result.downloadPromise;
          }
          if (!result?.downloadPromise && staleStaged && stagedJournal.state === "replacing") {
            const token = new CancellationToken();
            activeDownloadCancellation = token;
            await autoUpdater.downloadUpdate(token);
          }
          settleCheckStatus(result);
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
          "Couldn't check for updates, the update information was not found on the server.",
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
  await ensureStagedJournal(stateDir);
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
        // Leaving a pinned PR build is the same class of switch: its build is
        // armed and has to be superseded, not merely forgotten.
        const staleStaged = stagedBuildIsStale(settings);
        if (staleStaged) awaitingStagedReplacement = true;
        autoUpdater.autoDownload = staleStaged;
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        const result = await autoUpdater.checkForUpdates();
        if (result?.downloadPromise) {
          activeDownloadCancellation = result.cancellationToken;
          await result.downloadPromise;
        }
        if (!result?.downloadPromise && staleStaged && stagedJournal.state === "replacing") {
          const token = new CancellationToken();
          activeDownloadCancellation = token;
          await autoUpdater.downloadUpdate(token);
        }
        settleCheckStatus(result);
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
        if (stagedJournal.state === "replacement-failed" || stagedJournal.state === "replacing") {
          persistJournalTransition({
            type: "replacement-discovered",
            replacement: {
              ...stagedJournal.replacement,
              operationId: activeUpdaterOperationId!,
            },
            at: Date.now(),
          });
          if (stagedJournal.state === "replacing") persistJournalTransition({ type: "replacement-phase", operationId: stagedJournal.replacement.operationId, phase: "full-fallback" });
          broadcast(journalToUpdateStatus(stagedJournal));
        }
        // Manual downloads get no provider token, so make one: without it the
        // watchdog could report a stall but never release the request.
        const token = new CancellationToken();
        activeDownloadCancellation = token;
        await autoUpdater.downloadUpdate(token);
      },
      requestId,
    );
  } catch (err) {
    if (isManifest404Error(err)) {
      console.error("update download failed:", err);
      broadcast({
        state: "error",
        message:
          "Download failed, the update file was not found on the server.",
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
  autoUpdater.autoInstallOnAppQuit = blocker === undefined && (process.platform === "darwin" || !awaitingStagedReplacement);
  if (awaitingStagedReplacement && process.platform !== "darwin") {
    console.info(
      "install-on-quit disabled until the replacement build is staged; the cached one belongs to a channel the user left",
    );
  }
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
  if (stagedJournal.state === "replacing" || stagedJournal.state === "replacement-failed") {
    console.warn("update install blocked while replacement is incomplete");
    void dialog.showMessageBox({
      type: "warning",
      message: "Finish replacing the staged update first",
      detail: `AO cannot restart into ${stagedJournal.replacement.version} yet. Quitting outside AO may still install ${stagedJournal.staged.version}.`,
      buttons: ["OK"],
    });
    return;
  }
  if (awaitingStagedReplacement || journalPersistenceError || stagedJournalWritesPending > 0) return;
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
