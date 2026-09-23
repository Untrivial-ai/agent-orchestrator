import { ArrowLeft, ArrowRight } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import {
  getAllChangelogSlugs,
  getChangelogEntry,
  getWeeklyUpdates,
} from "@/lib/changelog";
import { ChangelogEntry } from "../components/ChangelogEntry";

// Static export needs every entry enumerated at build time.
export async function generateStaticParams() {
  return (await getAllChangelogSlugs()).map((slug) => ({ slug }));
}

export async function generateMetadata({
  params,
}: {
  params: Promise<{ slug: string }>;
}): Promise<Metadata> {
  const { slug } = await params;
  const entry = await getChangelogEntry(slug);
  if (!entry) {
    return { title: "Changelog" };
  }
  return {
    title: entry.title,
    description: entry.description,
    alternates: { canonical: entry.url },
    openGraph: {
      title: `${entry.title} | Agent Orchestrator`,
      description: entry.description,
      url: entry.url,
      images: [entry.image || "/og-image.png"],
    },
    twitter: {
      card: "summary_large_image",
      title: `${entry.title} | Agent Orchestrator`,
      description: entry.description,
      images: [entry.image || "/og-image.png"],
    },
  };
}

export default async function ChangelogEntryPage({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  const { slug } = await params;
  const entry = await getChangelogEntry(slug);
  if (!entry) {
    notFound();
  }

  const weeklyUpdates = getWeeklyUpdates();
  const entryIndex = weeklyUpdates.findIndex((candidate) => candidate.slug === slug);
  const newerEntry = entryIndex > 0 ? weeklyUpdates[entryIndex - 1] : undefined;
  const olderEntry =
    entryIndex >= 0 ? weeklyUpdates[entryIndex + 1] : undefined;
  const backHref = entry.source === "release" ? "/changelog/releases" : "/changelog";
  const backLabel = entry.source === "release" ? "Release archive" : "Changelog";

	return (
		<main className="relative min-h-screen select-text">
			<header className="relative">
				<div className="relative mx-auto max-w-4xl px-6 pb-8 pt-16 md:pt-20">
          <Link
            href={backHref}
            className="inline-flex items-center gap-1.5 text-sm font-mono text-muted-foreground hover:text-foreground transition-colors tracking-[0.5px]"
          >
            <ArrowLeft className="size-4" />
            {backLabel}
          </Link>

				</div>
			</header>

			<div className="relative mx-auto max-w-4xl px-6 pb-24 pt-8">
        <ChangelogEntry entry={entry} />
        {entry.source !== "release" && (olderEntry || newerEntry) && (
          <nav
            aria-label="Changelog navigation"
						className="mt-16 grid gap-8 sm:grid-cols-2"
          >
            {olderEntry ? (
              <Link
                href={olderEntry.url}
								className="group py-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70"
              >
                <span className="text-xs font-mono uppercase tracking-[0.14em] text-muted-foreground">
                  Older update
                </span>
                <span className="mt-2 flex items-center gap-2 text-sm font-medium text-foreground">
                  <ArrowLeft className="size-4 transition-transform group-hover:-translate-x-1" />
                  {olderEntry.title}
                </span>
              </Link>
            ) : (
							<span className="hidden sm:block" />
            )}
            {newerEntry && (
              <Link
                href={newerEntry.url}
								className="group py-2 text-right focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70"
              >
                <span className="text-xs font-mono uppercase tracking-[0.14em] text-muted-foreground">
                  Newer update
                </span>
                <span className="mt-2 flex items-center justify-end gap-2 text-sm font-medium text-foreground">
                  {newerEntry.title}
                  <ArrowRight className="size-4 transition-transform group-hover:translate-x-1" />
                </span>
              </Link>
            )}
          </nav>
        )}
      </div>
    </main>
  );
}
