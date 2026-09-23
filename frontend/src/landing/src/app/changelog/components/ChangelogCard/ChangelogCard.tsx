import { ArrowUpRight } from "lucide-react";
import Link from "next/link";
import {
	type ChangelogEntry,
	formatChangelogDate,
} from "@/lib/changelog-utils";

interface ChangelogCardProps {
	entry: ChangelogEntry;
}

export function ChangelogCard({ entry }: ChangelogCardProps) {
	const formattedDate = formatChangelogDate(entry.date);

	return (
		<Link
			href={entry.url}
			className="group block border-b border-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-orange-500/70"
		>
			<article className="grid gap-4 py-7 transition-colors duration-200 group-hover:bg-muted/20 sm:grid-cols-[8rem_1fr_auto] sm:gap-8 sm:px-3">
				<div>
					<time
						dateTime={entry.date}
						className="text-sm font-mono text-muted-foreground tabular-nums"
					>
						{formattedDate}
					</time>
				</div>
				<div className="min-w-0">
					<h3 className="text-xl md:text-2xl font-medium tracking-[-0.02em] text-foreground group-hover:text-orange-300 transition-colors duration-200 text-balance">
						{entry.title}
					</h3>
					{entry.description && (
						<p className="mt-2 max-w-2xl text-sm md:text-base text-muted-foreground leading-relaxed text-pretty">
							{entry.description}
						</p>
					)}
				</div>
				<ArrowUpRight className="hidden size-5 text-muted-foreground transition-[color,transform] duration-200 group-hover:-translate-y-0.5 group-hover:translate-x-0.5 group-hover:text-foreground sm:block" />
			</article>
		</Link>
	);
}
