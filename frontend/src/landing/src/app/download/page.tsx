import {
  COMPANY,
  DOWNLOAD_URL_LINUX,
  DOWNLOAD_URL_MAC_ARM64,
  DOWNLOAD_URL_MAC_X64,
  DOWNLOAD_URL_WINDOWS,
} from "@ao/shared/constants";
import { Cloud, Download, Plus } from "lucide-react";
import type { Metadata } from "next";
import Image from "next/image";
import Link from "next/link";
import { FaApple, FaLinux, FaWindows } from "react-icons/fa";
import { AndroidAppCTA } from "./AndroidAppCTA";
import { MobileAppCTA } from "./MobileAppCTA";
import { PlatformDownloadButton } from "./PlatformDownloadButton";
import { DesktopAppPreview, PhoneAppPreview } from "./StaticAppPreviews";

export const metadata: Metadata = {
  title: "Download",
  description:
    "Download Agent Orchestrator for macOS, Windows, or Linux, and get AO Mobile on iPhone and Android.",
};

interface GitHubReleaseAsset {
  name: string;
  browser_download_url: string;
}

interface GitHubRelease {
  draft: boolean;
  prerelease: boolean;
  tag_name: string;
  assets: GitHubReleaseAsset[];
}

interface DownloadBuild {
  channel: "Stable" | "Nightly";
  href: string;
  label: string;
}

