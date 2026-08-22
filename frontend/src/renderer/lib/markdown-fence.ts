/**
 * Reading a fenced code block out of react-markdown's `pre` override.
 *
 * Shared because two surfaces render agent-authored markdown with the same
 * highlighter — `chat/ChatMarkdown.tsx` and `markdown/MarkdownFileView.tsx` —
 * and a second copy of this would be a second place for the language sniffing
 * to drift.
 */

import { isValidElement, type ReactNode } from "react";

/** The text inside a node, for the copy button and language sniffing. */
export function textOf(children: ReactNode): string {
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
export function fenceOf(children: ReactNode): { code: string; language?: string } | undefined {
	if (!isValidElement<{ className?: string; children?: ReactNode }>(children)) return undefined;
	return {
		code: textOf(children.props.children).replace(/\n$/, ""),
		language: LANGUAGE_CLASS.exec(children.props.className ?? "")?.[1],
	};
}
