import { autoUpdater } from "electron-updater";
import { CancellationToken } from "builder-util-runtime";
import { app, dialog, autoUpdater as nativeAutoUpdater } from "electron";
import { startMacUpdateProgress } from "./mac-update-progress";
import { accessSync, constants as fsConstants, existsSync, readFileSync, writeFileSync } from "node:fs";
import { mkdir, readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import semver from "semver";
import type { RequestOptions } from "node:http";
import {
  readUpdateSettings,
  updateUpdateSettings,
  writeUpdateSettings,
  UPDATE_SETTINGS_FILE_NAME,
  type UpdateChannel,
  type UpdateSettings,
  type UpdateStatus,
  type UpdateInstallResult,
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
// (e.g. "pr2270") with allowPrerelease enabled. Downgrades require a channel
// transition, including one saved on an earlier launch. Otherwise falls back to the home
// channel logic (latest vs nightly).
export function configureFeed(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): void {
  // A saved channel choice remains intent until the running build reaches it.
  // Assign after channel: electron-updater's channel setter enables downgrades.
  const allowDowngrade = isChannelTransition(settings);
  if (settings.feature !== null && settings.feature !== undefined) {
    // Feature build: pin to the pr<N> semver prerelease identifier channel.
    autoUpdater.channel = `pr${settings.feature.pr}`;
    autoUpdater.allowPrerelease = true;
    autoUpdater.allowDowngrade = allowDowngrade;
    return;
  }

  const channel: UpdateChannel = settings.channel;
  autoUpdater.channel = channel; // "latest" | "nightly"
  // Nightly builds ship as GitHub *prereleases*. With allowPrerelease false
  // (the default) electron-updater only inspects the latest NON-prerelease
  // release and looks for nightly-mac.yml there, which 404s. Enable prerelease
  // scanning on the nightly channel only; stable must never pull prereleases.
  autoUpdater.allowPrerelease = channel === "nightly";
  autoUpdater.allowDowngrade = allowDowngrade;
}

let lastStatus: UpdateStatus = { state: "idle" };
let independentStatusRevision = 0;
let eventsWired = false;
let lastCheckError: string | undefined;

// Staged-update tracking for the escalation evaluator: set on update-downloaded,
// re-evaluated every 30 minutes while the update sits uninstalled. stateDir is
// captured from whichever entry point wired the events (both receive it).
let stagedVersion: string | undefined;
// A persisted stamp is not an installer handoff in this process. In particular,
// MacUpdater has no native feed/server after relaunch until it downloads again.
let stagedInCurrentProcess = false;
let macRestartPreparation: Promise<UpdateInstallResult> | undefined;
let macRestartRequested = false;
let restartFailureHandler: (() => void) | undefined;
let macRestartProgress: Awaited<ReturnType<typeof startMacUpdateProgress>> | undefined;
let nativeReadyVersion: string | undefined;
let nativePreparationError: Error | undefined;
const NATIVE_PREPARATION_TIMEOUT_MS = 180_000;
let nativePreparationBlocked: Error | undefined;
let rejectNativeOperation: ((error: Error) => void) | undefined;
let nativePreparation: { version: string; promise: Promise<void>; finish(error?: Error): void } | undefined;

function beginNativePreparation(version: string): void {
  if (nativePreparationBlocked) return;
  if (nativePreparation) {
    nativePreparationBlocked = new Error("macOS received overlapping update requests. Close and reopen AO before retrying.");
    nativePreparation.finish(nativePreparationBlocked);
    return;
  }
  nativeReadyVersion = undefined;
  nativePreparationError = undefined;
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no; });
  // The native event can arrive outside a queued operation. Always observe rejection.
  void promise.catch(() => undefined);
  const preparation = {
    version, promise,
    finish(error?: Error) {
      if (nativePreparation !== preparation) return;
      clearTimeout(timer);
      nativePreparation = undefined;
      nativePreparationError = error;
      if (error) {
        nativeReadyVersion = undefined;
        rejectNativeOperation?.(error);
        reject(error);
      } else { nativeReadyVersion = version; resolve(); }
    },
  };
  const timer = setTimeout(() => {
    // Squirrel offers no cancellation API. Do not start another generation on
    // top of a timed-out native request, even if its JS transfer settles later.
    nativePreparationBlocked = new Error("macOS stopped responding while preparing the update. AO has stayed open. Close and reopen AO before retrying.");
    autoUpdater.autoDownload = false;
    autoUpdater.autoInstallOnAppQuit = false;
    activeDownloadCancellation?.cancel();
    preparation.finish(nativePreparationBlocked);
    broadcast(stagedDownloadedStatus());
  }, NATIVE_PREPARATION_TIMEOUT_MS);
  timer.unref?.();
  nativePreparation = preparation;
}

function isNativeInstallReady(): boolean {
  return process.platform !== "darwin" || (stagedInCurrentProcess && nativeReadyVersion !== undefined && nativeReadyVersion === stagedVersion);
}
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
  | "manual-install"
  | "settings-write"
  | "return-home"
  // Recovery cleanup. Queued rather than run inline so a download cannot start
  // into a pending cache directory that is still being emptied.
  | "cache-clear";
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
// Stalled-download watchdog. electron-updater keeps its request open when a
// download stops receiving bytes, so AO kept the last percentage forever, held
// the updater queue occupied, and offered nothing to retry. Bytes that are
// genuinely slow still advance the percentage, so inactivity is the signal, not
// elapsed time.
const DOWNLOAD_STALL_TIMEOUT_MS = 2 * 60 * 1000;
let downloadStallTimer: ReturnType<typeof setTimeout> | undefined;
let activeDownloadCancellation: CancellationToken | undefined;
let downloadStalled = false;
let manualDownloadPending = false;

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

/**
 * Where renderer pushes go.
 *
 * NOT BrowserWindow.getAllWindows(). Since #3750 the AO shell is a BaseWindow
 * hosting the UI in a WebContentsView, and BrowserWindow.getAllWindows() only
 * ever returns BrowserWindow instances — so enumerating windows here matched
 * nothing and every "updates:status" and "updates:telemetry" push was dropped
 * on the floor from 2026-08-09 onward. `invoke` handlers reply to their own
 * sender regardless of window type, so updates:getStatus kept working and the
 * breakage looked like a stale-cache bug: Settings showed a correct timestamp
 * on reopen and never moved while open.
 *
 * main.ts owns the shell handle and already pushes daemon status this way
 * (`getShellWebContents()?.send("daemon:status", …)`), so it injects the same
 * resolver here rather than this module reaching back into it.
 */
type RendererSink = { send: (channel: string, payload: unknown) => void };
let resolveRendererSink: () => RendererSink | null | undefined = () => undefined;

export function setRendererSink(resolve: () => RendererSink | null | undefined): void {
  resolveRendererSink = resolve;
}

// Resolved per send, never cached: the shell WebContents is replaced when the
// window is recreated, and holding the first one would silently stop delivering.
function sendToRenderer(channel: string, payload: unknown): void {
  resolveRendererSink()?.send(channel, payload);
}

