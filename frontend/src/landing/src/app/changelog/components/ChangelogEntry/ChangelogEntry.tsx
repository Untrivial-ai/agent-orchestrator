import Link from "next/link";
import { MDXRemote } from "next-mdx-remote/rsc";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
	type ChangelogEntry as ChangelogEntryType,
	formatChangelogDate,
} from "@/lib/changelog-utils";
import { changelogMdxComponents } from "./changelog-mdx-components";

interface ChangelogEntryProps {
	entry: ChangelogEntryType;
}

export async function ChangelogEntry({ entry }: ChangelogEntryProps) {
	const formattedDate = formatChangelogDate(entry.date);

	return (
		<article id={`changelog-${entry.slug}`} className="relative select-text">
			{/* Sticky date label positioned beside the article on wide screens. */}
			<div
				className="hidden lg:flex absolute top-0 bottom-0 items-start"
				style={{ right: "calc(100% + 24px)" }}
			>
				<div className="sticky top-24 pt-1">
					<span className="text-sm font-mono text-muted-foreground whitespace-nowrap">
						{formattedDate}
					</span>
				</div>
			</div>

			{/* Mobile date */}
			<time
				dateTime={entry.date}
				className="lg:hidden block text-sm font-mono text-muted-foreground mb-4"
			>
				{formattedDate}
			</time>

			{/* Title */}
			<Link href={entry.url} className="group">
				<h2 className="text-2xl md:text-3xl font-medium text-foreground mb-4 group-hover:text-foreground/80 transition-colors">
					{entry.title}
				</h2>
			</Link>

			{/* Featured image */}
			{entry.image && (
				<div className="relative mb-6 overflow-hidden">
					{/* biome-ignore lint/performance/noImgElement: Need native img for natural dimensions */}
					<img src={entry.image} alt={entry.title} className="w-full h-auto" />
				</div>
			)}

			{/* Description */}
			{entry.description && (
				<p className="text-lg text-muted-foreground mb-6">
					{entry.description}
				</p>
			)}

			{/* Body. Curated entries compile as MDX (custom components); GitHub
			    release bodies are plain Markdown, rendered without the MDX compiler
			    so arbitrary text can never be parsed as MDX. */}
			<div className="prose prose-invert max-w-none prose-headings:font-medium prose-headings:tracking-[-0.5px] prose-h2:text-xl prose-h2:mt-8 prose-h2:mb-4 prose-h3:text-lg prose-h3:mt-6 prose-h3:mb-3 prose-p:text-muted-foreground prose-p:leading-relaxed prose-li:text-muted-foreground prose-strong:font-medium prose-strong:text-foreground prose-a:text-foreground prose-a:underline prose-a:underline-offset-4 hover:prose-a:text-muted-foreground">
				{entry.source === "release" ? (
					<ReactMarkdown components={{ hr: () => null }} remarkPlugins={[remarkGfm]}>
						{entry.content}
					</ReactMarkdown>
				) : (
					<MDXRemote
						source={entry.content}
						components={changelogMdxComponents}
						options={{ mdxOptions: { remarkPlugins: [remarkGfm] } }}
					/>
				)}
			</div>
		</article>
	);
}
