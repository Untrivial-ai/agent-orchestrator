/**
 * Markdown for agent prose.
 *
 * Agents write markdown — headings, lists, tables, fenced code — and rendering it
 * as preformatted text was showing the syntax instead of the structure. `## Plan`
 * appeared as literal hashes, a table as a wall of pipes, a code block bracketed by
 * visible backticks.
 *
 * Two deliberate choices about what this does NOT do:
 *
 *   - Raw HTML is escaped, not rendered. Agent output is only as trustworthy as the
 *     files it just read, so `rehype-raw` is deliberately absent; markdown-only is
 *     the whole sanitization story and there is no schema to get wrong, with one
 *     exception: ```mermaid fences render as diagrams through `MermaidBlock`,
 *     whose SVG passes through mermaid's strict mode plus DOMPurify with
 *     active-content tags forbidden. Syntax
 *     highlighting keeps that property — it renders a token tree as elements
 *     rather than injecting the highlighter's HTML string.
 *   - Nothing here re-parses or re-orders content. It renders exactly the text the
 *     daemon stored, so the rendered form and the record cannot disagree. That is
 *     also why "copy as markdown" copies the stored text and not a re-serialized
 *     version of the DOM.
 *
 * Streaming is fine unstyled-first: an unclosed fence renders as a plain code
 * block with partial content, and gains its colours when the closing fence
 * arrives. See `lib/code-highlight.ts` for why it is not tokenized before then.
 */