// emitUpdateOutcome pushes an update outcome to renderers on a channel separate
// from "updates:status", so suppressing a status for UI reasons (as the
// automatic path does) never suppresses the telemetry for it.
function emitUpdateOutcome(outcome: UpdateOutcome): void {
  sendToRenderer("updates:telemetry", outcome);
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
    status.state === "preparing" ||
    status.state === "downloaded";
  const stamped: UpdateStatus = {
    ...statusWithCheckTime,
    ...stagedStamp(),
    ...(lastCheckError ? { checkError: lastCheckError } : {}),
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
  sendToRenderer("updates:status", stamped);
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
  if (status.state !== "error" && status.state !== "unsupported") {
    lastCheckedAtMs = Date.now();
    lastCheckError = undefined;
  }
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

// Retain only one public release response. Revalidate on every check: a 304
// avoids transferring/parsing the whole history without hiding a new release.
// Never use cached discovery on a network failure or for another repository.
let releaseDiscoveryCache: { url: string; etag: string; releases: GitHubReleaseSummary[] } | undefined;

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
    const url = `https://api.github.com/repos/${owner}/${repo}/releases?per_page=100`;
    const cached = releaseDiscoveryCache?.url === url ? releaseDiscoveryCache : undefined;
    const response = await fetch(
      url,
      {
        cache: "no-store",
        headers: {
          Accept: "application/vnd.github+json",
          "X-GitHub-Api-Version": "2022-11-28",
          "User-Agent": `ao-desktop/${app.getVersion()}`,
          ...(cached ? { "If-None-Match": cached.etag } : {}),
        },
        signal: AbortSignal.timeout(10000),
      },
    );
    if (response.status !== 304 && !response.ok) return undefined;
    const releases = response.status === 304 ? cached?.releases : (await response.json()) as GitHubReleaseSummary[];
    if (!Array.isArray(releases)) return undefined;
    if (response.status !== 304) {
      const etag = response.headers.get("etag");
      releaseDiscoveryCache = etag ? { url, etag, releases } : undefined;
    }
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
 * a check whose error the automatic path deliberately swallows — in both cases
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
      ...(isNativeInstallReady() ? {} : { ready: false }),
    },
  };
}

/**
 * Staged-build provenance, persisted beside the update settings.
 *
 * stagedVersion/stagedChannel are module state, so a relaunch that did NOT
 * install (a blocked location, a crash, a quit Squirrel could not finish) came
 * back knowing nothing about the build still sitting armed in the cache. A
 * channel switch after that restart could not be recognised as stranding
 * anything, which is the case stagedBuildIsStale exists to catch.
 */
const STAGED_UPDATE_FILE_NAME = "staged-update.json";

function stagedUpdateFile(stateDir: string): string {
  return path.join(stateDir, STAGED_UPDATE_FILE_NAME);
}

// Keep removal behind older writes so a failed staging attempt cannot reappear
// on relaunch, or delete the provenance of its replacement.
let stagedPersistenceQueue: Promise<unknown> = Promise.resolve();

/** Persist in event order without blocking updater events. */
function persistStagedBuild(stateDir: string | undefined): void {
  if (stateDir === undefined || stagedVersion === undefined || stagedAtMs === undefined) return;
  const payload = `${JSON.stringify({
    version: stagedVersion,
    stagedAt: stagedAtMs,
    channel: stagedChannel,
  })}\n`;
  // mkdir first: this can be the earliest write into the state dir on a fresh
  // install, and writeUpdateSettings is not guaranteed to have run yet.
  stagedPersistenceQueue = stagedPersistenceQueue
    .then(() => mkdir(stateDir, { recursive: true, mode: 0o750 }))
    .then(() => writeFile(stagedUpdateFile(stateDir), payload, { mode: 0o600 }))
    .catch(() => undefined);
}

function forgetPersistedStagedBuild(stateDir: string | undefined): void {
  if (stateDir === undefined) return;
  stagedPersistenceQueue = stagedPersistenceQueue
    .then(() => unlink(stagedUpdateFile(stateDir)))
    .catch(() => undefined);
}

/**
 * Reload provenance for a build staged by an earlier run.
 *
 * Discards it when the running build already matches, or supersedes a staged
 * build from the same channel. In both cases nothing remains pending. An older
 * build from another channel can still be an intentional channel transition.
 * Unreadable provenance is also discarded because inventing it is worse than
 * having none.
 */
function restoreStagedBuild(stateDir: string): void {
  // Synchronous on purpose. Awaiting a real filesystem read here would push the
  // launch-time update check behind an I/O turn for a file that is a few dozen
  // bytes and read exactly once per process.
  let raw: { version?: unknown; stagedAt?: unknown; channel?: unknown };
  try {
    raw = JSON.parse(readFileSync(stagedUpdateFile(stateDir), "utf8")) as typeof raw;
  } catch {
    return;
  }
  if (
    typeof raw.version !== "string" ||
    typeof raw.stagedAt !== "number" ||
    !Number.isFinite(raw.stagedAt) ||
    raw.version === app.getVersion() ||
    (raw.channel === installedUpdateChannel() &&
      semver.valid(raw.version) !== null &&
      semver.valid(app.getVersion()) !== null &&
      semver.lt(raw.version, app.getVersion()))
  ) {
    forgetPersistedStagedBuild(stateDir);
    return;
  }
  stagedVersion = raw.version;
  stagedAtMs = raw.stagedAt;
  stagedChannel = typeof raw.channel === "string" ? raw.channel : undefined;
  stagedEscalated = false;
}

/** The feed channel a settings object resolves to. Mirrors configureFeed. */
function effectiveChannel(
  settings: Pick<UpdateSettings, "channel" | "feature">,
): string {
  return settings.feature ? `pr${settings.feature.pr}` : settings.channel;
}

/** Identify the running build independently of a channel choice already saved by Settings. */
function installedUpdateChannel(): string {
  const prerelease = semver.prerelease(app.getVersion())?.[0];
  return typeof prerelease === "string" ? prerelease : "latest";
}

function isChannelTransition(settings: Pick<UpdateSettings, "channel" | "feature">): boolean {
  return effectiveChannel(settings) !== installedUpdateChannel();
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
    stagedAtMs !== undefined &&
    stagedChannel !== undefined &&
    stagedChannel !== effectiveChannel(settings)
  );
}

/**
 * Drop our tracking of a staged build that no longer belongs to the selected
 * channel, so the sidebar stops offering to restart into it. The build itself
 * stays armed until the replacement finishes downloading; that window is why
 * the replacement download is forced rather than left to the user's preference.
 */
/**
 * True from the moment a stale staged build is dropped until a replacement is
 * staged over it.
 *
 * Windows and Linux re-read autoInstallOnAppQuit inside the quit handler
 * (BaseUpdater.addQuitHandler), so clearing it there genuinely stops the
 * install. macOS cannot: MacUpdater reads the flag once, at download time, to
 * decide whether to hand the build to Squirrel, and the ShipIt waiting on
 * process exit is not recallable.
 *
 * So on Windows and Linux this closes the gap completely, and on macOS it is a
 * no-op that costs nothing. Superseding the build with a correct one stays the
 * only lever that works on all three.
 */
let awaitingStagedReplacement = false;

