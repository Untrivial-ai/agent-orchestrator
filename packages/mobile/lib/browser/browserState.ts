export type MobileBrowserState = {
	url: string;
	title?: string;
	canGoBack: boolean;
	canGoForward: boolean;
	loading: boolean;
	error?: string;
};

export const initialBrowserState: MobileBrowserState = {
	url: "",
	canGoBack: false,
	canGoForward: false,
	loading: false,
};

export type BrowserNavigationUpdate = Partial<Pick<MobileBrowserState, "url" | "title" | "canGoBack" | "canGoForward" | "loading">>;

export function browserLoadStart(state: MobileBrowserState, url?: string): MobileBrowserState {
	return { ...state, ...(url ? { url } : {}), loading: true, error: undefined };
}

export function browserLoadEnd(state: MobileBrowserState): MobileBrowserState {
	return { ...state, loading: false };
}

export function browserLoadError(state: MobileBrowserState, message: string): MobileBrowserState {
	return { ...state, loading: false, error: message || "Couldn't load this page." };
}

export function browserNavigationChanged(state: MobileBrowserState, update: BrowserNavigationUpdate): MobileBrowserState {
	return {
		...state,
		...defined({
			url: update.url,
			title: update.title,
			canGoBack: update.canGoBack,
			canGoForward: update.canGoForward,
			loading: update.loading,
		}),
		error: update.loading ? undefined : state.error,
	};
}

function defined<T extends Record<string, unknown>>(value: T): Partial<T> {
	return Object.fromEntries(Object.entries(value).filter(([, entry]) => entry !== undefined)) as Partial<T>;
}
