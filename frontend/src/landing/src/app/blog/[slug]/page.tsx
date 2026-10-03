import type { Metadata } from "next";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { MDXRemote } from "next-mdx-remote/rsc";
import { notFound } from "next/navigation";
import remarkGfm from "remark-gfm";
import { mdxComponents } from "@/app/blog/components/mdx-components";
import { AuthorAvatar } from "@/app/blog/components/AuthorAvatar";
import { GridCross } from "@/app/blog/components/GridCross";
import {
	formatBlogDate,
	getAllSlugs,
	getBlogPost,
} from "@/lib/blog";

export const dynamicParams = false;

export function generateStaticParams() {
	return getAllSlugs().map((slug) => ({ slug }));
}

export async function generateMetadata({
	params,
}: {
	params: Promise<{ slug: string }>;
}): Promise<Metadata> {
	const { slug } = await params;
	const post = getBlogPost(slug);
	if (!post) return { title: "Blog" };

	return {
		title: post.title,
		description: post.description,
		keywords: post.keywords,
		alternates: { canonical: post.url },
		openGraph: {
			title: `${post.title} | Agent Orchestrator`,
			description: post.description,
			url: post.url,
			images: [post.image ?? "/og-image.png"],
		},
		twitter: {
			card: "summary_large_image",
			title: `${post.title} | Agent Orchestrator`,
			description: post.description,
			images: [post.image ?? "/og-image.png"],
		},
	};
}

export default async function BlogPostPage({
	params,
}: {
	params: Promise<{ slug: string }>;
}) {
	const { slug } = await params;
	const post = getBlogPost(slug);
	if (!post) notFound();

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

					<Link
						href="/blog"
						className="inline-flex items-center gap-1.5 text-sm font-mono text-muted-foreground hover:text-foreground transition-colors tracking-[0.5px]"
					>
						<ArrowLeft className="size-4" />
						Blog
					</Link>

					<p className="mt-8 text-xs font-mono text-muted-foreground tracking-[0.5px]">
						{post.category}
						<span className="mx-2 text-muted-foreground/50">·</span>
						<time dateTime={post.date}>{formatBlogDate(post.date)}</time>
					</p>
					<h1 className="mt-3 text-balance text-3xl font-medium tracking-[-0.5px] text-foreground md:text-4xl">
						{post.title}
					</h1>
					{post.description ? (
						<p className="mt-4 max-w-2xl text-pretty text-muted-foreground">
							{post.description}
						</p>
					) : null}
					<div className="mt-6 flex items-center gap-2">
						<AuthorAvatar
							name={post.author.name}
							avatar={post.author.avatar}
							size="sm"
						/>
						<span className="text-sm text-muted-foreground">{post.author.name}</span>
					</div>

					<GridCross className="bottom-0 left-0" />
					<GridCross className="bottom-0 right-0" />
				</div>
			</header>

			<article className="relative prose prose-invert mx-auto max-w-3xl px-6 py-16 text-foreground prose-headings:text-balance prose-headings:font-medium prose-headings:text-foreground prose-p:text-pretty prose-p:text-muted-foreground prose-li:text-muted-foreground prose-strong:text-foreground prose-a:text-foreground prose-a:underline prose-a:underline-offset-4 prose-code:text-foreground prose-code:before:content-none prose-code:after:content-none prose-pre:border prose-pre:border-border prose-pre:bg-muted/35 prose-th:text-foreground prose-td:text-muted-foreground">
				<MDXRemote
					source={post.content}
					components={mdxComponents}
					options={{ mdxOptions: { remarkPlugins: [remarkGfm] } }}
				/>
			</article>
		</main>
	);
}