function discardStagedBuild(): void {
  // Nothing valid is installable until the replacement lands: the only build in
  // the cache belongs to a channel the user has left.
  awaitingStagedReplacement = true;
  nativeReadyVersion = undefined;
  stagedInCurrentProcess = false;
  forgetPersistedStagedBuild(escalationStateDir);
  offeredReleaseNotes = undefined;
  directFeedReleaseNotes = undefined;
  stagedVersion = undefined;
  stagedInCurrentProcess = false;
  stagedChannel = undefined;
  stagedAtMs = undefined;
  stagedEscalated = false;
  stagedRequestId = undefined;
  stopEscalationTimer();
}

const MAC_STAGING_FAILURE_MESSAGE =
  "macOS couldn't prepare the update because files were missing from its temporary installation copy. Check for updates and download it again to retry. If it happens again, install the latest app manually from GitHub Releases.";

function handleMacStagingFailure(err: unknown, requestId = activeUpdaterRequestId): boolean {
  if (process.platform !== "darwin") return false;
  const message = err instanceof Error ? err.message : String(err);
  // Match the installer failure, not an unrelated ENOENT or download error.
  if (
    !/ditto:/i.test(message) ||
    !/\.ShipIt\/update[^/]*\/.*\.app\//i.test(message) ||
    !/No such file or directory/i.test(message)
  ) return false;
  console.error("macOS update staging failed:", err);
  // AO's download event precedes native extraction. A failure can also arrive
  // before that stamp exists; neither case proves an installer is ready.
  discardStagedBuild();
  downloadStalled = false;
  applyInstallOnQuitPolicy();
  automaticCheckPreviousStatus = undefined;
  broadcast({
    state: "error",
    message: MAC_STAGING_FAILURE_MESSAGE,
    ...(requestId === undefined ? {} : { requestId }),
  });
  // Leave the verified ZIP alone. The failed copy is owned by ShipIt; repeating
  // the download handoff lets the native updater prepare it again. Deleting a
  // live cache here could race a newer download or an installer still reading it.
  return true;
}

/** A build is downloaded and waiting to install, and we know which one. */
function hasStagedBuild(): boolean {
  return stagedAtMs !== undefined && stagedVersion !== undefined;
}

/** The feed is offering something other than what is already staged. */
function supersedesStagedBuild(version: string | undefined): boolean {
  return hasStagedBuild() && version !== undefined && version !== stagedVersion;
}

type UpdateCheckOutcome = Awaited<ReturnType<typeof autoUpdater.checkForUpdates>>;

const UPDATE_CHECK_TIMEOUT_MS = 60_000;
const UPDATE_CHECK_TIMEOUT_MESSAGE = "Update check timed out. The update service did not respond in time. Try again.";

// electron-updater owns this executor but omits it from its public declarations.
// Limit the adapter to metadata requests; downloads retain their own cancellation.
type UpdateMetadataExecutor = {
  request(options: RequestOptions, token?: CancellationToken, data?: Record<string, unknown> | null): Promise<string | null>;
};

async function checkForUpdatesWithDeadline(): Promise<UpdateCheckOutcome> {
  const executor = (autoUpdater as typeof autoUpdater & { httpExecutor: UpdateMetadataExecutor }).httpExecutor;
  const request = executor.request;
  const tokens = new Set<CancellationToken>();
  let timedOut = false;
  executor.request = async (options, parent, data) => {
    const token = new CancellationToken(parent);
    tokens.add(token);
    if (timedOut) token.cancel();
    try {
      return await request.call(executor, options, token, data);
    } finally {
      tokens.delete(token);
      token.dispose();
    }
  };
  const timer = setTimeout(() => {
    timedOut = true;
    broadcast(withActiveRequest({ state: "error", message: UPDATE_CHECK_TIMEOUT_MESSAGE }));
    // Cancellation aborts the request AND rejects its promise. Do not race the
    // check: electron-updater must clear its cached promise before AO retries.
    for (const token of tokens) token.cancel();
  }, UPDATE_CHECK_TIMEOUT_MS);
  timer.unref?.();
  try {
    return await autoUpdater.checkForUpdates();
  } catch (err) {
    throw timedOut ? new Error(UPDATE_CHECK_TIMEOUT_MESSAGE) : err;
  } finally {
    clearTimeout(timer);
    executor.request = request;
  }
}

/**
 * Land a terminal status when a check resolved without emitting one.
 *
 * electron-updater can do exactly that: `checkForUpdates()` called while another
 * check is in flight returns the in-flight promise, and that check's events were
 * already consumed under a different operation. Nothing else ever moves the
 * status off "checking", and the Settings row keys its spinner and its disabled
 * Check button off that state, so the page wedges with no visible explanation.
 * Applied to both background and renderer-requested checks.
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
    state: nativePreparationBlocked ? "error" : isNativeInstallReady() || !stagedInCurrentProcess ? "downloaded" : "preparing",
    ...(nativePreparationBlocked ? { message: nativePreparationBlocked.message } : {}),
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
  if (lastStatus.state === "downloading" || lastStatus.state === "preparing" || nativePreparationBlocked) return;
  const evaluatedVersion = stagedVersion;
  const evaluatedAt = stagedAtMs;
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
    if (["downloading", "preparing"].includes(lastStatus.state) || stagedVersion !== evaluatedVersion || stagedAtMs !== evaluatedAt) return;
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
    if (nativePreparationBlocked) throw nativePreparationBlocked;
    // A completed proxy transfer is not completed native staging. Holding this
    // gate prevents a late native event for A being attributed to a newer B.
    if (nativePreparation) await nativePreparation.promise;
    activeUpdaterOperation = operation;
    activeUpdaterRequestId = requestId;
    activeUpdaterPhase = operation === "manual-download" ? "download" : "check";
    pendingUpdateVersion = undefined;
    if (operation === "automatic-check") {
      automaticCheckNetFailureCounted = false;
      automaticCheckFailureCounted = false;
    }
    const nativeFailure = new Promise<never>((_resolve, reject) => { rejectNativeOperation = reject; });
    try {
      await Promise.race([runOperation(), nativeFailure]);
      if (nativePreparation) await nativePreparation.promise;
      if (nativePreparationBlocked) throw nativePreparationBlocked;
    } catch (err) {
      // Recover before releasing the queue. An outer caller catch can run after
      // the next download starts and would discard that replacement's stamp.
      if ((operation === "automatic-check" || operation === "manual-check" ||
        operation === "manual-download" || operation === "return-home") &&
        handleMacStagingFailure(err, requestId)) return;
      throw err;
    } finally {
      rejectNativeOperation = undefined;
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
  lastCheckError = errorMessage(err);
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

// A staged build that the native installer refused to install, as opposed to
// anything that goes wrong while checking for or downloading one.
//
// Matching on message text is regrettable but forced: electron-updater collapses
// check failures, download failures and native install failures onto ONE untyped
// "error" event carrying a plain Error, and MacUpdater re-emits the native
// failure verbatim (`this.nativeUpdater.on("error", it => this.emit("error", it))`)
// with no code, domain or phase attached. Listening to the native updater
// directly would be structural, but it does not help: MacUpdater registers its
// own native listener in its constructor at import time and `emit` is
// synchronous, so the handler below has already run to completion before any
// listener we add later is called. Same reason isManifest404Error and
// isNetErrorMessage read the text.
//
// So the strings are pinned to the two literals Squirrel actually emits, read
// off the exact commit Electron bundles (SQRLCodeSignature.m @0e5d146):
//
//   :134  "Code signature at URL %@ did not pass validation"   -> DidNotPass
//   :116  "Failed to get static code for bundle %@"            -> CouldNotCreateStaticCode
//
// Both are SQRLCodeSignatureErrorDomain and both mean "the staged copy is not
// installable", so both take the same remedy. Everything after the colon in the
// first one is the Security-framework detail (`code object is not signed at
// all`, `code failed to satisfy specified code requirement(s)`, …) and varies
// with the damage, so it is deliberately not matched.
//
// Guardrails, because this is still a text match: it is gated on
// hasStagedBuild(), so nothing that fails while merely checking or downloading
// can reach it; a false positive costs one re-download; a false negative is the
// status quo this fixes.
const STAGED_INSTALL_REJECTION_PATTERN =
  /did not pass validation|failed to get static code for bundle/i;

/**
 * A redundant native staging request that Squirrel refused — not a failure.
 *
 * SQRLUpdater.checkForUpdatesCommand is a RACCommand built with
 * `initWithEnabled:` and never sets `allowsConcurrentExecution`, which defaults
 * to NO. RACCommand computes `moreExecutionsAllowed` as
 * `allowsConcurrentExecution ? YES : !executing`, so calling `-execute:` while a
 * staging run is in flight does not queue: it returns `[RACSignal error:]` with
 * domain RACCommandErrorDomain and code RACCommandErrorNotEnabled (1), carrying
 * the message "The command is disabled and cannot be executed".
 *
 * MacUpdater asks the native updater to fetch on every completed download, so a
 * download finishing while the previous build is still being staged produces
 * exactly this. It arrives twice — re-emitted onto electron-updater's own error
 * event, and as a rejection of the download promise (MacUpdater registers
 * `nativeUpdater.once("error", reject)` before kicking the native check off).
 * Untreated, that raw string reached the user as an update failure.
 *
 * Matched STRUCTURALLY, unlike the install-rejection pattern below: Electron's
 * three-argument AutoUpdater::OnError sets `code` and `domain` on the JS Error
 * (electron_api_auto_updater.cc), so there is no need to match on text here.
 * Verified against Electron 33.4.11, which pins Squirrel.Mac 0e5d146 and
 * ReactiveObjC 74ab5ba.
 *
 * The redundant handoff itself is a symptom of nothing owning an attempt through
 * native staging; this only stops it being reported as a failure.
 */
