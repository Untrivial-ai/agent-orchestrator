import type { Metadata } from "next";
import { ContentIndexHeader } from "@/app/components/ContentIndexHeader";
import { getBlogPosts } from "@/lib/blog";
import { BlogCard } from "./components/BlogCard";

export const metadata: Metadata = {
  title: "Blog",
  description:
    "News, updates, and insights from the Agent Orchestrator team about coding agent orchestration and developer productivity.",
  alternates: {
    canonical: "/blog",
    types: {
      "application/rss+xml": "/feed.xml",
    },
  },
  openGraph: {
    title: "Blog | Agent Orchestrator",
    description:
      "News, updates, and insights from the Agent Orchestrator team about coding agent orchestration and developer productivity.",
    url: "/blog",
    images: ["/og-image.png"],
  },
  twitter: {
    card: "summary_large_image",
    title: "Blog | Agent Orchestrator",
    description:
      "News, updates, and insights from the Agent Orchestrator team about coding agent orchestration and developer productivity.",
    images: ["/og-image.png"],
  },
};

export default async function BlogPage() {
  const posts = getBlogPosts();

  return (
    <main className="relative min-h-screen">
      {/* Vertical guide lines */}
      <div
        className="absolute inset-0 pointer-events-none"
        style={{
          backgroundImage: `
            linear-gradient(to right, transparent 0%, transparent calc(50% - 384px), rgba(255,255,255,0.06) calc(50% - 384px), rgba(255,255,255,0.06) calc(50% - 383px), transparent calc(50% - 383px), transparent calc(50% + 383px), rgba(255,255,255,0.06) calc(50% + 383px), rgba(255,255,255,0.06) calc(50% + 384px), transparent calc(50% + 384px))
          `,
        }}
      />

      <ContentIndexHeader
        eyebrow="Blog"
        title="News & Updates"
        description="Insights from the Agent Orchestrator team about coding agent orchestration and developer productivity."
      />

      {/* Posts section */}
      <div className="relative max-w-3xl mx-auto px-6 py-12">
        {posts.length === 0 ? (
          <p className="text-muted-foreground">No posts yet.</p>
        ) : (
          <div className="flex flex-col gap-4">
            {posts.map((post) => (
              <BlogCard key={post.url} post={post} />
            ))}
          </div>
        )}
      </div>
    </main>
  );
}
