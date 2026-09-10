import {
  COMPANY,
  DOWNLOAD_URL_LINUX,
  DOWNLOAD_URL_MAC_ARM64,
  DOWNLOAD_URL_MAC_X64,
  DOWNLOAD_URL_WINDOWS,
} from "@ao/shared/constants";

export interface GitHubReleaseAsset {
  name: string;
  browser_download_url: string;
}

export interface GitHubRelease {
  draft: boolean;
  prerelease: boolean;
  tag_name: string;
  assets: GitHubReleaseAsset[];
}

export interface DownloadBuild {
  channel: "Stable" | "Nightly";
  href: string;
  label: string;
}

export interface PlatformDownloads {
  name: string;
  builds: DownloadBuild[];
}

export async function getReleases(): Promise<GitHubRelease[]> {
  try {
    const response = await fetch(
      `https://api.github.com/repos/${COMPANY.GITHUB_REPO}/releases?per_page=30`,
      {
        headers: { Accept: "application/vnd.github+json" },
        next: { revalidate: 3600 },
      },
    );

    if (!response.ok) return [];
    return (await response.json()) as GitHubRelease[];
  } catch {
    return [];
  }
}

function assetUrl(
  release: GitHubRelease | undefined,
  exactName: string,
  fallbackPattern?: RegExp,
) {
  const exact = release?.assets.find((asset) => asset.name === exactName);
  if (exact) return exact.browser_download_url;
  return release?.assets.find((asset) => fallbackPattern?.test(asset.name))
    ?.browser_download_url;
}

function build(
  label: string,
  href: string | undefined,
  channel: DownloadBuild["channel"],
): DownloadBuild | null {
  return href ? { label, href, channel } : null;
}

function available(builds: Array<DownloadBuild | null>): DownloadBuild[] {
  return builds.filter((item): item is DownloadBuild => item !== null);
}

export function findStableRelease(releases: GitHubRelease[]) {
  return releases.find((release) => !release.draft && !release.prerelease);
}

export function findNightlyRelease(releases: GitHubRelease[]) {
  return releases.find(
    (release) =>
      !release.draft &&
      release.prerelease &&
      release.tag_name.includes("-nightly."),
  );
}

/** Per-platform Stable + Nightly download links, sourced from the latest GitHub releases. */
export function getPlatformDownloads(
  releases: GitHubRelease[],
): PlatformDownloads[] {
  const stable = findStableRelease(releases);
  const nightly = findNightlyRelease(releases);

  return [
    {
      name: "macOS",
      builds: available([
        build("Mac (Apple silicon)", DOWNLOAD_URL_MAC_ARM64, "Stable"),
        build("Mac (Intel)", DOWNLOAD_URL_MAC_X64, "Stable"),
        build(
          "Mac (Apple silicon)",
          assetUrl(nightly, "agent-orchestrator-darwin-arm64.zip"),
          "Nightly",
        ),
        build(
          "Mac (Intel)",
          assetUrl(nightly, "agent-orchestrator-darwin-x64.zip"),
          "Nightly",
        ),
      ]),
    },
    {
      name: "Windows",
      builds: available([
        build("Windows (x64)", DOWNLOAD_URL_WINDOWS, "Stable"),
        build(
          "Windows (x64)",
          assetUrl(nightly, "agent-orchestrator-win32-x64.exe"),
          "Nightly",
        ),
      ]),
    },
    {
      name: "Linux",
      builds: available([
        build("Linux AppImage (x64)", DOWNLOAD_URL_LINUX, "Stable"),
        build(
          "Linux .deb (x64)",
          assetUrl(
            stable,
            "agent-orchestrator-linux-x64.deb",
            /^agent-orchestrator[_-].*(?:amd64|x86_64)\.deb$/i,
          ),
          "Stable",
        ),
        build(
          "Linux RPM (x64)",
          assetUrl(
            stable,
            "agent-orchestrator-linux-x64.rpm",
            /^agent-orchestrator-.*x86_64\.rpm$/i,
          ),
          "Stable",
        ),
        build(
          "Linux AppImage (x64)",
          assetUrl(nightly, "agent-orchestrator-linux-x64.AppImage"),
          "Nightly",
        ),
        build(
          "Linux .deb (x64)",
          assetUrl(nightly, "agent-orchestrator-linux-x64.deb"),
          "Nightly",
        ),
      ]),
    },
  ];
}
