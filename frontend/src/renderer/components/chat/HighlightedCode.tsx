/**
 * Tokenized code as React elements.
 *
 * Extracted from `ChatMarkdown` so a patch can be highlighted by the same engine as
 * a fenced block. Two highlighters in one surface would mean two grammar payloads,
 * two caches, and two chances for a code block and a diff to disagree about what
 * `go` looks like.
 *
 * It renders the token content only — no `pre`, no chrome — because the two callers
 * frame it differently: a fence has a language header and copy controls, a patch sits
 * inside a file row. Everything about how the tree is produced is shared; nothing
 * about how it is framed is.
 */

import { memo, useEffect, useReducer, type ReactNode } from "react";
import type { Root, RootContent } from "hast";
import { highlight, highlightSync, type GrammarName } from "../../lib/code-highlight";
// The token colours live with the engine rather than with one caller: they are
// scoped under `.chat-code`/`.markdown-code`, so anything rendering these class
// names has to be inside one of those containers and has to have loaded this sheet.
import "./code-theme.css";

/**
 * Code, highlighted when it can be, plain otherwise.
 *
 * Memoized because the timeline re-renders on every poll: a settled block's props
 * do not change, so it should not walk its token tree again once a second.
 */
export const HighlightedCode = memo(function HighlightedCode({
	code,
	language,
	streaming,
}: {
	code: string;
	/** Undefined renders plain monospace and loads no grammar at all. */
	language?: GrammarName;
	/** Text still arriving. A block that changes per delta is not worth tokenizing. */
	streaming?: boolean;
}) {
	const tokens = useHighlighted(code, language, Boolean(streaming));
	return <>{tokens ? renderTokens(tokens.children) : code}</>;
});

/**
 * The token tree for a settled block, as soon as one can be had.
 *
 * Two paths, because the flash between them is the whole problem: a cached or
 * already-warm block is highlighted in its first render, and only a cold one renders
 * plain for a beat while the grammars load. The cache is the state — the async path
 * writes into it and this re-reads it, so nothing here can hold a tree that disagrees
 * with the one the next block will get.
 */
export function useHighlighted(
	code: string,
	language: GrammarName | undefined,
	streaming: boolean,
): Root | undefined {
	// A streaming fence has no settled content: its text changes on every delta, so
	// tokenizing it would re-parse per delta and fill the cache with keys nothing
	// will ever read again.
	const wanted = Boolean(language) && !streaming;
	const tokens = wanted ? highlightSync(code, language!) : undefined;
	const [, reread] = useReducer((count: number) => count + 1, 0);

	useEffect(() => {
		if (!wanted || tokens) return;
		let live = true;
		void highlight(code, language!).then(() => {
			if (live) reread();
		});
		return () => {
			live = false;
		};
	}, [code, language, wanted, tokens]);

	return tokens;
}

/**
 * highlight.js output as React elements rather than as an HTML string.
 *
 * `dangerouslySetInnerHTML` is the usual way to do this and it is exactly what the
 * chat surface rules out: the escaping guarantee would then depend on the highlighter
 * rather than on there being no HTML path at all. The tree is only spans carrying
 * class names, so walking it is cheap. Index keys are correct here — the tree is
 * rebuilt whole or read from cache, never patched.
 */
export function renderTokens(nodes: readonly RootContent[]): ReactNode {
	return nodes.map((node, index) => {
		if (node.type === "text") return node.value;
		if (node.type !== "element") return null;
		const className = node.properties.className;
		return (
			<span key={index} className={Array.isArray(className) ? className.join(" ") : undefined}>
				{renderTokens(node.children)}
			</span>
		);
	});
}
