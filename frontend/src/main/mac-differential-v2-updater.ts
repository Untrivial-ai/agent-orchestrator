import { MacUpdater, type Provider, type ResolvedUpdateFileInfo } from "electron-updater";
import type { DownloadExecutorTask, DownloadUpdateOptions } from "electron-updater/out/AppUpdater";
import { CancellationError, type UpdateInfo } from "builder-util-runtime";
import { net } from "electron";
import { readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import type { UpdateDownloadedEvent } from "electron-updater/out/types";
import type { MacV2TrustedKey } from "./mac-differential-v2-protocol";
import { authorizeMacV2Target, MacV2CleanupError, reconstructMacV2, verifyMacV2LocalFile } from "./mac-differential-v2-transfer";

const MAC_V2_CACHE_AUTHORIZATION = "ao-diff-v2-cache.json";
class MacV2CacheAuthorizationError extends Error {}

export interface MacV2UpdaterOptions {
  trustedKeys: Readonly<Record<string, MacV2TrustedKey>>;
  fetch?: typeof globalThis.fetch;
  timeoutMs?: number;
}

/** Uses the dependency's declared protected extension, never its stock range worker. */
export class MacDifferentialV2Updater extends MacUpdater {
  private activeV2Cancellation?: DownloadUpdateOptions["cancellationToken"];
  private cancelledBeforeNativeHandoff = false;
  private cancelledCandidatePath?: string;
  // Compiled implementation capability, never supplied by metadata or settings.
  get differentialCapability(): "mac-differential-v2" { return "mac-differential-v2"; }

  constructor(readonly v2: MacV2UpdaterOptions) {
    super();
    this.disableDifferentialDownload = true;
  }

  protected override dispatchUpdateDownloaded(event: UpdateDownloadedEvent): void {
    if (this.activeV2Cancellation?.cancelled) {
      this.cancelledBeforeNativeHandoff = true;
      this.cancelledCandidatePath = event.downloadedFile;
      this.autoInstallOnAppQuit = false;
      return;
    }
    super.dispatchUpdateDownloaded(event);
  }

  protected override async doDownloadUpdate(options: DownloadUpdateOptions): Promise<string[]> {
    const autoInstallOnAppQuit = this.autoInstallOnAppQuit;
    this.activeV2Cancellation = options.cancellationToken;
    this.cancelledBeforeNativeHandoff = false;
    this.cancelledCandidatePath = undefined;
    try {
      let result: string[];
      try {
        result = await super.doDownloadUpdate(options);
      } catch (error) {
        if (!(error instanceof MacV2CacheAuthorizationError)) throw error;
        if (options.cancellationToken.cancelled) throw new CancellationError();
        this._logger.warn("Cached v2 candidate authorization failed, fallback to one full download");
        result = await super.doDownloadUpdate(options);
      }
      if (this.cancelledBeforeNativeHandoff || options.cancellationToken.cancelled) {
        if (this.cancelledCandidatePath) await rm(this.cancelledCandidatePath, { force: true });
        await rm(this.v2MarkerPath(), { force: true });
        await this.downloadedUpdateHelper?.clear();
        throw new CancellationError();
      }
      return result;
    } finally {
      this.autoInstallOnAppQuit = autoInstallOnAppQuit;
      this.activeV2Cancellation = undefined;
      this.cancelledCandidatePath = undefined;
    }
  }

  protected override async executeDownload(task: DownloadExecutorTask): Promise<string[]> {
    const done = task.done;
    return super.executeDownload({
      ...task,
      done: done === undefined ? undefined : async event => {
        const markerPath = this.v2MarkerPath();
        let markerText: string;
        try {
          markerText = await readFile(markerPath, "utf8");
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code === "ENOENT") return done(event);
          throw error;
        }
        try {
          const marker = JSON.parse(markerText) as unknown;
          if (!marker || typeof marker !== "object" || Array.isArray(marker) ||
              Object.keys(marker).sort().join(",") !== "sha512,size,url,version") throw new Error("Invalid v2 cache marker");
          const value = marker as Record<string, unknown>;
          if (value.version !== event.version || value.url !== task.fileInfo.url.href ||
              value.size !== task.fileInfo.info.size || value.sha512 !== task.fileInfo.info.sha512) throw new Error("Mismatched v2 cache marker");
          const controller = new AbortController();
          const cancel = () => controller.abort(new CancellationError());
          task.downloadUpdateOptions.cancellationToken.on("cancel", cancel);
          try {
            if (task.downloadUpdateOptions.cancellationToken.cancelled) throw new CancellationError();
            const authorization = this.transferOptions(task.downloadUpdateOptions, task.fileInfo, event.downloadedFile, controller.signal);
            await authorizeMacV2Target(authorization);
            await verifyMacV2LocalFile(event.downloadedFile, authorization.target, authorization.signal);
          } finally {
            task.downloadUpdateOptions.cancellationToken.removeListener("cancel", cancel);
          }
        } catch (error) {
          await rm(markerPath, { force: true });
          await rm(event.downloadedFile, { force: true });
          await this.downloadedUpdateHelper?.clear();
          throw new MacV2CacheAuthorizationError("Cached v2 candidate denied", { cause: error });
        }
        return done(event);
      },
    });
  }

  private v2MarkerPath(): string {
    if (!this.downloadedUpdateHelper) throw new Error("Missing updater cache");
    return path.join(this.downloadedUpdateHelper.cacheDirForPendingUpdate, MAC_V2_CACHE_AUTHORIZATION);
  }

  private transferOptions(options: DownloadUpdateOptions, fileInfo: ResolvedUpdateFileInfo, destination: string, signal: AbortSignal) {
    return {
      capability: this.differentialCapability, arch: process.arch,
      enabled: true, channel: this.channel ?? "", installedVersion: this.currentVersion.version,
      candidateVersion: options.updateInfoAndProvider.info.version,
      target: { url: fileInfo.url.href, size: fileInfo.info.size ?? 0, sha512: fileInfo.info.sha512 },
      baselinePath: path.join(this.downloadedUpdateHelper?.cacheDir ?? "", "update.zip"), destination,
      trustedKeys: this.v2.trustedKeys,
      fetch: this.v2.fetch ?? ((input: URL | RequestInfo, init?: RequestInit) => net.fetch(input as string, init)),
      signal,
      onProgress: (progress: { total: number; transferred: number; percent: number; bytesPerSecond: number; delta: number }) => this.emit("download-progress", progress),
    };
  }

  protected override async differentialDownloadInstaller(
    fileInfo: ResolvedUpdateFileInfo,
    options: DownloadUpdateOptions,
    destination: string,
    _provider: Provider<UpdateInfo>,
    oldInstallerFileName: string,
  ): Promise<boolean> {
    const controller = new AbortController();
    const cancel = () => controller.abort(new CancellationError());
    options.cancellationToken.on("cancel", cancel);
    const timeout = setTimeout(() => controller.abort(new Error("v2 transfer timeout")), this.v2.timeoutMs ?? 30_000);
    try {
      if (options.cancellationToken.cancelled) throw new CancellationError();
      if (options.disableDifferentialDownload !== false || !this.downloadedUpdateHelper || oldInstallerFileName !== "update.zip") return true;
      this._logger.info("Download block maps using isolated v2 resolver");
      const transfer = this.transferOptions(options, fileInfo, destination, controller.signal);
      transfer.baselinePath = path.join(this.downloadedUpdateHelper.cacheDir, oldInstallerFileName);
      await reconstructMacV2(transfer);
      // Cancellation can arrive during the last digest read or handle close.
      controller.signal.throwIfAborted();
      const marker = { version: options.updateInfoAndProvider.info.version, url: fileInfo.url.href,
        size: fileInfo.info.size ?? 0, sha512: fileInfo.info.sha512 };
      const markerPath = this.v2MarkerPath();
      const temporaryMarker = `${markerPath}.${process.pid}.${Date.now()}.tmp`;
      try {
        await writeFile(temporaryMarker, `${JSON.stringify(marker)}\n`, { flag: "wx", mode: 0o600 });
        await rename(temporaryMarker, markerPath);
      } finally {
        await rm(temporaryMarker, { force: true });
      }
      return false;
    } catch (error) {
      if (error instanceof MacV2CleanupError) throw error;
      if (options.cancellationToken.cancelled) throw new CancellationError();
      await rm(this.v2MarkerPath(), { force: true });
      this._logger.warn("V2 differential transfer failed, fallback to full download");
      // MacUpdater owns the one verified full download after all v2 work settles.
      return true;
    } finally {
      clearTimeout(timeout);
      options.cancellationToken.removeListener("cancel", cancel);
    }
  }
}
