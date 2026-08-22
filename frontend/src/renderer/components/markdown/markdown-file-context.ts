import { createContext } from "react";

export type MarkdownFileContextValue = {
	sessionId: string;
	filePath: string;
	/**
	 * The file detail's load timestamp. The blob route sets `no-store`, so this is
	 * what makes a rewritten image reload — see `buildWorkspaceBlobUrl`.
	 */
	version: number;
};

// The `img` override only receives hast-derived props (src/alt), so the
// session/file it's resolving a relative path against has to arrive via
// context rather than a prop react-markdown would need to be told to pass.
export const MarkdownFileContext = createContext<MarkdownFileContextValue | null>(null);
