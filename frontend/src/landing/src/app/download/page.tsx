import { Cloud, Download } from "lucide-react";
import type { Metadata } from "next";
import Image from "next/image";
import Link from "next/link";
import { FaApple, FaLinux, FaWindows } from "react-icons/fa";
import { AndroidAppCTA } from "./AndroidAppCTA";
import { MobileAppCTA } from "./MobileAppCTA";
import { PlatformDownloadButton } from "./PlatformDownloadButton";
import { DesktopAppPreview, PhoneAppPreview } from "./StaticAppPreviews";
import { getPlatformDownloads, getReleases } from "../../lib/releases";

export const metadata: Metadata = {
  title: "Download",
  description:
    "Download Agent Orchestrator for macOS, Windows, or Linux, and get AO Mobile on iPhone and Android.",
};

export default async function DownloadPage() {
  const releases = await getReleases();
  const platformIcons = {
    macOS: FaApple,
    Windows: FaWindows,
    Linux: FaLinux,
  } as const;
  const platformDownloads = getPlatformDownloads(releases).map((platform) => ({
    ...platform,
    icon: platformIcons[platform.name],
  }));
  const stableHref = (platformName: string, label: string) =>
    platformDownloads
      .find((platform) => platform.name === platformName)
      ?.builds.find((item) => item.channel === "Stable" && item.label === label)
      ?.href;
  const heroDownloads = {
    macArm64: stableHref("macOS", "Mac (Apple silicon)"),
    macX64: stableHref("macOS", "Mac (Intel)"),
    windows: stableHref("Windows", "Windows (x64)"),
    linux: stableHref("Linux", "Linux AppImage (x64)"),
  };
  const channels = [
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
  ].filter((channel) =>
    platformDownloads.some((platform) =>
      platform.builds.some((item) => item.channel === channel.name),
    ),
  );

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
                  <PlatformDownloadButton downloads={heroDownloads} />
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
                <h2 className="text-xl font-semibold text-foreground">
                  Mobile
                </h2>
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
            {channels.map((channel) => (
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

                    if (builds.length === 0) return null;

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
        </div>
      </section>
    </main>
  );
}
