import { autoUpdater as stockAutoUpdater } from "electron-updater";
import { MacDifferentialV2Updater } from "./mac-differential-v2-updater";
import macV2TrustedKeys from "../../scripts/mac-differential-v2-trust.json";
import macDifferentialRollout from "../../scripts/mac-differential-rollout.json";
import { CancellationToken } from "builder-util-runtime";
import { app, BrowserWindow, dialog } from "electron";
import { accessSync, constants as fsConstants, existsSync, readFileSync } from "node:fs";
import { mkdir, readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import semver from "semver";
import {
  readUpdateSettings,
  macDifferentialUpdatesEnabled,
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
  updateFailureCategory,
  type UpdateOutcome,
  type UpdatePhase,
  type UpdateTrigger,
} from "../shared/update-telemetry";

// Current AO uses the stock full-ZIP path. A future compatible build explicitly
// selects the v2 subclass; old clients never learn its metadata or map URLs.
const autoUpdater = process.platform === "darwin" && macDifferentialRollout.enabled === true
  ? new MacDifferentialV2Updater({ trustedKeys: macV2TrustedKeys })
  : stockAutoUpdater;

const FAIL_CLOSED_UPDATE_SETTINGS: UpdateSettings = {
  enabled: false,
  channel: "latest",
  nightlyAck: false,
  feature: null,
  macDifferentialUpdates: false,
};
let developerModeHydrated = false;
let developerModeRequested = false;
let differentialEligible = false;
let pendingTargetBytes: number | undefined;
let offeredUpdateVersion: string | undefined;
let offeredMacFiles: Array<{ url: string; size?: number }> = [];

function selectMacTargetBytes(arm64: boolean): void {
  const hasArm64 = offeredMacFiles.some(file => file.url.includes("arm64"));
  const file = offeredMacFiles.find(file =>
    /\.zip(?:$|[?#])/i.test(file.url) && file.url.includes("arm64") === (arm64 && hasArm64));
  pendingTargetBytes = typeof file?.size === "number" && Number.isFinite(file.size) && file.size >= 0
    ? file.size : undefined;
}
let transferObservation = {
  eligible: false,
  attemptedDifferential: false,
  fallback: false,
  transferred: undefined as number | undefined,
};
let updaterLoggerWired = false;

// electron-updater defaults this flag to false on macOS. Override it before
// any renderer or settings hydration can race an update operation.
if (process.platform === "darwin") autoUpdater.disableDifferentialDownload = true;

export function applyUpdaterPolicy(
  settings: UpdateSettings,
  platform: NodeJS.Platform = process.platform,
): void {
  // Keep this gate closed until the dependency and older-client feed isolation
  // contracts pass. This repository never generates macOS release sidecars.
  const eligible = macDifferentialRollout.enabled === true && developerModeHydrated && macDifferentialUpdatesEnabled({ platform, settings });
  differentialEligible = eligible;
  if (platform === "darwin") autoUpdater.disableDifferentialDownload = !eligible;
  console.info("[auto-updater] mac differential policy", {
    eligible,
    platform,
    channel: settings.channel,
    featurePinned: settings.feature !== null,
    developerMode: settings.macDifferentialUpdates === true,
  });
}

// This observes the pinned dependency's phase messages, never its raw URLs,
// paths, HTTP headers or error stacks. Unknown messages cannot leak credentials.
function wireUpdaterLogger(): void {
  if (updaterLoggerWired || process.platform !== "darwin") return;
  updaterLoggerWired = true;
  const base = autoUpdater.logger ?? console;
  const observe = (level: "info" | "warn" | "error" | "debug", first: unknown) => {
    const message = typeof first === "string" ? first : "";
    if (message === "Checked for macOS Rosetta environment (isRosetta=true)" ||
        message === "Checked 'uname -a': arm64=true") {
      selectMacTargetBytes(true);
    }
    if (message.startsWith("Download block maps") || message.startsWith("Differential download:")) {
      transferObservation.attemptedDifferential = true;
      base.info("[auto-updater] differential transfer attempted");
    } else if (/(?:fall(?:ing)? back|fallback) to full download/i.test(message)) {
      transferObservation.fallback = true;
      base.warn("[auto-updater] differential transfer fell back to full download");
    } else if (level === "warn" || level === "error") {
      base[level](`[auto-updater] ${updateFailureCategory(message)}`);
    }
  };
  autoUpdater.logger = {
    info: (first: unknown) => observe("info", first),
    warn: (first: unknown) => observe("warn", first),
    error: (first: unknown) => observe("error", first),
    debug: (first: unknown) => observe("debug", first),
  };
}

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

// emitUpdateOutcome pushes an update outcome to renderers on a channel separate
// from "updates:status", so suppressing a status for UI reasons (as the
// automatic path does) never suppresses the telemetry for it.
function emitUpdateOutcome(outcome: UpdateOutcome): void {
  if (outcome.phase === "download" && process.platform === "darwin") {
    outcome = {
      ...outcome,
      differential_eligible: transferObservation.eligible,
      transfer_mode: transferObservation.attemptedDifferential ? "differential" : "full",
      fallback: transferObservation.fallback,
      ...(transferObservation.transferred === undefined ? {} : { transferred_bytes: transferObservation.transferred }),
      ...(pendingTargetBytes === undefined ? {} : { target_bytes: pendingTargetBytes }),
    };
  }
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

/** Fire-and-forget: losing this file costs provenance, never correctness. */
function persistStagedBuild(stateDir: string | undefined): void {
  if (stateDir === undefined || stagedVersion === undefined || stagedAtMs === undefined) return;
  const payload = `${JSON.stringify({
    version: stagedVersion,
    stagedAt: stagedAtMs,
    channel: stagedChannel,
  })}\n`;
  // mkdir first: this can be the earliest write into the state dir on a fresh
  // install, and writeUpdateSettings is not guaranteed to have run yet.
  void mkdir(stateDir, { recursive: true, mode: 0o750 })
    .then(() => writeFile(stagedUpdateFile(stateDir), payload, { mode: 0o600 }))
    .catch(() => undefined);
}

function forgetPersistedStagedBuild(stateDir: string | undefined): void {
  if (stateDir === undefined) return;
  void unlink(stagedUpdateFile(stateDir)).catch(() => undefined);
}

/**
 * Reload provenance for a build staged by an earlier run.
 *
 * Discards it when the running version already matches — that build installed,
 * so nothing is pending — and when anything is unreadable, because inventing
 * provenance is worse than having none.
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
    raw.version === app.getVersion()
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
  forgetPersistedStagedBuild(escalationStateDir);
  offeredReleaseNotes = undefined;
  directFeedReleaseNotes = undefined;
  stagedVersion = undefined;
  stagedChannel = undefined;
  stagedAtMs = undefined;
  stagedEscalated = false;
  stagedRequestId = undefined;
  stopEscalationTimer();
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
    activeUpdaterRequestId = requestId;
    activeUpdaterPhase = operation === "manual-download" ? "download" : "check";
    pendingUpdateVersion = operation === "manual-download" ? offeredUpdateVersion : undefined;
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
        applyUpdaterPolicy(settings);
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
  wireUpdaterLogger();
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
    offeredMacFiles = Array.isArray(info?.files) ? info.files : [];
    offeredUpdateVersion = info?.version;
    selectMacTargetBytes(process.arch === "arm64");
    transferObservation = {
      eligible: differentialEligible,
      attemptedDifferential: false,
      fallback: false,
      transferred: undefined,
    };
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
    offeredReleaseNotes = normalizeReleaseNotes(info?.releaseNotes) ?? directFeedReleaseNotes;
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
    const transferred = Number.isFinite(p?.transferred) && p.transferred >= 0 ? p.transferred : undefined;
    const total = Number.isFinite(p?.total) && p.total >= 0 ? p.total : undefined;
    const bytesPerSecond = Number.isFinite(p?.bytesPerSecond) && p.bytesPerSecond >= 0 ? p.bytesPerSecond : undefined;
    transferObservation.transferred = transferred;
    return broadcastUpdaterStatus({
      state: "downloading",
      version: pendingUpdateVersion,
      percent: Math.max(0, Math.min(100, Math.round(p?.percent ?? 0))),
      ...(transferred === undefined ? {} : { transferred }),
      ...(total === undefined ? {} : { total }),
      ...(bytesPerSecond === undefined ? {} : { bytesPerSecond }),
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
      applyUpdaterPolicy(settings);
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
        if (settings.enabled) {
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
  restoreStagedBuild(stateDir);
  startRetirementPollTimer(stateDir);
  const intervalMs = await requestAutomaticUpdateCheck(stateDir);
  if (intervalMs !== undefined)
    schedulePeriodicAutomaticUpdateCheck(stateDir, intervalMs);
}

// The mirror belongs to Developer Mode IPC. A stale settings form must not
// restore an old value when changing channel or automatic-download preference.
async function persistRendererUpdateSettings(
  stateDir: string,
  settings: UpdateSettings,
): Promise<UpdateSettings> {
  return updateUpdateSettings(stateDir, current => ({
    ...settings,
    macDifferentialUpdates: current.macDifferentialUpdates === true,
  }));
}

async function persistUpdaterSettings(
  stateDir: string,
  settings: UpdateSettings,
): Promise<void> {
  const next = await persistRendererUpdateSettings(stateDir, settings);
  applyUpdaterPolicy(next);
  configureFeed(next);
  reconcileAutomaticUpdateSchedule(stateDir, next);
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
        const requested = options.settings
          ? await persistRendererUpdateSettings(stateDir, options.settings)
          : await readUpdateSettings(stateDir);
        const settings = await reconcileAndPersist(stateDir, requested);
        applyUpdaterPolicy(settings);
        reconcileAutomaticUpdateSchedule(stateDir, settings);
        configureFeed(settings);
        // Same reason as the automatic path: a channel switch leaves the old
        // channel's build armed, and only staging the new one over it helps.
        const staleStaged = stagedBuildIsStale(settings);
        if (staleStaged) discardStagedBuild();
        autoUpdater.autoDownload = staleStaged;
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        const restoreFeed = await configureDirectPrereleaseFeed(settings);
        try {
          settleCheckStatus(await autoUpdater.checkForUpdates());
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
        applyUpdaterPolicy(settings);
        reconcileAutomaticUpdateSchedule(stateDir, settings);
        configureFeed(settings);
        // Leaving a pinned PR build is the same class of switch: its build is
        // armed and has to be superseded, not merely forgotten.
        const staleStaged = stagedBuildIsStale(settings);
        if (staleStaged) discardStagedBuild();
        autoUpdater.autoDownload = staleStaged;
        applyInstallOnQuitPolicy();
        broadcastUpdaterStatus({ state: "checking" });
        settleCheckStatus(await autoUpdater.checkForUpdates());
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
        // Manual downloads get no provider token, so make one: without it the
        // watchdog could report a stall but never release the request.
        const token = new CancellationToken();
        activeDownloadCancellation = token;
        applyUpdaterPolicy(escalationStateDir
          ? await readUpdateSettings(escalationStateDir)
          : FAIL_CLOSED_UPDATE_SETTINGS);
        transferObservation = { eligible: differentialEligible, attemptedDifferential: false, fallback: false, transferred: undefined };
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

/** Persist the narrow Developer Mode mirror and apply its fail-closed policy. */
export async function setMacDifferentialUpdates(
  stateDir: string,
  enabled: boolean,
): Promise<void> {
  if (typeof enabled !== "boolean") return;
  developerModeRequested = enabled;
  // Revoke eligibility synchronously, even while a previous operation is busy.
  // An already-started dependency download retains its captured options.
  if (!enabled) {
    developerModeHydrated = false;
    applyUpdaterPolicy(FAIL_CLOSED_UPDATE_SETTINGS);
  }
  await runSerializedUpdaterOperation("settings-write", async () => {
    const settings = await updateUpdateSettings(stateDir, (current) => ({
      ...current,
      macDifferentialUpdates: enabled,
    }));
    developerModeHydrated = enabled && developerModeRequested;
    applyUpdaterPolicy(settings);
  });
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
  autoUpdater.autoInstallOnAppQuit = blocker === undefined && !awaitingStagedReplacement;
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
