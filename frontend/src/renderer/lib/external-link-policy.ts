import { aoBridge } from "./bridge";

export function isWebLink(url: string): boolean {
	try {
		const { protocol } = new URL(url);
		return protocol === "http:" || protocol === "https:";
	} catch {
		return false;
	}
}

function decodedPath(url: string): string {
	const trimmed = url.trim();
	let path = trimmed.split(/[?#]/, 1)[0] ?? "";
	if (/^file:/i.test(trimmed)) {
		try {
			path = new URL(trimmed).pathname;
		} catch {
			return "";
		}
	}
	try {
		path = decodeURIComponent(path);
	} catch {
		// Keep malformed percent escapes inert; the preview endpoint will reject
		// anything it cannot resolve inside the session workspace.
	}
	return path.replace(/\\/g, "/");
}

function hasParentTraversal(path: string): boolean {
	return path.split("/").includes("..");
}

function comparableWorkspacePath(path: string): string {
	return path.replace(/^(?:\.\/)+/, "").replace(/^\/+/, "");
}

/** Resolve a displayed path to the workspace-relative path returned by Files. */
export function workspaceFilePath(url: string, workspacePaths: string[]): string | undefined {
	const path = decodedPath(url);
	if (!path || hasParentTraversal(path)) return undefined;
	const comparable = comparableWorkspacePath(path);
	return workspacePaths.find((candidate) => {
		const workspacePath = comparableWorkspacePath(decodedPath(candidate));
		return !!workspacePath && (comparable === workspacePath || comparable.endsWith(`/${workspacePath}`));
	});
}

export function isWorkspaceFileLink(url: string, workspacePaths: string[]): boolean {
	return workspaceFilePath(url, workspacePaths) !== undefined;
}

/**
 * Whether a chat href looks like a local file path worth asking the daemon to
 * resolve. This does not grant filesystem access: the session preview endpoint
 * still confines the result to the workspace before serving it.
 */
export function isPotentialWorkspaceFileLink(url: string): boolean {
	const trimmed = url.trim();
	if (!trimmed || trimmed.startsWith("#") || isWebLink(trimmed)) return false;
	if (/^file:/i.test(trimmed)) return !hasParentTraversal(decodedPath(trimmed));

	const path = decodedPath(trimmed);
	if (!path || hasParentTraversal(path) || path.startsWith("//")) return false;
	if (/^[a-z][a-z\d+.-]*:/i.test(path) && !/^[a-z]:\//i.test(path)) return false;

	return (
		/^[a-z]:\//i.test(path) ||
		path.startsWith("/") ||
		path.startsWith("./") ||
		path.includes("/") ||
		/(?:^|\/)\.[^/]+$/.test(path) ||
		/\.[a-z\d][a-z\d._-]*$/i.test(path)
	);
}

export function isWorkspaceHtmlLink(url: string, workspacePaths: string[]): boolean {
	return /\.html?$/i.test(url.split(/[?#]/, 1)[0]) && isWorkspaceFileLink(url, workspacePaths);
}

export async function openLinkInSystemBrowser(url: string): Promise<void> {
	try {
		await aoBridge.app.openExternal(url);
	} catch (error) {
		console.warn("Unable to open link in system browser", error);
	}
}

export function handleModifierLinkClick(event: MouseEvent): void {
	if (event.button !== 0 || !event.altKey || event.defaultPrevented) return;
	const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null;
	const href = anchor?.getAttribute("href");
	if (!href) return;

	let url: URL;
	try {
		url = new URL(href, window.location.href);
	} catch {
		return;
	}
	if (!["http:", "https:"].includes(url.protocol) || url.origin === window.location.origin) return;

	event.preventDefault();
	void openLinkInSystemBrowser(url.href);
}