function isNativeStagingBusyError(err: unknown): boolean {
  const e = err as { code?: unknown; domain?: unknown };
  return e?.domain === "RACCommandErrorDomain" && e?.code === 1;
}

/**
 * Leave the UI on a truthful terminal status after something that was not a
 * failure. A manual path broadcasts "checking" before it starts, so simply
 * swallowing the error would wedge the Settings spinner.
 */
function settleWithoutFailure(): void {
  broadcastCompletedCheck(
    hasStagedBuild() ? stagedDownloadedStatus() : { state: "not-available" },
  );
}

function isStagedInstallRejection(err: unknown): boolean {
  return STAGED_INSTALL_REJECTION_PATTERN.test(errorMessage(err));
}

/**
 * Consecutive verification failures for one staged version.
 *
 * Keyed by version because the question is "has THIS build failed before", not
 * "how many failures have we seen". A different build resets the count.
 */
let installRejections: { version: string | undefined; count: number } | undefined;

/**
 * The rejection already handled and reported, so the SAME native failure
 * arriving a second time cannot be re-processed.
 *
 * MacUpdater re-emits every native Squirrel error onto electron-updater's own
 * "error" event synchronously, and the operation promise can reject with that
 * same error, so one verification failure can be delivered more than once.
 * Handling it disarms the staged build, which makes hasStagedBuild() false — so
 * without this record the second delivery misses the branch below, falls through
 * to generic handling, and replaces the actionable message with the raw Squirrel
 * signature dump (or, on the automatic path, restores the pre-check status and
 * shows nothing at all).
 *
 * Cleared when a build stages again, so a genuinely new rejection is handled in
 * full rather than swallowed.
 */
let handledInstallRejection: { version: string | undefined } | undefined;

/**
 * How many times one build may fail verification before AO stops re-preparing
 * it on every check.
 *
 * Two: the first failure buys a re-preparation from the archive already in the
 * cache, the second discards that archive. A third automatic attempt would just
 * re-download the same bytes on every check forever, which is the loop this
 * bound exists to stop.
 */
const MAX_AUTOMATIC_INSTALL_ATTEMPTS = 2;

/**
 * True once a build has used up its automatic recovery attempts.
 *
 * Checked before an automatic check arms auto-download, which is the only place
 * the loop can be broken: the download is started by checkForUpdates() itself,
 * before the offered version is known, so this cannot discriminate by version at
 * that point. It is deliberately cleared as soon as the feed offers something
 * else, or the user asks explicitly — see forgetInstallRejections.
 */
function automaticRecoveryExhausted(): boolean {
  return (
    installRejections !== undefined &&
    installRejections.count >= MAX_AUTOMATIC_INSTALL_ATTEMPTS
  );
}

/**
 * Reset the budget.
 *
 * Called when the feed offers a different build (a new target gets its own
 * attempts) and on an explicit manual check or download (the user asking again
 * is the "explicit retry" route the exhausted message points at).
 */
function forgetInstallRejections(): void {
  installRejections = undefined;
}

/** Count this rejection and report how many times this build has now failed. */
function recordInstallRejection(version: string | undefined): number {
  installRejections =
    installRejections !== undefined && installRejections.version === version
      ? { version, count: installRejections.count + 1 }
      : { version, count: 1 };
  return installRejections.count;
}

/**
 * Drop electron-updater's cached pending download.
 *
 * Verified against the published electron-updater@6.8.9 tarball:
 * DownloadedUpdateHelper.validateDownloadedPath short-circuits on existence
 * alone once a build has been downloaded by the running instance ("check here
 * only existence, not checksum"), and nothing in electron-updater clears that
 * cache when the native install fails. So without this, every later check hands
 * ShipIt the exact same staged bytes and fails identically until the app is
 * restarted.
 *
 * downloadedUpdateHelper is `protected` on AppUpdater, so it is reached through
 * a narrowed cast rather than `any`: the cast names the one member being
 * borrowed, and the optional calls make this a no-op instead of a crash if a
 * later electron-updater renames or removes it.
 */
async function clearPendingUpdateCache(): Promise<void> {
  const helper = (
    autoUpdater as unknown as {
      downloadedUpdateHelper?: { clear?: () => Promise<void> } | null;
    }
  ).downloadedUpdateHelper;
  try {
    await helper?.clear?.();
  } catch (err) {
    console.error("could not clear the cached update download:", err);
  }
}

