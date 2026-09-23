import { useState } from "react";
import { useTranslation } from "react-i18next";
import { getApiBaseUrl } from "../lib/api-client";
import { cn } from "../lib/utils";
import type { WorkspaceFileSummary } from "../hooks/useSessionWorkspaceFiles";

type WorkspaceFileStatus = WorkspaceFileSummary["status"];
type ImageDiffSide = "before" | "after";

// A light checkerboard so transparent pixels read as transparent instead of
// borrowing the panel background.
const CHECKERBOARD =
	"repeating-conic-gradient(color-mix(in srgb, currentColor 8%, transparent) 0% 25%, transparent 0% 50%) 0 0 / 16px 16px";

// workspaceImageUrl points an <img> straight at the daemon's blob route. The
// route sets no-store, so `version` — the file detail's load timestamp — is what
// makes an edited image reload: without a changing URL the element never
// refetches at all.
function workspaceImageUrl(sessionId: string, path: string, side: ImageDiffSide, version: number): string {
	const query = new URLSearchParams({ path, side, v: String(version) });
	return `${getApiBaseUrl()}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/file/blob?${query}`;
}

/**
 * ImageDiffView renders an image file's change as the images themselves —
 * before and after — instead of the "binary file" placeholder a text diff falls
 * back to. Added files have no before side and deleted files have no after
 * side, so those render a single pane.
 *
 * Each pane is keyed by the source it points at, so re-saving a broken image
 * remounts the pane instead of leaving its failed/dimension state stuck on the
 * previous load.
 */
export function ImageDiffView({
	path,
	sessionId,
	split,
	status,
	version,
}: {
	path: string;
	sessionId: string;
	split: boolean;
	status: WorkspaceFileStatus;
	version: number;
}) {
	const { t } = useTranslation();
	// A file with no change on this side has nothing to compare against: an added
	// file has no base revision, a deleted one has no worktree copy, and an
	// unmodified one would just render the same image twice.
	const showBefore = status !== "added" && status !== "unmodified";
	const showAfter = status !== "deleted";
	const bothSides = showBefore && showAfter;
	return (
		<div className={cn("grid gap-2 p-2.5", bothSides && split ? "grid-cols-2" : "grid-cols-1")}>
			{showBefore ? (
				<ImageDiffPane
					key={`before:${path}:${version}`}
					label={t("files.imageBefore")}
					path={path}
					side="before"
					sessionId={sessionId}
					version={version}
				/>
			) : null}
			{showAfter ? (
				<ImageDiffPane
					key={`after:${path}:${version}`}
					label={t("files.imageAfter")}
					path={path}
					side="after"
					sessionId={sessionId}
					version={version}
				/>
			) : null}
		</div>
	);
}

function ImageDiffPane({
	label,
	path,
	side,
	sessionId,
	version,
}: {
	label: string;
	path: string;
	side: ImageDiffSide;
	sessionId: string;
	version: number;
}) {
	const { t } = useTranslation();
	const [size, setSize] = useState<{ width: number; height: number } | null>(null);
	const [failed, setFailed] = useState(false);
	return (
		<figure className="min-w-0 overflow-hidden rounded-md border border-border/60">
			<figcaption className="flex items-center justify-between gap-2 border-b border-border/60 bg-background/60 px-2 py-1">
				<span
					className={cn("text-2xs font-medium uppercase tracking-wide", side === "before" ? "text-error" : "text-success")}
				>
					{label}
				</span>
				<span className="truncate font-mono text-2xs text-passive">
					{size ? t("files.imageDimensions", { height: size.height, width: size.width }) : null}
				</span>
			</figcaption>
			<div className="grid min-h-24 place-items-center p-3 text-passive" style={{ background: CHECKERBOARD }}>
				{failed ? (
					<p className="text-center text-xs text-muted-foreground">{t("files.imagePreviewFailed")}</p>
				) : (
					<img
						alt={t("files.imageAlt", { file: path, side: label })}
						className="max-h-[420px] max-w-full object-contain"
						onError={() => setFailed(true)}
						onLoad={(event) =>
							setSize({ height: event.currentTarget.naturalHeight, width: event.currentTarget.naturalWidth })
						}
						src={workspaceImageUrl(sessionId, path, side, version)}
					/>
				)}
			</div>
		</figure>
	);
}
