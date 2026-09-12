/**
 * Resolves a markdown image's `src` against the worktree it was rendered from.
 *
 * A rendered `.md` file is read over the REST API, not from disk (the renderer
 * has no `file://`/IPC file-read access — see `ImageDiffView.tsx`, which already
 * solved this same problem for image-diff previews via the workspace file blob
 * route). `./assets/flow.png` in a markdown file means nothing to the browser on
 * its own; it has to be resolved against the `.md` file's own directory and
 * turned into a URL against that same blob route.
 */

import { getApiBaseUrl } from "./api-client";

const ABSOLUTE_SRC = /^[a-z][a-z0-9+.-]*:/i;

/** True for a scheme-qualified URL (`https:`, `data:`, ...) or a root-relative path. */
export function isAbsoluteMarkdownAssetSrc(src: string): boolean {
	return ABSOLUTE_SRC.test(src) || src.startsWith("/");
}

/**
 * POSIX join + normalize, without Node's `path` module — `nodeIntegration` is
 * disabled in this Electron app, so `path` is not resolvable in the renderer.
 * A `..` that would climb above the workspace root is dropped rather than kept,
 * clamping at the root instead of producing an escaping path.
 */
function normalizeSegments(joined: string): string {
	const stack: string[] = [];
	for (const segment of joined.split("/")) {
		if (segment === "" || segment === ".") continue;
		if (segment === "..") {
			stack.pop();
			continue;
		}
		stack.push(segment);
	}
	return stack.join("/");
}

/** The workspace-relative path a markdown-relative `src` refers to. */
export function resolveMarkdownAssetPath(markdownFilePath: string, rawSrc: string): string {
	// A trailing query/fragment on the image reference isn't meaningful once
	// folded into the blob route's own `path=`/`side=` query string.
	const withoutQueryOrFragment = rawSrc.replace(/[?#].*$/, "");
	// react-markdown URI-encodes path characters before the img override sees
	// them. Decode once here so URLSearchParams does not turn `%20` into `%2520`
	// and ask the daemon for a file whose name literally contains "%20".
	let decodedPath = withoutQueryOrFragment;
	try {
		decodedPath = decodeURIComponent(withoutQueryOrFragment);
	} catch {
		// Keep malformed input inert rather than letting one bad image abort the
		// entire Markdown render. The blob route still confines the final path.
	}
	const lastSlash = markdownFilePath.lastIndexOf("/");
	const markdownDir = lastSlash === -1 ? "" : markdownFilePath.slice(0, lastSlash);
	const joined = markdownDir ? `${markdownDir}/${decodedPath}` : decodedPath;
	return normalizeSegments(joined);
}

/**
 * Matches `ImageDiffView.tsx`'s `workspaceImageUrl` — the same blob route,
 * `side=after` for current content.
 *
 * `version` is not decoration. The blob route sets `no-store`, so as
 * `ImageDiffView` puts it: without a changing URL the element never refetches at
 * all. Pass the file detail's load timestamp so an image the agent rewrites
 * actually reloads instead of sitting on the copy the browser already has.
 */
export function buildWorkspaceBlobUrl(sessionId: string, path: string, version: number): string {
	const query = new URLSearchParams({ path, side: "after", v: String(version) });
	return `${getApiBaseUrl()}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/file/blob?${query}`;
}

/** `undefined` for an empty src or an absolute one that should pass through untouched. */
export function resolveMarkdownImageSrc(
	sessionId: string,
	markdownFilePath: string,
	rawSrc: string | undefined,
	version: number,
): string | undefined {
	if (!rawSrc) return undefined;
	if (isAbsoluteMarkdownAssetSrc(rawSrc)) return rawSrc;
	return buildWorkspaceBlobUrl(sessionId, resolveMarkdownAssetPath(markdownFilePath, rawSrc), version);
}