// AO_E2E_UPDATE_SENTINEL is the absolute path the end-to-end mac update test
// (scripts/e2e-mac-update.mjs) asks the app to write once an update is actually
// STAGED on disk and ready for the ShipIt swap. Unset in every real build, so
// this is a complete no-op for users.
//
// Do not delete this while tidying: scripts/e2e-mac-update.mjs refuses to run
// against a bundle whose app.asar does not contain this exact string, so
// dropping it silently disables the whole macOS update-hop e2e job rather than
// failing it. That is what happened between #3012 and #4254, and
// e2e-mac-update.test.mjs now asserts the coupling to keep it from recurring.
export const E2E_UPDATE_SENTINEL_ENV = "AO_E2E_UPDATE_SENTINEL";

// installE2EUpdateSentinel hangs the sentinel off the NATIVE macOS updater
// (require("electron").autoUpdater, i.e. Squirrel.Mac), NOT electron-updater's
// own "update-downloaded".
//
// That distinction is load-bearing and was verified against the published
// electron-updater@6.8.9 tarball. In MacUpdater.updateDownloaded(),
// dispatchUpdateDownloaded(event) fires FIRST and only then does
// `if (this.autoInstallOnAppQuit) { this.nativeUpdater.checkForUpdates() }`
// kick Squirrel into fetching from the local proxy server. So electron-updater
// announces "downloaded" BEFORE Squirrel has fetched or staged anything: a
// harness that quits on that signal stages nothing, installs nothing, and
// reports a false failure or flaps. The native event is the one MacUpdater
// itself listens to in order to set squirrelDownloadedUpdate = true, and it is
// the only signal that means "staged, will swap on quit". See #3288.
//
// macOS only in practice: NsisUpdater and AppImageUpdater never drive the
// native updater, so this listener simply never fires off darwin.
function installE2EUpdateSentinel(): void {
  const sentinelPath = process.env[E2E_UPDATE_SENTINEL_ENV];
  if (!sentinelPath) return;
  nativeAutoUpdater.on("update-downloaded", (_event, _notes, releaseName) => {
    try {
      // Written synchronously: the harness quits the app right after seeing
      // this file, so an async write could lose the race with termination.
      writeFileSync(
        sentinelPath,
        `${JSON.stringify({ stagedAt: Date.now(), releaseName: releaseName ?? null })}\n`,
      );
      console.info(`[e2e] native updater staged ${releaseName ?? "an update"}; wrote ${sentinelPath}`);
    } catch (err) {
      console.error("[e2e] failed to write update sentinel:", err);
    }
  });
}

