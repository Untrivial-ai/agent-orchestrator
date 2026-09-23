import { COMPANY } from "@ao/shared/constants";
import { ArrowRight, ExternalLink, Rss } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { getWeeklyUpdates } from "@/lib/changelog";
import { ChangelogEntry } from "./components/ChangelogEntry";

export const metadata: Metadata = {
  title: "Changelog",
  description:
    "The latest updates, improvements, and new features in Agent Orchestrator.",
  alternates: {
    canonical: "/changelog",
    types: {
      "application/rss+xml": "/changelog.xml",
    },
  },
  openGraph: {
    title: "Changelog | Agent Orchestrator",
    description:
      "The latest updates, improvements, and new features in Agent Orchestrator.",
    url: "/changelog",
    images: ["/og-image.png"],
  },
  twitter: {
    card: "summary_large_image",
    title: "Changelog | Agent Orchestrator",
    description:
      "The latest updates, improvements, and new features in Agent Orchestrator.",
    images: ["/og-image.png"],
  },
};

export default function ChangelogPage() {
  const entries = getWeeklyUpdates();

  return (
    <main className="relative min-h-screen">
      <header className="relative">
        <div className="relative mx-auto max-w-4xl px-6 pb-10 pt-16 md:pb-14 md:pt-24">
          <span className="text-sm font-mono text-muted-foreground tracking-[0.5px]">
            Changelog
          </span>
          <h1 className="text-5xl md:text-7xl font-medium tracking-[-0.05em] leading-[0.94] text-foreground mt-5 text-balance">
            What&apos;s new
          </h1>
          <p className="text-base md:text-lg text-muted-foreground mt-5 max-w-2xl leading-relaxed text-pretty">
            New workflows, meaningful improvements, and fixes you will notice.
            Updated weekly. For technical release notes and downloads, use the
            release archive.
          </p>
          <div className="flex flex-wrap items-center gap-x-5 gap-y-3 mt-7">
            <Link
              href="/changelog/releases"
              className="inline-flex items-center gap-1.5 text-sm text-foreground hover:text-orange-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
            >
              Release archive
              <ArrowRight className="size-3.5" />
            </Link>
            {/* react-doctor-disable-next-line react-doctor/nextjs-no-a-element -- RSS is a document endpoint and requires full navigation. */}
            <a
              href="/changelog.xml"
              className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
            >
              <Rss className="size-3.5" />
              RSS feed
            </a>
            <a
              href={`${COMPANY.GITHUB_URL}/releases`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
            >
              All downloads
              <ExternalLink className="size-3.5" />
            </a>
          </div>

        </div>
      </header>

      <div className="relative mx-auto max-w-4xl px-6 pb-24 pt-8 md:pt-12">
        {entries.length === 0 ? (
          <p className="text-muted-foreground">No updates yet.</p>
        ) : (
          <section aria-label="Weekly product updates" className="space-y-20 md:space-y-28">
            {entries.map((entry) => (
              <ChangelogEntry key={entry.url} entry={entry} />
            ))}
          </section>
        )}
      </div>
    </main>
  );
}
