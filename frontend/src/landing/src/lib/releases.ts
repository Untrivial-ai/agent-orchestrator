import { COMPANY } from "@ao/shared/constants";
import semver from "semver";

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
  name: "macOS" | "Windows" | "Linux";
  builds: DownloadBuild[];
}

const RELEASE_PAGE_SIZE = 20;
const RELEASE_PAGE_COUNT = 5;

export async function getReleases(): Promise<GitHubRelease[]> {
  try {
    const responses = await Promise.all(
      Array.from({ length: RELEASE_PAGE_COUNT }, (_, index) =>
        fetch(
          `https://api.github.com/repos/${COMPANY.GITHUB_REPO}/releases?per_page=${RELEASE_PAGE_SIZE}&page=${index + 1}`,
          {
            headers: { Accept: "application/vnd.github+json" },
            next: { revalidate: 60 },
          },
        ),
      ),
    );

    if (responses.some((response) => !response.ok)) return [];
    return (
      await Promise.all(
        responses.map(
          async (response) => (await response.json()) as GitHubRelease[],
        ),
      )
    ).flat();
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
  return newestEligibleRelease(releases, "Stable");
}

export function findNightlyRelease(releases: GitHubRelease[]) {
  return newestEligibleRelease(releases, "Nightly");
}

const CHANNEL_MANIFESTS = {
  Stable: ["latest-mac.yml", "latest-linux.yml", "latest.yml"],
  Nightly: ["nightly-mac.yml", "nightly-linux.yml", "nightly.yml"],
} as const;

function newestEligibleRelease(
  releases: GitHubRelease[],
  channel: DownloadBuild["channel"],
): GitHubRelease | undefined {
  const manifestNames = CHANNEL_MANIFESTS[channel];

  return releases
    .filter((release) => {
      const version = semver.valid(release.tag_name);
      if (release.draft || version === null) return false;

      const prerelease = semver.prerelease(version);
      const channelMatches =
        channel === "Stable"
          ? !release.prerelease && prerelease === null
          : release.prerelease && prerelease?.[0] === "nightly";
      if (!channelMatches) return false;

      return manifestNames.every((manifestName) =>
        release.assets.some((asset) => asset.name === manifestName),
      );
    })
    .sort((left, right) => semver.rcompare(left.tag_name, right.tag_name))[0];
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
        build(
          "Mac (Apple silicon)",
          assetUrl(stable, "agent-orchestrator-darwin-arm64.dmg") ??
            assetUrl(stable, "agent-orchestrator-darwin-arm64.zip"),
          "Stable",
        ),
        build(
          "Mac (Intel)",
          assetUrl(stable, "agent-orchestrator-darwin-x64.dmg") ??
            assetUrl(stable, "agent-orchestrator-darwin-x64.zip"),
          "Stable",
        ),
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
        build(
          "Windows (x64)",
          assetUrl(stable, "agent-orchestrator-win32-x64.exe"),
          "Stable",
        ),
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
        build(
          "Linux AppImage (x64)",
          assetUrl(stable, "agent-orchestrator-linux-x64.AppImage"),
          "Stable",
        ),
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
