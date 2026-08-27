/**
 * GitHub-accurate rendered view of a `.md` file's current worktree content.
 *
 * Shown as the "Preview" tab alongside a markdown file's "Diff" in
 * `WorkspaceDiffView.tsx`. Styled with `github-markdown-css` (`.markdown-body`)
 * rather than AO's own design system — a deliberate, scoped exception for this
 * one view, since the whole point is to look like GitHub, not like AO chrome.
 *
 * Two choices carried over from `ChatMarkdown.tsx`, for the same reasons:
 *
 *   - `rehype-raw` is deliberately absent. A worker's markdown file is agent-
 *     produced content; markdown-only is the whole sanitization story and there
 *     is no schema to get wrong.
 *   - Fenced code reuses the app's existing lowlight/highlight.js engine
 *     (`lib/code-highlight.ts`) rather than Shiki: the renderer's CSP
 *     (`script-src 'self'`, no `wasm-unsafe-eval`) blocks Shiki's default WASM
 *     engine.
 *
 * No explicit light/dark plumbing is needed for `github-markdown-css`: its
 * combined stylesheet follows `prefers-color-scheme`, and `main.ts` already
 * drives Electron's `nativeTheme.themeSource` from AO's own theme preference,
 * so this renderer's `prefers-color-scheme` already tracks AO's theme, not the
 * raw OS setting. (In `npm run dev:web` there is no `nativeTheme`, so it follows
 * the OS directly.)
 */

import Markdown, { type Components } from "react-markdown";
import type { PluggableList } from "unified";
import remarkGfm from "remark-gfm";
import { rehypeGithubAlerts } from "rehype-github-alerts";
import rehypeSlug from "rehype-slug";
import rehypeAutolinkHeadings from "rehype-autolink-headings";
import { useCallback, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import { canonicalLanguage } from "../../lib/code-highlight";
import { fenceOf } from "../../lib/markdown-fence";
import { isWebLink } from "../../lib/external-link-policy";
import { HighlightedCode } from "../chat/HighlightedCode";
import "../chat/code-theme.css";
import "github-markdown-css/github-markdown.css";
import { MarkdownFileContext } from "./markdown-file-context";
import { MarkdownImage } from "./MarkdownImage";
import { MarkdownExternalLink } from "./MarkdownExternalLink";

const REMARK_PLUGINS = [remarkGfm];

// rehype-slug must run before rehype-autolink-headings, which reads the `id`
// the former just attached. The anchor mirrors GitHub's own markup exactly
// (`.anchor` > `.octicon.octicon-link`) so github-markdown-css's own hover-
// reveal CSS applies with no extra styling of our own.
const REHYPE_PLUGINS: PluggableList = [
	[rehypeGithubAlerts, {}],
	rehypeSlug,
	[
		rehypeAutolinkHeadings,
		{
			properties: { className: ["anchor"], ariaHidden: true, tabIndex: -1 },
			content: { type: "element", tagName: "span", properties: { className: ["octicon", "octicon-link"] }, children: [] },
		},
	],
];


export function MarkdownFileView({
	sessionId,
	filePath,
	content,
	truncated,
	version,
}: {
	sessionId: string;
	filePath: string;
	content: string;
	/** The daemon capped the file it sent; what renders below is a prefix of it. */
	truncated: boolean;
	/** The file detail's load timestamp, for cache-busting relative images. */
	version: number;
}) {
	const { t } = useTranslation();
	const bodyRef = useRef<HTMLDivElement>(null);
	const contextValue = useMemo(() => ({ sessionId, filePath, version }), [sessionId, filePath, version]);

	// Heading slugs are unique per rendered file, not per document, and the Files
	// tab expands many files at once — two open READMEs both containing
	// "## Overview" put two `id="overview"` in the same DOM. Resolving inside this
	// file's own container is what keeps a fragment link on the file it was
	// written in; `document.getElementById` would hand back whichever came first.
	// It also leaves a hand-written `[x](#overview)` working, which prefixing the
	// slugs would have broken.
	const onFragmentClick = useCallback((fragment: string) => {
		const id = fragment.slice(1);
		if (!id) return;
		bodyRef.current
			?.querySelector(`[id="${CSS.escape(id)}"]`)
			?.scrollIntoView({ behavior: "smooth", block: "start" });
	}, []);

	const components = useMemo<Components>(
		() => ({
			pre: ({ children }) => {
				const fence = fenceOf(children);
				if (!fence) return <>{children}</>;
				return (
					<pre className="markdown-code">
						<code>
							<HighlightedCode code={fence.code} language={canonicalLanguage(fence.language)} />
						</code>
					</pre>
				);
			},

			img: ({ src, alt }) => <MarkdownImage src={typeof src === "string" ? src : undefined} alt={alt} />,

			// A dispatcher, not a single link renderer: a same-page heading fragment
			// scrolls to that heading; a genuine http(s)/mailto link opens
			// externally; a relative link to another repo file (`./OTHER.md`) has no
			// in-app navigation target today, so it renders as inert text rather than
			// a link that would silently do nothing (or worse, navigate the app
			// shell).
			a: ({ href, children, node: _node, ...rest }) => {
				if (!href) return <>{children}</>;
				if (href.startsWith("#")) {
					return (
						<a
							href={href}
							{...rest}
							onClick={(event) => {
								event.preventDefault();
								onFragmentClick(href);
							}}
						>
							{children}
						</a>
					);
				}
				if (isWebLink(href) || href.startsWith("mailto:")) {
					return <MarkdownExternalLink href={href}>{children}</MarkdownExternalLink>;
				}
				return <span>{children}</span>;
			},
		}),
		[onFragmentClick],
	);
	return (
		<MarkdownFileContext.Provider value={contextValue}>
			<div>
				{truncated ? (
					<div className="shrink-0 border-b border-border bg-warning/10 px-3 py-1.5 text-xs text-warning">
						{t("files.contentTruncated")}
					</div>
				) : null}
				<div className="markdown-body p-4" ref={bodyRef}>
					<Markdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={REHYPE_PLUGINS} components={components}>
						{content}
					</Markdown>
				</div>
			</div>
		</MarkdownFileContext.Provider>
	);
}
