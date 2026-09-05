import { MacUpdater, type Provider, type ResolvedUpdateFileInfo } from "electron-updater";
import type { DownloadUpdateOptions } from "electron-updater/out/AppUpdater";
import { CancellationError, type UpdateInfo } from "builder-util-runtime";
import { net } from "electron";
import path from "node:path";
import { MacV2CleanupError, reconstructMacV2 } from "./mac-differential-v2-transfer";

export interface MacV2UpdaterOptions {
  trustedKeys: Readonly<Record<string, string>>;
  fetch?: typeof globalThis.fetch;
  timeoutMs?: number;
}

/** Uses the dependency's declared protected extension, never its stock range worker. */
export class MacDifferentialV2Updater extends MacUpdater {
  // Compiled implementation capability, never supplied by metadata or settings.
  get differentialCapability(): "mac-differential-v2" { return "mac-differential-v2"; }

  constructor(readonly v2: MacV2UpdaterOptions) {
    super();
    this.disableDifferentialDownload = true;
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
      await reconstructMacV2({
        capability: this.differentialCapability, arch: process.arch,
        enabled: true, channel: this.channel ?? "", installedVersion: this.currentVersion.version,
        candidateVersion: options.updateInfoAndProvider.info.version,
        target: { url: fileInfo.url.href, size: fileInfo.info.size ?? 0, sha512: fileInfo.info.sha512 },
        baselinePath: path.join(this.downloadedUpdateHelper.cacheDir, oldInstallerFileName), destination,
        trustedKeys: this.v2.trustedKeys,
        fetch: this.v2.fetch ?? ((input, init) => net.fetch(input as string, init)),
        signal: controller.signal,
        onProgress: progress => this.emit("download-progress", progress),
      });
      // Cancellation can arrive during the last digest read or handle close.
      controller.signal.throwIfAborted();
      return false;
    } catch (error) {
      if (error instanceof MacV2CleanupError) throw error;
      if (options.cancellationToken.cancelled) throw new CancellationError();
      this._logger.warn("V2 differential transfer failed, fallback to full download");
      // MacUpdater owns the one verified full download after all v2 work settles.
      return true;
    } finally {
      clearTimeout(timeout);
      options.cancellationToken.removeListener("cancel", cancel);
    }
  }
}
