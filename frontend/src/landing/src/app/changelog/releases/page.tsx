import { COMPANY } from "@ao/shared/constants";
import { ArrowLeft, ExternalLink } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { getStableReleaseEntries } from "@/lib/changelog-releases";
import { formatChangelogDate } from "@/lib/changelog-utils";

export const metadata: Metadata = {
	title: "Release archive",
	description:
		"Stable Agent Orchestrator versions, technical release notes, and downloads.",
	alternates: { canonical: "/changelog/releases" },
};

export default async function ReleaseArchivePage() {
	const releases = await getStableReleaseEntries();

	return (
		<main className="relative min-h-screen">
			<header className="relative">
				<div className="relative mx-auto max-w-4xl px-6 pb-12 pt-16 md:pb-16 md:pt-24">
					<Link
						href="/changelog"
						className="inline-flex items-center gap-1.5 text-sm font-mono text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
					>
						<ArrowLeft className="size-4" />
						Weekly updates
					</Link>
					<h1 className="mt-6 text-4xl font-medium leading-none tracking-[-0.04em] text-foreground md:text-6xl">
						Release archive
					</h1>
					<p className="mt-5 max-w-2xl text-base leading-relaxed text-muted-foreground md:text-lg">
						Stable versions, technical notes, and downloads. The weekly changelog
						keeps the product story concise; this archive keeps the complete release
						history close by.
					</p>
				</div>
			</header>

			<section className="relative mx-auto max-w-4xl px-6 pb-24 pt-8 md:pt-12">
				{releases.length === 0 ? (
					<div className="py-6">
						<p className="text-foreground">The release archive is temporarily unavailable.</p>
						<a
							href={`${COMPANY.GITHUB_URL}/releases`}
							className="mt-3 inline-flex items-center gap-1.5 text-sm text-orange-400 hover:text-orange-300"
						>
							Open GitHub Releases
							<ExternalLink className="size-3.5" />
						</a>
					</div>
				) : (
					<div className="space-y-2">
						{releases.map((release) => (
							<article
								key={release.slug}
								className="grid gap-3 py-5 sm:grid-cols-[1fr_auto_auto] sm:items-center sm:gap-8"
							>
								<div>
									<h2 className="text-lg font-medium text-foreground">
										{release.title}
									</h2>
									<time
										dateTime={release.date}
										className="mt-1 block text-sm font-mono text-muted-foreground tabular-nums sm:hidden"
									>
										{formatChangelogDate(release.date)}
									</time>
								</div>
								<time
									dateTime={release.date}
									className="hidden text-sm font-mono text-muted-foreground tabular-nums sm:block"
								>
									{formatChangelogDate(release.date)}
								</time>
								<a
									href={release.releaseUrl}
									target="_blank"
									rel="noopener noreferrer"
									className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
								>
									Notes and downloads
									<ExternalLink className="size-3.5" />
								</a>
							</article>
						))}
					</div>
				)}
			</section>
		</main>
	);
}
