import type { Metadata } from "next";
import Link from "next/link";
import { GridCross } from "@/app/blog/components/GridCross";
import {
	formatCompareDate,
	getComparisonPages,
} from "@/lib/compare";
import { getComparisonPageTypeLabel } from "@/lib/compare-utils";

export const metadata: Metadata = {
	title: "Compare",
	description:
		"Honest, current comparisons of Agent Orchestrator with Superset, Conductor, Emdash, Paseo, cmux, AgentsMesh, and notes on what changed since Composio.",
	alternates: {
		canonical: "/compare",
	},
	openGraph: {
		title: "Compare | Agent Orchestrator",
		description:
			"Honest, current comparisons of Agent Orchestrator with peer tools for parallel coding agents.",
		url: "/compare",
		images: ["/og-image.png"],
	},
};

export default function CompareIndexPage() {
	const pages = getComparisonPages();

	return (
		<main className="relative min-h-screen">
			<div
				className="absolute inset-0 pointer-events-none"
				style={{
					backgroundImage: `
            linear-gradient(to right, transparent 0%, transparent calc(50% - 384px), rgba(255,255,255,0.06) calc(50% - 384px), rgba(255,255,255,0.06) calc(50% - 383px), transparent calc(50% - 383px), transparent calc(50% + 383px), rgba(255,255,255,0.06) calc(50% + 383px), rgba(255,255,255,0.06) calc(50% + 384px), transparent calc(50% + 384px))
          `,
				}}
			/>

			<header className="relative border-b border-border">
				<div className="max-w-3xl mx-auto px-6 pt-16 pb-10 md:pt-20 md:pb-12 relative">
					<GridCross className="top-0 left-0" />
					<GridCross className="top-0 right-0" />

					<span className="text-sm font-mono text-muted-foreground tracking-[0.5px]">
						Compare
					</span>
					<h1 className="text-3xl md:text-4xl font-medium tracking-[-0.5px] text-foreground mt-4">
						AO versus the field
					</h1>
					<p className="text-muted-foreground mt-3 max-w-lg">
						Current, verifiable comparisons. Each page leads with what AO
						uniquely has, concedes where the other product is stronger, and
						avoids overclaiming work still in progress.
					</p>

					<GridCross className="bottom-0 left-0" />
					<GridCross className="bottom-0 right-0" />
				</div>
			</header>

			<div className="relative max-w-3xl mx-auto px-6 py-12">
				{pages.length === 0 ? (
					<p className="text-muted-foreground">No comparisons yet.</p>
				) : (
					<ul className="flex flex-col gap-4">
						{pages.map((page) => (
							<li key={page.slug}>
								<Link href={page.url} className="block group">
									<article className="border border-border bg-background p-6 transition-all hover:bg-muted/50 hover:border-foreground/20">
										<div className="flex items-center gap-3 mb-3">
											<span className="text-xs font-mono text-muted-foreground tracking-[0.5px]">
												{getComparisonPageTypeLabel(page.type)}
											</span>
											<span className="text-muted-foreground/50">·</span>
											<time
												dateTime={page.lastUpdated ?? page.date}
												className="text-xs text-muted-foreground"
											>
												Updated{" "}
												{formatCompareDate(page.lastUpdated ?? page.date)}
											</time>
										</div>
										<h2 className="text-lg font-medium text-foreground mb-2 group-hover:text-foreground/90">
											{page.title}
										</h2>
										{page.description ? (
											<p className="text-muted-foreground text-sm leading-relaxed line-clamp-3">
												{page.description}
											</p>
										) : null}
									</article>
								</Link>
							</li>
						))}
					</ul>
				)}
			</div>
		</main>
	);
}
