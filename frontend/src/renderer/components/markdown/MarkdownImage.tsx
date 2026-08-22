import { useContext } from "react";
import { MarkdownFileContext } from "./markdown-file-context";
import { resolveMarkdownImageSrc } from "../../lib/markdown-image-resolver";

/** react-markdown's `img` override: resolves a worktree-relative `src` via the workspace blob route. */
export function MarkdownImage({ src, alt }: { src?: string; alt?: string }) {
	const context = useContext(MarkdownFileContext);
	const resolvedSrc = context ? resolveMarkdownImageSrc(context.sessionId, context.filePath, src) : src;
	return <img src={resolvedSrc} alt={alt ?? ""} />;
}
