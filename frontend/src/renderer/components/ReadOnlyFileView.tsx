import { useTranslation } from "react-i18next";
import { getApiBaseUrl } from "../lib/api-client";
import type { WorkspaceFileDetail } from "../hooks/useSessionWorkspaceFiles";
import { canonicalLanguage } from "../lib/code-highlight";
import { HighlightedCode } from "./chat/HighlightedCode";
import { PanelMessage } from "./WorkspaceDiffView";

function formatBytes(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function workspaceRawImageUrl(sessionId: string, path: string): string {
	const query = new URLSearchParams({ path, side: "after" });
	return `${getApiBaseUrl()}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/file/blob?${query}`;
}

// Renders an untouched (unmodified) workspace file: an agent didn't write
// this one, so there's no diff to show, just its current content. Binary and
// oversized files short-circuit before any tokenization is attempted.
export function ReadOnlyFileView({ detail, sessionId }: { detail: WorkspaceFileDetail; sessionId: string }) {
	const { t } = useTranslation();
	if (detail.binary) {
		if (detail.imageMediaType) {
			return (
				<div className="grid place-items-center p-3">
					<img
						alt={detail.path}
						className="max-h-[70vh] max-w-full object-contain"
						src={workspaceRawImageUrl(sessionId, detail.path)}
					/>
				</div>
			);
		}
		return <PanelMessage>{t("files.binaryUnavailable")}</PanelMessage>;
	}
	if (detail.contentTruncated) {
		return <PanelMessage>{t("files.explorer.tooLarge", { size: formatBytes(detail.size) })}</PanelMessage>;
	}
	return <HighlightedContent content={detail.content} path={detail.path} />;
}

// Reuse AO's CSP-compatible highlight.js pipeline so file previews and code
// shown elsewhere in the renderer share grammars, colors, and caching.
function HighlightedContent({ content, path }: { content: string; path: string }) {
	const extension = path.split("/").pop()?.split(".").pop();
	const language = canonicalLanguage(extension);
	return (
		<div className="chat-code min-h-0 flex-1 overflow-auto p-3">
			<pre className="whitespace-pre font-mono text-xs leading-5 text-foreground">
				<code>
					<HighlightedCode code={content} language={language} />
				</code>
			</pre>
		</div>
	);
}
