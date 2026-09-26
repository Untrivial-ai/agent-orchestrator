import { useCallback, useEffect, useState } from "react";
import type { BrowserDownload, BrowserDownloadAction } from "../../shared/browser-downloads";
import { aoBridge } from "../lib/bridge";

export function useBrowserDownloads() {
	const bridge = aoBridge.browser?.downloads;
	const [downloads, setDownloads] = useState<BrowserDownload[]>([]);
	const [error, setError] = useState("");
	const [initialized, setInitialized] = useState(false);

	useEffect(() => {
		setInitialized(false);
		if (!bridge) {
			setDownloads([]);
			setInitialized(true);
			return;
		}
		let active = true;
		void bridge.list().then((state) => {
			if (!active) return;
			setDownloads(state.downloads);
			setError(state.error ?? "");
			setInitialized(true);
		}).catch((reason) => {
			if (!active) return;
			setError(reason instanceof Error ? reason.message : String(reason));
			setInitialized(true);
		});
		const unsubscribe = bridge.onChanged((state) => {
			setDownloads(state.downloads);
			setError(state.error ?? "");
		});
		return () => {
			active = false;
			unsubscribe();
		};
	}, [bridge]);

	const action = useCallback(async (id: string, nextAction: BrowserDownloadAction) => {
		if (!bridge) return;
		try {
			const state = await bridge.action({ id, action: nextAction });
			setDownloads(state.downloads);
			setError(state.error ?? "");
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : String(reason));
		}
	}, [bridge]);

	const clear = useCallback(async () => {
		if (!bridge) return;
		try {
			const state = await bridge.clear();
			setDownloads(state.downloads);
			setError(state.error ?? "");
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : String(reason));
		}
	}, [bridge]);

	return { downloads, error, initialized, action, clear };
}