async function getReleases(): Promise<GitHubRelease[]> {
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

// Inline code style matches the changelog PR badge (bg-muted + font-mono) so the
// file and folder names read as code without pulling in the MDX renderer.
function Code({ children }: { children: string }) {
  return (
    <code className="break-all rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em] text-foreground">
      {children}
    </code>
  );
}

// macOS can refuse a first launch with "the developer cannot be verified" even
// though every build we ship is Developer ID signed, hardened, notarized, and
// stapled (verified on the published assets). right-click + Open is the
// documented way through it and is safe: the user still gets an explicit consent
// prompt and Gatekeeper still evaluates the signature. The cause is still under
// investigation, so this copy stays symptom-only and asks people to report the
// diagnostics we need rather than asserting a mechanism we have not confirmed.
// Collapsed by default behind a native disclosure so visitors who never hit
// the block see one line, not two cards: no client JS, static-export safe.
function MacUnblockNotice() {
  return (
    <section className="mt-16">
      <details className="group rounded-2xl border border-border p-5 sm:p-6">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-4 [&::-webkit-details-marker]:hidden">
          <span>
            <span className="block text-base font-semibold text-foreground">
              If macOS blocks the app on first launch
            </span>
            <span className="mt-1 block text-sm leading-6 text-muted-foreground">
              A one-time Finder step gets you through it — expand for the steps
              and what to send us if it persists.
            </span>
          </span>
          <Plus className="h-5 w-5 shrink-0 text-muted-foreground transition-transform duration-200 group-open:rotate-45" />
        </summary>

        <div className="mt-6">
          <p className="max-w-2xl text-sm leading-6 text-muted-foreground">
            macOS may say Agent Orchestrator{" "}
            <span className="text-foreground">
              cannot be opened because the developer cannot be verified
            </span>
            . Every macOS build is signed with our Apple Developer ID and
            notarized by Apple, so this is not a sign the download is unsafe.
            Opening it from Finder the first time gets you through it.
          </p>

          <div className="mt-6 grid grid-cols-1 gap-6 md:grid-cols-2">
        <article className="flex flex-col rounded-2xl bg-card p-5 sm:p-6">
          <h3 className="text-base font-semibold text-foreground">
            Open it from Finder
          </h3>
          <ol className="mt-4 list-decimal space-y-2 pl-5 text-sm leading-6 text-muted-foreground">
            <li>
              Unzip the download, then drag{" "}
              <Code>Agent Orchestrator.app</Code> into your{" "}
              <Code>Applications</Code> folder.
            </li>
            <li>
              Right-click (or Control-click) the app and choose{" "}
              <span className="text-foreground">Open</span>. Do not double-click
              it, as that only offers Move to Trash.
            </li>
            <li>
              Choose <span className="text-foreground">Open</span> again in the
              prompt. macOS remembers the choice, so this is a one-time step.
            </li>
          </ol>
        </article>

        <article className="flex flex-col rounded-2xl bg-card p-5 sm:p-6">
          <h3 className="text-base font-semibold text-foreground">
            If that does not work
          </h3>
          <p className="mt-4 text-sm leading-6 text-muted-foreground">
            Please report it so we can find the cause rather than work around it.
            Including this output helps most:
          </p>
          <div className="mt-3 overflow-x-auto rounded-xl bg-muted p-3">
            <pre className="whitespace-pre font-mono text-xs leading-6 text-foreground">
              <code>
                {`sw_vers\ncodesign -dvvv "/Applications/Agent Orchestrator.app"\nspctl -a -t exec -vvv --ignore-cache --no-cache "/Applications/Agent Orchestrator.app"\nlog show --last 10m --predicate 'subsystem == "com.apple.syspolicy"'`}
              </code>
            </pre>
          </div>
          <p className="mt-4 text-sm leading-6 text-muted-foreground">
            The last command is the useful one: it is macOS stating its own
            reason for refusing the app.{" "}
            <a
              href={COMPANY.REPORT_ISSUE_URL}
              className="text-foreground underline underline-offset-4 hover:opacity-75"
            >
              Open an issue
            </a>{" "}
            with it attached.
          </p>
        </article>
          </div>
        </div>
      </details>
    </section>
  );
}

export default async function DownloadPage() {
  const releases = await getReleases();
  const stable = releases.find(
    (release) => !release.draft && !release.prerelease,
  );
  const nightly = releases.find(
    (release) =>
      !release.draft &&
      release.prerelease &&
      release.tag_name.includes("-nightly."),
  );

  const platformDownloads = [
    {
      name: "macOS",
      icon: FaApple,
      // Prefer the .dmg. Mounting it gives the drag-to-Applications window, so
      // the app lands in /Applications instead of being unzipped into
      // ~/Downloads and launched from there, which is what leaves macOS running
      // it translocated or as a stale copy (#3617, #3527).
      //
      // Only the STABLE channel builds one. The container costs its own
      // notarization submission per architecture and nightly publishes far too
      // often to pay for it, so the nightly rows resolve to their zip
      // permanently, not just until some later rollout step.
      //
      // Every row falls back to the zip FROM THE SAME RELEASE before it falls
      // back to a DOWNLOAD_URL_* constant. That ordering is load-bearing: those
      // constants now point at the dmg too, so they are not a safe fallback on
      // their own. Reading the live release list is what lets this page serve a
      // real asset rather than a 404 in the window before the first stable dmg
      // ships, and if the GitHub call fails outright the constant is a guess of
      // last resort.
      builds: available([
        build(
          "Mac (Apple silicon)",
          assetUrl(stable, "agent-orchestrator-darwin-arm64.dmg") ??
            assetUrl(stable, "agent-orchestrator-darwin-arm64.zip") ??
            DOWNLOAD_URL_MAC_ARM64,
          "Stable",
        ),
        build(
          "Mac (Intel)",
          assetUrl(stable, "agent-orchestrator-darwin-x64.dmg") ??
            assetUrl(stable, "agent-orchestrator-darwin-x64.zip") ??
            DOWNLOAD_URL_MAC_X64,
          "Stable",
        ),
        build(
          "Mac (Apple silicon)",
          assetUrl(nightly, "agent-orchestrator-darwin-arm64.dmg") ??
            assetUrl(nightly, "agent-orchestrator-darwin-arm64.zip"),
          "Nightly",
        ),
        build(
          "Mac (Intel)",
          assetUrl(nightly, "agent-orchestrator-darwin-x64.dmg") ??
            assetUrl(nightly, "agent-orchestrator-darwin-x64.zip"),
          "Nightly",
        ),
      ]),
    },
    {
      name: "Windows",
      icon: FaWindows,
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
      icon: FaLinux,
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

  return (
    <main className="min-h-[100dvh] bg-background text-foreground">
      <section className="relative px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px] lg:py-24">
        <div className="mx-auto max-w-7xl">
          <div className="mb-12 select-none text-left">
            <h1 className="text-2xl font-semibold text-foreground sm:text-3xl lg:text-4xl">
              Use AO everywhere you work
            </h1>
            <p className="mt-3 text-base text-muted-foreground">
              One workspace to run, review, and ship coding agents across every
              surface.
            </p>
          </div>

          <div className="grid gap-6 md:grid-cols-2">
            <article className="order-2 flex h-full flex-col rounded-2xl bg-card p-4 sm:p-5 md:order-1">
              <div className="relative mb-5 h-80 overflow-hidden rounded-xl sm:h-[360px]">
                <Image
                  src="/optimized/feature3.webp"
                  alt=""
                  fill
                  preload
                  sizes="(max-width: 767px) 100vw, 50vw"
                  className="object-cover"
                />
                <div className="absolute inset-0 bg-background/10" />
                <DesktopAppPreview />
              </div>

              <div className="flex flex-1 flex-col">
                <h2 className="text-xl font-semibold text-foreground">
                  Desktop
                </h2>
                <p className="mt-2 text-base text-muted-foreground">
                  Full AO workspace for planning, running, and reviewing
                  multi-agent work.
                </p>
                <div className="mt-6">
                  <PlatformDownloadButton />
                </div>
              </div>
            </article>

            <article className="order-1 flex h-full flex-col rounded-2xl bg-card p-4 sm:p-5 md:order-2">
              <div className="relative mb-5 h-80 overflow-hidden rounded-xl sm:h-[360px]">
                <Image
                  src="/optimized/feature.webp"
                  alt=""
                  fill
                  preload
                  sizes="(max-width: 767px) 100vw, 50vw"
                  className="object-cover"
                />
                <div className="absolute inset-0 bg-background/10" />
                <PhoneAppPreview />
              </div>

              <div className="flex flex-1 flex-col">
                <h2 className="text-xl font-semibold text-foreground">Mobile</h2>
                <p className="mt-2 text-base text-muted-foreground">
                  Mobile companion to monitor agent runs and follow reviews from
                  anywhere. Free on iPhone and Android.
                </p>
                <div className="mt-6 flex flex-wrap items-center gap-3">
                  <MobileAppCTA />
                  <AndroidAppCTA />
                </div>
              </div>
            </article>
          </div>

          <section className="mt-8 overflow-hidden rounded-2xl border border-border bg-card">
            <div className="grid gap-6 p-5 sm:p-6 lg:grid-cols-[1fr_auto] lg:items-center">
              <div className="max-w-3xl">
                <p className="inline-flex items-center gap-2 text-xs font-medium uppercase tracking-[0.18em] text-muted-foreground">
                  <Cloud className="size-3.5" aria-hidden="true" />
                  AO Cloud
                </p>
                <h2 className="mt-3 text-2xl font-semibold text-foreground">
                  Join the AO Cloud waitlist
                </h2>
                <p className="mt-2 text-sm leading-6 text-muted-foreground">
                  Request early access for shared agent sessions, team handoffs,
                  and hosted runs.
                </p>
              </div>
              <Link
                href="/waitlist"
                className="inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-3xl bg-foreground px-3 py-2 text-sm font-semibold tracking-[-0.5px] text-background transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background sm:px-6 sm:py-3 sm:text-base"
              >
                Join waitlist
              </Link>
            </div>
          </section>

          <div className="mt-16 space-y-16">
            {[
              {
                name: "Stable" as const,
                label: "Recommended",
                description:
                  "Best for most people. Stable builds are the right choice for everyday work.",
              },
              {
                name: "Nightly" as const,
                label: "Early access",
                description:
                  "The newest changes, published frequently. Nightly builds may contain bugs or incomplete features.",
              },
            ].map((channel) => (
              <section key={channel.name}>
                <div className="mb-6 max-w-2xl">
                  <h2 className="text-2xl font-semibold text-foreground">
                    {channel.name} ({channel.label})
                  </h2>
                  <p className="mt-2 text-sm leading-6 text-muted-foreground">
                    {channel.description}
                  </p>
                </div>

                <div className="grid grid-cols-1 gap-6 text-foreground md:grid-cols-3">
                  {platformDownloads.map((platform) => {
                    const Icon = platform.icon;
                    const builds = platform.builds.filter(
                      (item) => item.channel === channel.name,
                    );

                    return (
                      <article
                        key={`${channel.name}-${platform.name}`}
                        className="flex flex-col rounded-2xl bg-card p-5 sm:p-6"
                      >
                        <div className="mb-5 flex items-center gap-3">
                          <Icon
                            className="size-4 shrink-0"
                            aria-hidden="true"
                          />
                          <h3 className="text-base font-semibold">
                            {platform.name}
                          </h3>
                        </div>

                        <div className="flex-1 divide-y divide-border">
                          {builds.map((downloadBuild) => (
                            <a
                              key={downloadBuild.label}
                              href={downloadBuild.href}
                              download
                              className="block w-full py-4 transition-opacity hover:opacity-75"
                            >
                              <span className="flex items-center gap-3">
                                <span className="whitespace-nowrap text-sm">
                                  {downloadBuild.label}
                                </span>
                                <Download
                                  className="ml-auto size-4 shrink-0 text-muted-foreground"
                                  aria-hidden="true"
                                />
                              </span>
                            </a>
                          ))}
                        </div>
                      </article>
                    );
                  })}
                </div>
              </section>
            ))}
          </div>

          <MacUnblockNotice />
        </div>
      </section>
    </main>
  );
}
