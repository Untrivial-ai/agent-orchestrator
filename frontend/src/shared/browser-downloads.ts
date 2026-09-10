export type BrowserDownloadStatus =
	| "progressing"
	| "paused"
	| "completed"
	| "cancelled"
	| "interrupted";

export type BrowserDownload = {
	id: string;
	fileName: string;
	receivedBytes: number;
	totalBytes: number;
	status: BrowserDownloadStatus;
	active?: boolean;
	resumable?: boolean;
	startedAt: number;
	updatedAt: number;
};

export type BrowserDownloadsState = {
	downloads: BrowserDownload[];
	error?: string;
};

export type BrowserDownloadAction = "pause" | "resume" | "cancel" | "open" | "show" | "remove";

export type BrowserDownloadActionInput = {
	id: string;
	action: BrowserDownloadAction;
};
