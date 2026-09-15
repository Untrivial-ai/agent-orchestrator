/**
 * Where an image in chat prose loads from.
 *
 * Agents save screenshots and diagrams in their worktree, and the natural way to
 * show one is `![](docs/screenshot.png)`. That path means nothing to the browser
 * on its own. The alternative, writing the full daemon URL, bakes the daemon's
 * port and the session id into the stored transcript, so every image in a past
 * conversation breaks when the port changes. Resolving at render time keeps the
 * stored text portable: the URL is built from the renderer's current API base.
 *
 * A relative path resolves from the worktree root, since a chat message has no
 * file of its own to be relative to, through the same workspace blob route the
 * file viewer uses (see `lib/markdown-image-resolver.ts`). Absolute sources pass
 * through untouched.
 */

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { resolveMarkdownImageSrc } from "../../lib/markdown-image-resolver";

type ChatImageSource = { sessionId: string; version: number };

/**
 * A context rather than a prop for the same reason as `StreamingProse` in
 * `ChatMarkdown.tsx`: the components map has to stay module-level.
 */
const ChatImageSourceContext = createContext<ChatImageSource | undefined>(undefined);

export function ChatImageSourceProvider({ sessionId, children }: { sessionId: string; children: ReactNode }) {
	// The blob route is `no-store`, but the browser never refetches an unchanged
	// URL. One version per mount: reopening the chat picks up an image the agent
	// rewrote, while polling re-renders keep the URL stable instead of reloading.
	const [version] = useState(() => Date.now());
	const value = useMemo(() => ({ sessionId, version }), [sessionId, version]);
	return <ChatImageSourceContext.Provider value={value}>{children}</ChatImageSourceContext.Provider>;
}

/** The URL to load for an image the agent wrote; unchanged outside a session. */
export function useChatImageSrc(src: string | undefined): string | undefined {
	const source = useContext(ChatImageSourceContext);
	if (!source) return src;
	return resolveMarkdownImageSrc(source.sessionId, "", src, source.version);
}

/** react-markdown's `img` override for chat. */
export function ChatMarkdownImage({ src, alt }: { src?: string | Blob; alt?: string }) {
	const resolved = useChatImageSrc(typeof src === "string" ? src : undefined);
	return <img src={resolved} alt={alt ?? ""} className="my-2 max-w-full rounded-md border border-border" />;
}
