import { useContext, useState } from "react";
import { MarkdownFileContext } from "./markdown-file-context";
import { resolveMarkdownImageSrc } from "../../lib/markdown-image-resolver";

/**
 * react-markdown's `img` override: resolves a worktree-relative `src` via the
 * workspace blob route.
 *
 * A source that fails falls back to its alt text rather than a broken-image box.
 * The common case is not a typo: the renderer's CSP allows images only from
 * itself, `data:`, and the loopback daemon, so an `https://` image in a README
 * (a shields.io badge, a hosted screenshot) cannot load at all. Alt text is what
 * that reference actually has to offer here.
 */
export function MarkdownImage({ src, alt }: { src?: string; alt?: string }) {
	const context = useContext(MarkdownFileContext);
	// The failed URL rather than a boolean: a version bump hands us a new URL for
	// the same reference, and that one deserves its own attempt.
	const [failedSrc, setFailedSrc] = useState<string | null>(null);
	const resolvedSrc = context
		? resolveMarkdownImageSrc(context.sessionId, context.filePath, src, context.version)
		: src;
	if (!resolvedSrc || resolvedSrc === failedSrc) return <span className="text-muted-foreground">{alt ?? ""}</span>;
	return <img src={resolvedSrc} alt={alt ?? ""} onError={() => setFailedSrc(resolvedSrc)} />;
}