import {
	createContext,
	Fragment,
	memo,
	useContext,
	useState,
	type ReactNode,
} from "react";
import Markdown, { defaultUrlTransform, type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { WrapText } from "lucide-react";
import { cn } from "../../lib/utils";
import { aoBridge } from "../../lib/bridge";
import { canonicalLanguage } from "../../lib/code-highlight";
import { fenceOf } from "../../lib/markdown-fence";
import { isWebLink, openLinkInSystemBrowser } from "../../lib/external-link-policy";
import { isSessionLink, remarkSessionLinks } from "../../lib/session-links";
import {
	ContextMenu,
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuTrigger,
} from "../ui/context-menu";
import { HighlightedCode } from "./HighlightedCode";
import { MermaidBlock } from "./MermaidBlock";
import { CopyButton } from "./CopyButton";
import "./code-theme.css";

// Activity titles live inside disclosure buttons: keep inline formatting, but
// unwrap block elements and links so provider text cannot nest interactive UI.
const TITLE_ELEMENTS = ["code", "em", "strong", "del"];
const TITLE_COMPONENTS: Components = {
	code: ({ children }) => <code className="font-mono text-[0.95em]">{children}</code>,
};

export const ActivityTitle = memo(function ActivityTitle({ text }: { text: string }) {
	return (
		<Markdown allowedElements={TITLE_ELEMENTS} unwrapDisallowed components={TITLE_COMPONENTS}>
			{text}
		</Markdown>
	);
});

/** GitHub-flavoured markdown: tables, strikethrough, task lists, autolinks. */
const PLUGINS = [remarkGfm, remarkSessionLinks];

/**
 * Whether the prose is still arriving, for the fences inside it.
 *
 * A context rather than a prop because `COMPONENTS` has to stay module-level:
 * rebuilding that map per render would defeat react-markdown's own memoization
 * and re-parse every message on every poll.
 */
const StreamingProse = createContext(false);
const OpenChatLink = createContext<((url: string) => void) | undefined>(undefined);
const OpenSessionLink = createContext<((url: string) => void) | undefined>(undefined);

export function ChatLinkProvider({
	onLinkOpen,
	onSessionLinkOpen,
	children,
}: {
	onLinkOpen?: (url: string) => void;
	onSessionLinkOpen?: (url: string) => void;
	children: ReactNode;
}) {
	return (
		<OpenChatLink.Provider value={onLinkOpen}>
			<OpenSessionLink.Provider value={onSessionLinkOpen}>{children}</OpenSessionLink.Provider>
		</OpenChatLink.Provider>
	);
}

export const ChatMarkdown = memo(function ChatMarkdown({
	text,
	streaming = false,
	muted = false,
}: {
	text: string;
	streaming?: boolean;
	/**
	 * Prose that is not the answer. Reasoning is the case: it reads alongside the
	 * agent's reply and must not compete with it, so it steps back a tone rather than
	 * being dimmed with opacity — which would wash out code and links too.
	 */
	muted?: boolean;
}) {
	return (
		<StreamingProse.Provider value={streaming}>
			<div
				className={cn(
					"chat-md leading-[1.58]",
					muted ? "text-[13px] text-muted-foreground" : "text-sm text-foreground",
				)}
			>
				<Markdown remarkPlugins={PLUGINS} components={COMPONENTS} urlTransform={chatUrlTransform}>
					{text}
				</Markdown>
			</div>
		</StreamingProse.Provider>
	);
});

/**
 * Wrapping is a reading preference, not a property of one block: a fence that
 * scrolled out of view and back must not silently lose it, and a reader who
 * turned wrapping on is usually about to want it again — so the last choice seeds
 * the next block. Deliberately not persisted; reflowing every block of a
 * conversation on launch because of a choice made days ago is worse than starting
 * from the readable default.
 */
let preferredWrap = false;

/**
 * A fenced code block: language on the left, quiet actions on the right, its own
 * scroll.
 *
 * The block scrolls rather than wrapping because a wrapped line of code is harder
 * to read than a scrolled one, and it must never widen the conversation column.
 * The toggle is there for the case that argument loses — a single 300-character
 * line, where scrolling means losing the rest of the block.
 */
function CodeBlock({ code, language }: { code: string; language?: string }) {
	const streaming = useContext(StreamingProse);
	const grammar = canonicalLanguage(language);
	const [wrap, setWrap] = useState(preferredWrap);

	return (
		<div
			className="chat-code group/code my-2.5 overflow-hidden rounded-lg border border-border bg-surface"
			data-wrap={wrap ? "true" : "false"}
		>
			<div className="flex items-center gap-2 border-b border-border bg-raised/40 px-2.5 py-1">
				<span className="font-mono text-[10px] uppercase tracking-wide text-muted-foreground">
					{language || "text"}
				</span>
				{/* Hover-revealed, and focus-revealed so the keyboard can reach it. The
				    timeline's whole point is that prose dominates; a permanent row of
				    controls on every fence is the opposite. */}
				<div className="ml-auto flex items-center gap-0.5 opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/code:opacity-100">
					<button
						type="button"
						onClick={() => {
							preferredWrap = !wrap;
							setWrap(!wrap);
						}}
						aria-pressed={wrap}
						aria-label="Wrap long lines"
						title="Wrap long lines"
						className={cn(
							"flex size-7 items-center justify-center rounded-md transition-[background-color,color,transform] hover:bg-interactive-hover hover:text-foreground",
							wrap ? "text-accent" : "text-muted-foreground",
						)}
					>
						<WrapText aria-hidden="true" className="size-3" />
					</button>
					<CopyButton text={code} label="Copy code" />
				</div>
			</div>
			<pre className="scrollbar-none overflow-x-auto px-3 py-2.5">
				<code className="font-mono text-[12px] leading-[1.6] text-foreground">
					<HighlightedCode code={code} language={grammar} streaming={streaming} />
				</code>
			</pre>
		</div>
	);
}

const EMOJI_GRAPHEME = /\p{Extended_Pictographic}|\p{Regional_Indicator}|[#*0-9]\uFE0F?\u20E3/u;
const GRAPHEME_SEGMENTER = new Intl.Segmenter(undefined, { granularity: "grapheme" });

function compactEmoji(children: ReactNode): ReactNode {
	if (typeof children === "string") {
		let last = 0;
		const parts: ReactNode[] = [];
		for (const { segment, index } of GRAPHEME_SEGMENTER.segment(children)) {
			if (!EMOJI_GRAPHEME.test(segment)) continue;
			if (index > last) parts.push(children.slice(last, index));
			parts.push(
				<span key={`${index}-${segment}`} className="chat-md-emoji">
					{segment}
				</span>,
			);
			last = index + segment.length;
		}
		if (last === 0) return children;
		if (last < children.length) parts.push(children.slice(last));
		return parts;
	}
	if (Array.isArray(children)) {
		return children.map((child, index) => <Fragment key={index}>{compactEmoji(child)}</Fragment>);
	}
	return children;
}

function MarkdownLink({ href, children }: { href?: string; children?: ReactNode }) {
	const onLinkOpen = useContext(OpenChatLink);
	const onSessionLinkOpen = useContext(OpenSessionLink);
	const sessionLink = Boolean(href && isSessionLink(href));
	const anchor = (
		<a
			href={href}
			target={sessionLink ? undefined : "_blank"}
			rel="noreferrer noopener"
			onClick={(event) => {
				if (!href) return;
				event.preventDefault();
				if (sessionLink) {
					onSessionLinkOpen?.(href);
					return;
				}
				// Cmd/Ctrl-click (the VS Code/Slack convention) and Option/Alt-click
				// escape the in-app panel and go straight to the system browser.
				const toSystemBrowser = event.metaKey || event.ctrlKey || event.altKey;
				if (!toSystemBrowser && onLinkOpen && isWebLink(href)) {
					onLinkOpen(href);
					return;
				}
				void openLinkInSystemBrowser(href);
			}}
			className="text-markdown-link underline decoration-markdown-link/45 underline-offset-2 transition-colors hover:text-markdown-link-hover hover:decoration-markdown-link-hover/75"
		>
			{children}
		</a>
	);
	if (!href) return anchor;
	return (
		<ContextMenu>
			<ContextMenuTrigger asChild>{anchor}</ContextMenuTrigger>
			<ContextMenuContent className="min-w-44">
				{/* Only http(s) may reach shell.openExternal from here; other schemes
				    still get their address copied. */}
				{!sessionLink && isWebLink(href) ? (
					<ContextMenuItem onSelect={() => void openLinkInSystemBrowser(href)}>
						Open in system browser
					</ContextMenuItem>
				) : null}
				<ContextMenuItem onSelect={() => void aoBridge.clipboard.writeText(href)}>
					Copy link address
				</ContextMenuItem>
			</ContextMenuContent>
		</ContextMenu>
	);
}

function chatUrlTransform(url: string): string {
	return isSessionLink(url) ? url : defaultUrlTransform(url);
}

/**
 * A mermaid fence with the streaming state it was rendered under.
 *
 * A separate component rather than a hook call in the `pre` override because
 * overrides are not components: hooks are illegal there, and `COMPONENTS`
 * must stay module-level so react-markdown's memoization survives polling.
 */
function MermaidFence({ code }: { code: string }) {
	const streaming = useContext(StreamingProse);
	const onLinkOpen = useContext(OpenChatLink);
	return <MermaidBlock code={code} streaming={streaming} onLinkOpen={onLinkOpen} />;
}

const COMPONENTS: Components = {
	// Headings step down in size but stay in the conversation's voice — an agent's
	// "## Findings" is a paragraph label, not a page title.
	h1: ({ children }) => (
		<h3 className="mb-1.5 mt-4 text-[15px] font-semibold leading-snug text-foreground first:mt-0">
			{children}
		</h3>
	),
	h2: ({ children }) => (
		<h4 className="mb-1.5 mt-3.5 text-[14px] font-semibold leading-snug text-foreground first:mt-0">
			{children}
		</h4>
	),
	h3: ({ children }) => (
		<h5 className="mb-1 mt-3 text-[13.5px] font-semibold leading-snug text-foreground first:mt-0">
			{children}
		</h5>
	),
	h4: ({ children }) => (
		<h6 className="mb-1 mt-3 text-[13px] font-semibold uppercase tracking-wide text-muted-foreground first:mt-0">
			{children}
		</h6>
	),

	p: ({ children }) => <p className="my-2 first:mt-0 last:mb-0">{compactEmoji(children)}</p>,

	ul: ({ children }) => <ul className="my-2 ml-4 list-disc space-y-1 first:mt-0">{children}</ul>,
	ol: ({ children }) => (
		<ol className="my-2 ml-4 list-decimal space-y-1 first:mt-0">{children}</ol>
	),
	li: ({ children, className }) => (
		// A task-list item drops its bullet: the checkbox is the marker.
		<li className={cn("marker:text-muted-foreground", className?.includes("task-list-item") && "list-none")}>
			{compactEmoji(children)}
		</li>
	),
	// Read-only on purpose. The checkbox reflects what the agent wrote; clicking it
	// would imply AO could change the agent's plan, which it cannot.
	input: ({ checked, type }) =>
		type === "checkbox" ? (
			<input
				type="checkbox"
				checked={Boolean(checked)}
				readOnly
				aria-label={checked ? "done" : "not done"}
				className="mr-1.5 -ml-4 size-3 translate-y-[1px] accent-accent"
			/>
		) : null,

	// A fenced block arrives as `pre > code`, with the language — when the agent
	// gave one — as a class on the inner element. It is claimed here rather than in
	// `code` because a fence with no language carries no class at all, and matching
	// on the class alone rendered those as inline code.
	pre: ({ children }) => {
		const fence = fenceOf(children);
		if (!fence) return <>{children}</>;
		// Mermaid is a diagram, not source: it renders through the sandboxed SVG
		// path rather than the highlighter. See `MermaidBlock.tsx`.
		if (fence.language?.toLowerCase() === "mermaid") return <MermaidFence code={fence.code} />;
		return <CodeBlock code={fence.code} language={fence.language} />;
	},
	// Only inline code reaches here; `pre` above takes every fence.
	code: ({ children }) => (
		<code className="rounded bg-surface px-[5px] py-[2px] font-mono text-[11.5px] text-markdown-code">
			{children}
		</code>
	),

	// Wide tables scroll inside their own container so the conversation column
	// never scrolls sideways.
	table: ({ children }) => (
		<div className="my-2.5 overflow-x-auto rounded-lg border border-border">
			<table className="w-full border-collapse text-[12.5px]">{children}</table>
		</div>
	),
	thead: ({ children }) => <thead className="bg-raised/40">{children}</thead>,
	th: ({ children }) => (
		<th className="border-b border-border px-2.5 py-1.5 text-left font-medium text-muted-foreground">
			{compactEmoji(children)}
		</th>
	),
	td: ({ children }) => (
		<td className="border-b border-border/60 px-2.5 py-1.5 align-top">{compactEmoji(children)}</td>
	),

	blockquote: ({ children }) => (
		<blockquote className="my-2.5 border-l-2 border-border-strong pl-3 text-muted-foreground">
			{children}
		</blockquote>
	),
	hr: () => <hr className="my-3 border-border" />,
	strong: ({ children }) => <strong className="font-semibold text-foreground">{children}</strong>,
	del: ({ children }) => <del className="text-muted-foreground">{children}</del>,

	// Match terminal links: a plain HTTP(S) click uses the session's AO Browser;
	// Cmd/Ctrl- or Option/Alt-click and non-web schemes use the system browser,
	// and right-click offers the system browser and copying the address.
	a: MarkdownLink,

	img: ({ src, alt }) => (
		<img src={typeof src === "string" ? src : undefined} alt={alt ?? ""} className="my-2 max-w-full rounded-md border border-border" />
	),
};
