import { afterEach, describe, expect, it, vi } from "vitest";
import {
  findNightlyRelease,
  findStableRelease,
  getReleases,
  getPlatformDownloads,
  type GitHubRelease,
} from "./releases";

const installers = [
  "agent-orchestrator-darwin-arm64.dmg",
  "agent-orchestrator-darwin-x64.dmg",
  "agent-orchestrator-linux-x64.AppImage",
  "agent-orchestrator-linux-x64.deb",
  "agent-orchestrator-linux-x64.rpm",
  "agent-orchestrator-win32-x64.exe",
];

const stableManifests = ["latest-mac.yml", "latest-linux.yml", "latest.yml"];
const nightlyManifests = [
  "nightly-mac.yml",
  "nightly-linux.yml",
  "nightly.yml",
];

function release(
  tag_name: string,
  options: {
    assets?: string[];
    draft?: boolean;
    prerelease?: boolean;
  } = {},
): GitHubRelease {
  return {
    tag_name,
    draft: options.draft ?? false,
    prerelease: options.prerelease ?? tag_name.includes("-"),
    assets: (options.assets ?? []).map((name) => ({
      name,
      browser_download_url: `https://example.test/${tag_name}/${name}`,
    })),
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("GitHub release inventory", () => {
  it("loads the same 100-release window as the in-app updater in cacheable pages", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      const page = Number(new URL(url).searchParams.get("page"));
      return {
        ok: true,
        json: async () => [release(`v0.12.${page}`)],
      };
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(getReleases()).resolves.toHaveLength(5);
    expect(fetchMock).toHaveBeenCalledTimes(5);
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(
      [1, 2, 3, 4, 5].map(
        (page) =>
          `https://api.github.com/repos/Untrivial-ai/agent-orchestrator/releases?per_page=20&page=${page}`,
      ),
    );
  });

  it("fails closed if any inventory page cannot be loaded", async () => {
    const fetchMock = vi.fn(async (url: string) => ({
      ok: !url.endsWith("page=3"),
      json: async () => [release("v0.12.6")],
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getReleases()).resolves.toEqual([]);
  });
});

describe("landing release eligibility", () => {
  it("falls back to the newest stable release with a complete manifest set", () => {
    const releases = [
      release("v0.12.7", { assets: installers }),
      release("v0.12.5", { assets: [...installers, ...stableManifests] }),
      release("v0.12.6", { assets: [...installers, ...stableManifests] }),
    ];

    expect(findStableRelease(releases)?.tag_name).toBe("v0.12.6");
  });

  it("rejects drafts, prereleases, invalid tags, and partial stable uploads", () => {
    const releases = [
      release("v0.12.9", {
        assets: [...installers, ...stableManifests],
        draft: true,
      }),
      release("v0.12.8", {
        assets: [...installers, "latest-mac.yml", "latest.yml"],
      }),
      release("v0.12.7-beta.1", {
        assets: [...installers, ...stableManifests],
      }),
      release("not-semver", {
        assets: [...installers, ...stableManifests],
        prerelease: false,
      }),
    ];

    expect(findStableRelease(releases)).toBeUndefined();
  });

  it("uses semver ordering for completed nightly releases", () => {
    const releases = [
      release("v0.12.8-nightly.202608250759", {
        assets: [...installers, ...nightlyManifests],
      }),
      release("v0.12.8-nightly.202608261410", {
        assets: [...installers, ...nightlyManifests],
      }),
      release("v0.12.8-beta.1", {
        assets: [...installers, ...nightlyManifests],
      }),
    ];

    expect(findNightlyRelease(releases)?.tag_name).toBe(
      "v0.12.8-nightly.202608261410",
    );
  });

  it("fails closed when the nightly kill switch removes manifests", () => {
    const releases = [
      release("v0.12.8-nightly.202608261410", { assets: installers }),
      release("v0.12.8-nightly.202608250759", {
        assets: [...installers, ...nightlyManifests],
        draft: true,
      }),
    ];

    expect(findNightlyRelease(releases)).toBeUndefined();
    expect(
      getPlatformDownloads(releases).flatMap((platform) =>
        platform.builds.filter((build) => build.channel === "Nightly"),
      ),
    ).toEqual([]);
  });

  it("builds every stable URL from the eligible release without /latest fallbacks", () => {
    const releases = [
      release("v0.12.7", { assets: installers }),
      release("v0.12.6", { assets: [...installers, ...stableManifests] }),
    ];

    const downloads = getPlatformDownloads(releases).flatMap(
      (platform) => platform.builds,
    );

    expect(downloads).toHaveLength(6);
    expect(downloads.every((download) => download.channel === "Stable")).toBe(
      true,
    );
    expect(
      downloads.every(
        (download) =>
          download.href.includes("/v0.12.6/") &&
          !download.href.includes("/releases/latest/"),
      ),
    ).toBe(true);
  });
});
