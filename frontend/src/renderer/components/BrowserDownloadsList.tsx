import { Ban, Folder, FolderOpen, Pause, Play, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { BrowserDownload, BrowserDownloadAction } from "../../shared/browser-downloads";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

function formatBytes(bytes: number): string {
	if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
	const units = ["B", "KB", "MB", "GB"];
	const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
	const value = bytes / 1024 ** exponent;
	return `${value >= 10 || exponent === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[exponent]}`;
}

export function BrowserDownloadsList({
	downloads,
	error,
	onAction,
	compact = false,
}: {
	downloads: BrowserDownload[];
	error?: string;
	onAction: (id: string, action: BrowserDownloadAction) => void;
	compact?: boolean;
}) {
	const { t } = useTranslation();
	if (downloads.length === 0) {
		return (
			<>
				<p className="px-3 py-6 text-center text-xs text-muted-foreground">{t("browser.downloads.empty")}</p>
				{error ? <p className="px-3 py-2 text-xs text-destructive" role="alert">{error}</p> : null}
			</>
		);
	}

	return (
		<div className={cn("board-scrollbar flex flex-col overflow-y-auto", compact ? "max-h-80" : "max-h-[28rem] gap-2")}>
			{downloads.map((download) => {
				const progress = download.totalBytes > 0
					? Math.min(100, Math.max(0, (download.receivedBytes / download.totalBytes) * 100))
					: 0;
				const resumableInterrupted = download.status === "interrupted" && Boolean(download.resumable);
				const active = download.active ?? (
					download.status === "progressing" || download.status === "paused" || resumableInterrupted
				);
				const terminalStatus = download.status === "completed"
					? t("browser.downloads.completed")
					: download.status === "cancelled"
						? t("browser.downloads.cancelled")
						: t("browser.downloads.interrupted");
				return (
					<div className={cn("min-w-0", compact ? "border-b border-border px-3 py-2.5 last:border-b-0" : "rounded-md border border-border bg-card px-3 py-3")} key={download.id}>
						<div className="flex min-w-0 items-center gap-2">
							<div className="grid size-8 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
								{download.status === "cancelled" || download.status === "interrupted"
									? <Ban aria-hidden="true" className="size-4" />
									: <FolderOpen aria-hidden="true" className="size-4" />}
							</div>
							<div className="min-w-0 flex-1">
								<p className="truncate text-xs font-medium text-foreground" title={download.fileName}>{download.fileName}</p>
								<p className="mt-0.5 text-caption text-muted-foreground">
									{active
										? download.status === "interrupted"
											? t("browser.downloads.interrupted")
											: t(download.status === "paused" ? "browser.downloads.paused" : "browser.downloads.progress", {
											received: formatBytes(download.receivedBytes),
											total: download.totalBytes > 0 ? formatBytes(download.totalBytes) : t("browser.downloads.unknownSize"),
										})
										: terminalStatus}
								</p>
							</div>
							<div className="flex shrink-0 items-center gap-0.5">
								{download.status === "progressing" ? (
									<Button aria-label={t("browser.downloads.pause", { file: download.fileName })} onClick={() => onAction(download.id, "pause")} size="icon-sm" type="button" variant="ghost"><Pause aria-hidden="true" className="size-3.5" /></Button>
								) : download.status === "paused" || resumableInterrupted ? (
									<Button aria-label={t("browser.downloads.resume", { file: download.fileName })} onClick={() => onAction(download.id, "resume")} size="icon-sm" type="button" variant="ghost"><Play aria-hidden="true" className="size-3.5" /></Button>
								) : download.status === "completed" ? (
									<>
										<Button aria-label={t("browser.downloads.open", { file: download.fileName })} onClick={() => onAction(download.id, "open")} size="icon-sm" type="button" variant="ghost"><Folder aria-hidden="true" className="size-3.5" /></Button>
										<Button aria-label={t("browser.downloads.show", { file: download.fileName })} onClick={() => onAction(download.id, "show")} size="icon-sm" type="button" variant="ghost"><FolderOpen aria-hidden="true" className="size-3.5" /></Button>
									</>
								) : null}
								<Button
									aria-label={t(active ? "browser.downloads.cancel" : "browser.downloads.remove", { file: download.fileName })}
									onClick={() => onAction(download.id, active ? "cancel" : "remove")}
									size="icon-sm"
									type="button"
									variant="ghost"
								>
									{active ? <X aria-hidden="true" className="size-3.5" /> : <Trash2 aria-hidden="true" className="size-3.5" />}
								</Button>
							</div>
						</div>
						{active ? <div aria-label={t("browser.downloads.progressLabel", { file: download.fileName })} aria-valuemax={100} aria-valuemin={0} aria-valuenow={Math.round(progress)} className="mt-2 h-1 overflow-hidden rounded-full bg-muted" role="progressbar"><div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${progress}%` }} /></div> : null}
					</div>
				);
			})}
			{error ? <p className="px-3 py-2 text-xs text-destructive" role="alert">{error}</p> : null}
		</div>
	);
}
