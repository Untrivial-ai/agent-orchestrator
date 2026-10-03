import AsyncStorage from "@react-native-async-storage/async-storage";

const PREFIX = "ao.mobileBrowser.lastUrl.v1";

export function browserSessionUrlKey(host: string, httpPort: string | number, sessionId: string): string {
	return `${PREFIX}.${encodeURIComponent(host)}.${httpPort}.${encodeURIComponent(sessionId)}`;
}

export async function loadBrowserSessionUrl(key: string): Promise<string | undefined> {
	try {
		const value = await AsyncStorage.getItem(key);
		if (!value) return undefined;
		const url = new URL(value);
		return url.protocol === "http:" || url.protocol === "https:" ? url.href : undefined;
	} catch {
		return undefined;
	}
}

export async function saveBrowserSessionUrl(key: string, value: string): Promise<void> {
	try {
		const url = new URL(value);
		if (url.protocol !== "http:" && url.protocol !== "https:") return;
		await AsyncStorage.setItem(key, url.href);
	} catch {
		// Browser history is a convenience. Storage failures must not break Preview.
	}
}