// wireUpdaterEvents registers electron-updater listeners once and forwards each
// to the renderer as an UpdateStatus. Idempotent: safe to call on every entry
// point (launch auto-check and manual check).
function wireUpdaterEvents(): void {
  if (eventsWired) return;
  eventsWired = true;
  if (process.platform === "darwin") {
    nativeAutoUpdater.on("update-downloaded", (_event, _notes, releaseName) => {
      if (!nativePreparation || (releaseName && releaseName !== nativePreparation.version)) return;
      nativePreparation.finish();
      broadcast(lastStatus.state === "downloading" ? lastStatus : stagedDownloadedStatus());
    });
    nativeAutoUpdater.on("error", (error) => {
      nativePreparation?.finish(error);
      nativeReadyVersion = undefined;
      nativePreparationError = error;
      if (macRestartRequested) {
        macRestartRequested = false;
        macRestartPreparation = undefined;
        stagedInCurrentProcess = false;
        void macRestartProgress?.fail(errorMessage(error)).catch(() => undefined);
        broadcast({ state: "error", message: errorMessage(error) });
        // Squirrel can close the windows, then fail to persist its relaunch
        // request. Restore AO in that still-running process instead of leaving
        // the user with no app window and no possible automatic restart.
        restartFailureHandler?.();
      }
    });
  }
  // Registered last so a native update-downloaded reaches the sentinel handler
  // through the test harness's single-handler map lookup.
  installE2EUpdateSentinel();
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
    // A different build is a different target, so it starts with a full budget
    // even if the previous one exhausted its own.
    if (
      installRejections !== undefined &&
      info?.version !== installRejections.version
    ) {
      forgetInstallRejections();
    }
    pendingUpdateVersion = info?.version;
    offeredReleaseNotes = normalizeReleaseNotes(info?.releaseNotes) ?? directFeedReleaseNotes;
    broadcastCompletedCheck({ state: "available", version: info?.version });
    if (autoUpdater.autoDownload) {
      activeUpdaterPhase = "download";
      broadcastUpdaterStatus({ state: "downloading", version: info?.version });
      armDownloadStallWatchdog();
    }
  });
  autoUpdater.on("update-cancelled", () => {
    clearDownloadStallWatchdog();
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
    if (p?.transferred === undefined || p.transferred !== lastStatus.transferred) armDownloadStallWatchdog();
    return broadcastUpdaterStatus({
      state: p?.percent >= 100 ? "preparing" : "downloading",
      version: pendingUpdateVersion,
      percent: Math.max(0, Math.min(100, Math.floor(p?.percent ?? 0))),
      transferred: p?.transferred,
      total: p?.total,
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
    // Re-staging the SAME build must not restart the staged clock. electron-updater
    // re-runs its download task whenever a check finds a version it has already
    // cached, so this event repeats on every automatic check until the user quits.
    // Resetting stagedAtMs there would mean the latest-channel 48h escalation rule
    // could never fire, because the clock is only ever minutes old.
    const restaged = stagedAtMs !== undefined && info?.version === stagedVersion;
    stagedVersion = info?.version;
    stagedInCurrentProcess = true;
    if (process.platform === "darwin" && stagedVersion) beginNativePreparation(stagedVersion);
    stagedChannel = autoUpdater.channel ?? undefined;
    offeredReleaseNotes =
      normalizeReleaseNotes(info?.releaseNotes) ?? offeredReleaseNotes ?? directFeedReleaseNotes;
    if (!restaged) {
      stagedAtMs = Date.now();
      stagedEscalated = false;
    }
    stagedRequestId = activeUpdaterRequestId;
    // A build is staged again, so install-on-quit has something correct to run,
    // and a later rejection is a NEW one rather than a repeat delivery.
    handledInstallRejection = undefined;
    awaitingStagedReplacement = false;
    applyInstallOnQuitPolicy();
    persistStagedBuild(escalationStateDir);
    automaticCheckPreviousStatus = undefined;
    // A completed automatic download advances the independent baseline; a
    // renderer-requested download additionally carries its request ownership.
    broadcast(withActiveRequest(stagedDownloadedStatus()));
    // Evaluate now (nightly can escalate immediately), then every 30 minutes
    // while the update sits uninstalled. unref so the timer never holds the
    // process open on quit.
    void runEscalationCheck();
    // Re-arming on a re-stage would push the next evaluation out by another 30
    // minutes every time, and the nightly channel re-stages every 15 — the loop
    // would never get a turn. Leave the running timer alone in that case.
    if (!restaged || escalationTimer === undefined) {
      stopEscalationTimer();
      escalationTimer = setInterval(
        () => void runEscalationCheck(),
        30 * 60 * 1000,
      );
      escalationTimer.unref?.();
    }
  });
  autoUpdater.on("error", (err) => {
    clearDownloadStallWatchdog();
    if (handleMacStagingFailure(err)) {
      emitUpdateFailure(err);
      return;
    }
    if (downloadStalled) {
      // Our own cancellation surfacing as an error. The stall status is already
      // published and is more useful than "cancelled"; replacing it would lose
      // the retry wording.
      downloadStalled = false;
      console.info("update download cancelled after stalling:", err);
      return;
    }
    if (isNativeStagingBusyError(err)) {
      // Squirrel is already staging a build; this request was refused, nothing
      // failed. Reporting it would replace a true status with a native string
      // the user cannot act on.
      console.info("native staging already in progress; request refused:", err);
      return;
    }
    // Never crash on update failure (offline, unsigned macOS, etc.).
    // A one-off automatic failure restores the previous status so the UI does
    // not flash an error the user never asked for. That suppression is a UI
    // decision and must not suppress the telemetry: automatic checks are the
    // main way an install goes silently stale.
    emitUpdateFailure(err);
    // The native installer rejected the build already sitting in the cache
    // (#4254). This is the one failure class that cannot be left to the
    // automatic path's suppress-and-retry, because retrying it is exactly what
    // does not work: electron-updater re-serves the same cached bytes to
    // Squirrel on every subsequent check for the lifetime of this process,
    // rather than a fresh download — while the UI keeps offering a restart that
    // nothing has re-verified.
    //
    // Dropping the cached download turns the next check back into a real
    // download-and-verify instead of a replay, and disarming the staged state
    // stops the sidebar promising an install that is no longer possible. The
    // cost is one re-download when a rejection was transient, which is the
    // right trade against an install that is otherwise stuck until relaunch.
    //
    // Deliberately narrow: the richer in-app remediation for this class (the
    // direct-download offer after repeated failures) belongs to #3528, and the
    // pre-v0.11.0-baseline hop that provokes it belongs to #3288's matrix.
    if (isStagedInstallRejection(err)) {
      // A repeat delivery of a rejection already handled: keep the actionable
      // message that is on screen rather than letting this fall through and
      // overwrite it. Deliberately not a general "last error" deduplicator —
      // it matches only this class, and only while no build is staged.
      if (!hasStagedBuild() && handledInstallRejection !== undefined) {
        console.debug("ignoring a duplicate delivery of an install rejection:", err);
        return;
      }
    }
    // NOTE for the readiness work (Prepare phase): this branch is gated on
    // hasStagedBuild(), which reads stagedAtMs. Moving stagedAtMs to the native
    // update-downloaded event makes this guard FALSE at exactly the moment a
    // verification failure arrives, silently reclassifying install rejections as
    // generic check errors. Re-anchor it to the active native preparation in the
    // same change that moves the assignment.
    if (hasStagedBuild() && isStagedInstallRejection(err)) {
      // Squirrel verifies the bundle it just extracted, in this process, before
      // any ShipIt request exists. So a rejection indicts the EXTRACTED COPY.
      //
      // It does not by itself prove the cached archive is bad: electron-updater
      // checked that archive against the feed's sha512 when it downloaded it,
      // which establishes agreement with the feed AT THAT TIME — not a correctly
      // signed release, and not the absence of later damage to the cache. So one
      // re-preparation is worth attempting before the download is discarded.
      //
      // Disarm either way: the copy Squirrel holds cannot install, and leaving
      // it staged makes the UI promise a restart that fails. Dropping the staged
      // record re-enables auto-download, so the next check re-stages and
      // re-prepares from the archive already in the cache.
      //
      const failures = recordInstallRejection(stagedVersion);
      const exhausted = failures >= MAX_AUTOMATIC_INSTALL_ATTEMPTS;
      handledInstallRejection = { version: stagedVersion };
      discardStagedBuild();
      // Only once a re-preparation has ALSO failed is the archive worth
      // suspecting. Purging earlier costs a full re-download to fix a copy that
      // may well prepare cleanly on the next attempt.
      //
      // Queued on the operation chain rather than fired and forgotten:
      // discardStagedBuild() re-enables auto-download, so the next check can
      // start a download into the very directory this is emptying.
      if (exhausted) {
        void runSerializedUpdaterOperation(
          "cache-clear",
          clearPendingUpdateCache,
        ).catch(() => undefined);
      }
      console.error(
        `staged update rejected at install time (attempt ${failures}${exhausted ? ", discarding cached download and stopping automatic retries" : ""}):`,
        err,
      );
      broadcast(
        withActiveRequest({
          state: "error",
          message:
            exhausted
              ? "Couldn't install the update — the copy failed verification twice. " +
                "AO has discarded the download and stopped retrying on its own. Check " +
                "for updates again to start a fresh one, or download the latest build " +
                "manually and install it over this one."
              : "Couldn't install the update — the downloaded copy failed verification. " +
                "AO will prepare it again on the next check.",
        }),
      );
      return;
    }
    if (activeUpdaterOperation === "automatic-check" && activeUpdaterPhase === "check") {
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
        lastCheckError = errorMessage(err);
        broadcastUpdaterStatus(stagedDownloadedStatus());
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
    ...stagedStamp(),
    ...(lastCheckError ? { checkError: lastCheckError } : {}),
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

/**
 * Own a download that the check itself started.
 *
 * With `autoDownload` set, `doCheckForUpdates()` kicks the download off and
 * returns its promise WITHOUT awaiting it — deliberately, marked
 * `noinspection ES6MissingAwait` in AppUpdater. If the serialized operation
 * returns without awaiting that promise, the download, the localhost handoff to
 * Squirrel and the native staging behind it all continue after the operation has
 * settled: the queue lets the next check or download start on top of them, and a
 * `finally` that restores the feed runs while the download is still using it.
 *
 * Awaited regardless of the auto-update preference. The preference governs
 * whether a download is STARTED, not whether one already running is owned — and
 * a stale staged build forces a download precisely when the preference is off,
 * to supersede a build the user has moved away from.
 *
 * Sets the download phase so a failure here is attributed to the download rather
 * than to the check that started it.
 */
async function awaitStartedDownload(result: UpdateCheckOutcome): Promise<void> {
  if (!result?.downloadPromise) return;
  activeUpdaterPhase = "download";
  pendingUpdateVersion = result.updateInfo?.version;
  // The provider owns this download's token; hand it to the watchdog so a stall
  // can actually be cancelled rather than just reported.
  activeDownloadCancellation = result.cancellationToken;
  if (lastStatus.state === "available" || lastStatus.state === "checking") {
    broadcastUpdaterStatus({ state: "downloading", version: pendingUpdateVersion });
  }
  if (lastStatus.state === "downloading" || lastStatus.state === "preparing") armDownloadStallWatchdog();
  try {
    await result.downloadPromise;
  } finally {
    clearDownloadStallWatchdog();
  }
}

async function runAutomaticUpdateCheck(
  stateDir: string,
): Promise<number> {
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
      // user went without quitting — 175 MB of copying and a ShipIt spawn every
      // 15 minutes on nightly. Anything genuinely newer than the staged build is
      // still fetched, below.
      // A staged build from a channel the user has left is already armed with
      // the OS installer; the replacement must be fetched even when automatic
      // downloading is off, or quitting installs the build they moved away from.
      const staleStaged = stagedBuildIsStale(settings);
      if (staleStaged) discardStagedBuild();
      // automaticRecoveryExhausted() breaks the re-download loop: without it a
      // build that keeps failing verification is fetched and re-prepared on
      // every check, forever. A stale staged build still overrides, because
      // leaving THAT one armed installs a channel the user has left.
      autoUpdater.autoDownload =
        staleStaged || (hasStagedBuild() && !isNativeInstallReady()) ||
        (settings.enabled && !hasStagedBuild() && !automaticRecoveryExhausted());
      applyInstallOnQuitPolicy();
      // Only prerelease channels resolve a direct feed. Skipping the await on
      // stable keeps that check's event ordering exactly as it was.
      const restoreFeed = directPrereleaseChannel(settings)
        ? await configureDirectPrereleaseFeed(settings)
        : undefined;
      try {
        const result = await checkForUpdatesWithDeadline();
        settleCheckStatus(result);
        if (result?.downloadPromise) {
          await awaitStartedDownload(result);
        } else if (settings.enabled) {
          if (
            result?.isUpdateAvailable === true &&
            supersedesStagedBuild(result.updateInfo?.version)
          ) {
            // autoDownload was suspended for the staged build, but this is a
            // different version, so it still has to be fetched automatically.
            activeUpdaterPhase = "download";
            pendingUpdateVersion = result?.updateInfo?.version;
            const token = new CancellationToken();
            activeDownloadCancellation = token;
            broadcastUpdaterStatus({ state: "downloading", version: pendingUpdateVersion });
            armDownloadStallWatchdog();
            await autoUpdater.downloadUpdate(token);
          }
        }
      } catch (err) {
        // electron-updater normally also emits "error" (handled in
        // wireUpdaterEvents); a reject-only failure must still restore the
        // pre-check status so the renderer is neither stuck on "checking" nor
        // denied the stale-check nudge once the streak crosses the threshold
        // (#3526). Record before restoring so the restore broadcast is stamped.
        if (handleMacStagingFailure(err)) return;
        if (activeUpdaterPhase === "download") {
          if (!downloadStalled && lastStatus.state !== "error") broadcast(withActiveRequest({ state: "error", message: errorMessage(err), version: pendingUpdateVersion }));
        } else {
          recordAutomaticCheckFailure(err);
          restoreAutomaticCheckPreviousStatus();
          publishFailingChecks();
        }
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
  restoreStagedBuild(stateDir);
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
// opt-in, so a user who never enabled auto-updates can still discover the latest
// build from Settings. Downloads follow the saved preference, and it
// reports progress via the broadcast status. Updates only work in the packaged,
// signed app; in dev electron-updater has no feed, so surface that plainly.
export async function checkForUpdatesNow(
  stateDir: string,
  options: UpdateCheckOptions = {},
): Promise<void> {
  escalationStateDir = stateDir;
  wireUpdaterEvents();
  // Asking again IS the explicit retry the exhausted message points at.
  forgetInstallRejections();
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
  // Which phase a failure came from. The queue clears global operation state in
  // its own `finally` before this function's catch runs, and a queued operation
  // can reset the module-level phase, so the distinction is captured locally
  // while the operation is still on the stack. Boxed because the assignment
  // happens inside the operation closure.
  const failed: { phase: UpdatePhase } = { phase: "check" };
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
        if (staleStaged) discardStagedBuild();
        autoUpdater.autoDownload = staleStaged || (hasStagedBuild() && !isNativeInstallReady());
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        const restoreFeed = await configureDirectPrereleaseFeed(settings);
        try {
          const result = await checkForUpdatesWithDeadline();
          settleCheckStatus(result);
          // A stale staged build forces a download above; own it here so the
          // feed is not restored, and the next operation not started, while it
          // is still running.
          if (result?.downloadPromise) failed.phase = "download";
          await awaitStartedDownload(result);
          if (!result?.downloadPromise && settings.enabled && result?.isUpdateAvailable &&
              (!hasStagedBuild() || supersedesStagedBuild(result.updateInfo?.version))) {
            failed.phase = "download";
            activeUpdaterPhase = "download";
            pendingUpdateVersion = result.updateInfo?.version;
            const token = new CancellationToken();
            activeDownloadCancellation = token;
            broadcastUpdaterStatus({ state: "downloading", version: pendingUpdateVersion });
            armDownloadStallWatchdog();
            await autoUpdater.downloadUpdate(token);
          }
        } finally {
          restoreFeed?.();
        }
      },
      options.requestId,
    );
  } catch (err) {
    if (isNativeStagingBusyError(err)) {
      console.info("manual check refused: native staging already in progress:", err);
      settleWithoutFailure();
      return;
    }
    if (isManifest404Error(err)) {
      console.info(`manual update ${failed.phase} failed:`, err);
      broadcastCompletedCheck({
        state: "error",
        message:
          failed.phase === "download"
            ? "Download failed — the update file was not found on the server."
            : "Couldn't check for updates — the update information was not found on the server.",
        ...(options.requestId === undefined ? {} : { requestId: options.requestId }),
      });
      if (stagedAtMs !== undefined) {
        lastCheckError = errorMessage(err);
        broadcast(stagedDownloadedStatus());
      }
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
  // See checkForUpdatesNow: boxed so the closure assignment is visible here.
  const failed: { phase: UpdatePhase } = { phase: "check" };
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
        if (staleStaged) discardStagedBuild();
        autoUpdater.autoDownload = staleStaged || (hasStagedBuild() && !isNativeInstallReady());
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        const result = await checkForUpdatesWithDeadline();
        settleCheckStatus(result);
        // Same ownership rule as the other two paths: leaving a pinned build is
        // a channel switch, so the replacement download is forced here too.
        if (result?.downloadPromise) failed.phase = "download";
        await awaitStartedDownload(result);
      },
      requestId,
    );
  } catch (err) {
    if (isNativeStagingBusyError(err)) {
      console.info("return home refused: native staging already in progress:", err);
      settleWithoutFailure();
      return;
    }
    broadcast({
      state: "error",
      message:
        (err as Error)?.message ??
        (failed.phase === "download" ? "Download failed" : "Return failed"),
      ...(requestId === undefined ? {} : { requestId }),
    });
  }
}

// downloadUpdateNow starts downloading the update found by checkForUpdatesNow.
export async function downloadUpdateNow(requestId?: string): Promise<void> {
  if (manualDownloadPending || lastStatus.state === "downloading" || lastStatus.state === "preparing") return;
  const version = lastStatus.version;
  wireUpdaterEvents();
  forgetInstallRejections();
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
  manualDownloadPending = true;
  broadcast({ state: "downloading", version, requestId });
  try {
    await runSerializedUpdaterOperation(
      "manual-download",
      async () => {
        // Manual downloads get no provider token, so make one: without it the
        // watchdog could report a stall but never release the request.
        const token = new CancellationToken();
        if (lastStatus.state === "downloaded" || lastStatus.state === "preparing") return;
        pendingUpdateVersion = lastStatus.version ?? version;
        broadcastUpdaterStatus({ state: "downloading", version: pendingUpdateVersion });
        activeDownloadCancellation = token;
        armDownloadStallWatchdog();
        await autoUpdater.downloadUpdate(token);
      },
      requestId,
    );
  } catch (err) {
    if (lastStatus.state === "error" && lastStatus.message === "Download stopped responding. Try again.") return;
    if (isNativeStagingBusyError(err)) {
      console.info("download refused: native staging already in progress:", err);
      settleWithoutFailure();
      return;
    }
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
  } finally {
    manualDownloadPending = false;
    clearDownloadStallWatchdog();
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
  autoUpdater.autoInstallOnAppQuit = blocker === undefined && !awaitingStagedReplacement && !nativePreparationBlocked;
  if (awaitingStagedReplacement) {
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
export function setUpdateRestartFailureHandler(handler: () => void): void { restartFailureHandler = handler; }

export function isUpdateRestartRequested(): boolean { return macRestartRequested; }

export async function quitAndInstallUpdate(confirmedVersion?: string): Promise<UpdateInstallResult> {
  if (confirmedVersion !== undefined && (typeof confirmedVersion !== "string" || !confirmedVersion.trim())) {
    throw new Error("A valid confirmed update version is required.");
  }
  if (!app.isPackaged) return;
  if (awaitingStagedReplacement) {
    throw new Error("Check for updates and download an update before restarting to install.");
  }
  const blocker = getMacInstallBlocker();
  if (blocker !== undefined) {
    throw new Error(blocker);
  }
  if (process.platform !== "darwin") {
    if (!hasStagedBuild() || lastStatus.state === "downloading" || lastStatus.state === "preparing") {
      throw new Error("The update is not ready to install. Check for updates again.");
    }
    if (confirmedVersion !== undefined && stagedVersion && confirmedVersion !== stagedVersion) {
      return { state: "confirmation-required", version: stagedVersion,
        releaseNotes: lastStatus.state === "downloaded" && lastStatus.version === stagedVersion ? lastStatus.releaseNotes : undefined };
    }
    autoUpdater.quitAndInstall(false, true);
    return;
  }
  if (macRestartPreparation) return macRestartPreparation;
  let confirmation: UpdateInstallResult;
  macRestartPreparation = runSerializedUpdaterOperation("manual-install", async () => {
    let progress: Awaited<ReturnType<typeof startMacUpdateProgress>> | undefined;
    try {
      if (!escalationStateDir || !hasStagedBuild()) {
        throw new Error("Check for updates and download an update before restarting to install.");
      }
      if (!stagedInCurrentProcess) {
        confirmation = await prepareRememberedMacUpdate(confirmedVersion);
        if (confirmation) return;
      }
      if (confirmedVersion !== undefined && stagedVersion && confirmedVersion !== stagedVersion) {
        confirmation = { state: "confirmation-required", version: stagedVersion,
          releaseNotes: lastStatus.state === "downloaded" && lastStatus.version === stagedVersion ? lastStatus.releaseNotes : undefined };
        return;
      }
      const version = stagedVersion;
      if (!version) throw new Error("The update is no longer ready to install. Check for updates again.");
      await waitForNativePreparation(version);
      // The helper must acknowledge it is up (its READY handshake) before AO
      // quits. It stays hidden on the normal path and only shows a window if the
      // update stalls or fails.
      progress = await startMacUpdateProgress({
        stateDir: escalationStateDir,
        resourcesPath: process.resourcesPath,
        appPath: path.resolve(process.execPath, "..", "..", ".."),
        version,
      });
      progress.assertAlive();
      macRestartProgress = progress;
      macRestartRequested = true;
      autoUpdater.quitAndInstall(false, true);
      if (!macRestartRequested) throw nativePreparationError ?? new Error("The installer could not restart AO.");
    } catch (err) {
      macRestartRequested = false;
      stagedInCurrentProcess = false;
      nativeReadyVersion = undefined;
      console.error("failed to prepare update for restart:", err);
      await progress?.fail(errorMessage(err)).catch(() => undefined);
      broadcast({ state: "error", message: errorMessage(err) });
      throw err;
    }
  }).then(() => confirmation).finally(() => { if (!macRestartRequested) macRestartPreparation = undefined; });
  return macRestartPreparation;
}

async function waitForNativePreparation(version: string): Promise<void> {
  if (nativePreparationBlocked) throw nativePreparationBlocked;
  if (nativePreparation?.version === version) await nativePreparation.promise;
  if (nativePreparationError) throw nativePreparationError;
  if (nativeReadyVersion !== version || stagedVersion !== version) {
    throw new Error("The update is not ready in macOS. Check for updates again.");
  }
}

async function prepareRememberedMacUpdate(confirmedVersion?: string): Promise<UpdateInstallResult> {
  if (!escalationStateDir) throw new Error("Check for updates before restarting to install.");
  const settings = await reconcileAndPersist(escalationStateDir, await readUpdateSettings(escalationStateDir));
  configureFeed(settings);
  autoUpdater.autoDownload = false;
  applyInstallOnQuitPolicy();
  broadcastUpdaterStatus({ state: "checking" });
  const restoreFeed = await configureDirectPrereleaseFeed(settings);
  try {
    const result = await checkForUpdatesWithDeadline();
    if (result?.isUpdateAvailable !== true) {
      throw new Error("The remembered update is no longer available on the selected channel. Check for updates and try again.");
    }
    // A remembered stamp is not permission to install a different release.
    // Stop before downloading/arming it so cancelling the new confirmation
    // cannot install the unconfirmed target on a later ordinary quit.
    if (confirmedVersion !== undefined && result.updateInfo.version !== confirmedVersion) {
      return {
        state: "confirmation-required",
        version: result.updateInfo.version,
        releaseNotes: normalizeReleaseNotes(result.updateInfo.releaseNotes) ?? directFeedReleaseNotes,
      };
    }
    activeUpdaterPhase = "download";
    pendingUpdateVersion = result.updateInfo.version;
    const token = new CancellationToken();
    activeDownloadCancellation = token;
    // A cache hit re-establishes the native feed. The download promise is not
    // native readiness: waitForNativePreparation separately gates the quit.
    await autoUpdater.downloadUpdate(token);
  } finally {
    restoreFeed?.();
  }
}

// ensureUpdatePrefs prompts once (first run, before any settings file exists)
// for auto-update opt-in + channel, with a nightly instability disclaimer.
export async function ensureUpdatePrefs(stateDir: string): Promise<void> {
  if (existsSync(path.join(stateDir, UPDATE_SETTINGS_FILE_NAME))) return;

  const installedNightly = installedUpdateChannel() === "nightly";
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
      channel: installedNightly ? "nightly" : "latest",
      nightlyAck: installedNightly,
      feature: null,
    });
    return;
  }

  const chan = await dialog.showMessageBox({
    type: "question",
    buttons: ["Stable", "Nightly"],
    defaultId: installedNightly ? 1 : 0,
    cancelId: installedNightly ? 1 : 0,
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
