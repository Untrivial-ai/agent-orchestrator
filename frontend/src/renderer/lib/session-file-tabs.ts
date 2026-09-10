export type SessionFileTabState = {
	openPaths: string[];
	activePath: string | null;
};

export const EMPTY_SESSION_FILE_TABS: SessionFileTabState = { openPaths: [], activePath: null };
export const emptySessionFileTabs = (): SessionFileTabState => ({ ...EMPTY_SESSION_FILE_TABS });

export function openSessionFile(state: SessionFileTabState, path: string): SessionFileTabState {
	return {
		openPaths: state.openPaths.includes(path) ? state.openPaths : [...state.openPaths, path],
		activePath: path,
	};
}

export function activateSessionFile(state: SessionFileTabState, path: string | null): SessionFileTabState {
	if (path === null || state.openPaths.includes(path)) return { ...state, activePath: path };
	return state;
}

export function closeSessionFile(state: SessionFileTabState, path: string): SessionFileTabState {
	const index = state.openPaths.indexOf(path);
	if (index < 0) return state;
	const openPaths = state.openPaths.filter((candidate) => candidate !== path);
	if (state.activePath !== path) return { openPaths, activePath: state.activePath };
	return { openPaths, activePath: openPaths[index] ?? openPaths[index - 1] ?? null };
}

export const closeAllSessionFiles = (): SessionFileTabState => emptySessionFileTabs();
