/**
 * GitHub-accurate rendered view of a `.md` file's current worktree content.
 *
 * Shown as the "Rendered" tab alongside a markdown file's "Source diff" in
 * `SessionFilesView.tsx`. Styled with `github-markdown-css` (`.markdown-body`)
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
 * raw OS setting.
 */

import Markdown, { type Components } from "react-markdown";
import type { PluggableList } from "unified";
import remarkGfm from "remark-gfm";
import { rehypeGithubAlerts } from "rehype-github-alerts";
import rehypeSlug from "rehype-slug";
import rehypeAutolinkHeadings from "rehype-autolink-headings";
import { isValidElement, type ReactNode } from "react";
import { canonicalLanguage } from "../../lib/code-highlight";
import { isWebLink } from "../../lib/external-link-policy";
import { aoBridge } from "../../lib/bridge";
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

/** The text inside a node, for language sniffing on a fenced block. */
function textOf(children: ReactNode): string {
	if (typeof children === "string") return children;
	if (typeof children === "number") return String(children);
	if (Array.isArray(children)) return children.map(textOf).join("");
	if (children && typeof children === "object" && "props" in children) {
		return textOf((children as { props?: { children?: ReactNode } }).props?.children);
	}
	return "";
}

const LANGUAGE_CLASS = /language-([\w+#-]+)/;

/** The fence inside a `pre`, or undefined if this is not a fenced block. */
function fenceOf(children: ReactNode): { code: string; language?: string } | undefined {
	if (!isValidElement<{ className?: string; children?: ReactNode }>(children)) return undefined;
	return {
		code: textOf(children.props.children).replace(/\n$/, ""),
		language: LANGUAGE_CLASS.exec(children.props.className ?? "")?.[1],
	};
}

const COMPONENTS: Components = {
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
	// scrolls and copies its own link; a genuine http(s)/mailto link opens
	// externally; a relative link to another repo file (`./OTHER.md`) has no
	// in-app navigation target today, so it renders as inert text rather than a
	// link that would silently do nothing (or worse, navigate the app shell).
	a: ({ href, children, ...rest }) => {
		if (!href) return <>{children}</>;
		if (href.startsWith("#")) {
			return (
				<a
					href={href}
					{...rest}
					onClick={(event) => {
						event.preventDefault();
						document.getElementById(href.slice(1))?.scrollIntoView({ behavior: "smooth", block: "start" });
						void aoBridge.clipboard.writeText(href);
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
};

export function MarkdownFileView({
	sessionId,
	filePath,
	content,
}: {
	sessionId: string;
	filePath: string;
	content: string;
}) {
	return (
		<MarkdownFileContext.Provider value={{ sessionId, filePath }}>
			<div className="markdown-body p-4">
				<Markdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={REHYPE_PLUGINS} components={COMPONENTS}>
					{content}
				</Markdown>
			</div>
		</MarkdownFileContext.Provider>
	);
}
