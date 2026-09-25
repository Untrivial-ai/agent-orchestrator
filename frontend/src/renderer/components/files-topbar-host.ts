import { createContext, useContext } from "react";

/**
 * The inspector's top-bar slot for the Files tab. Like the browser's address
 * bar, the Files panel renders its filter field into this element when it is
 * shown inside the inspector; outside it (e.g. the maximized center view) there
 * is no host and the field stays in the panel's own header.
 */
export const FilesTopbarHostContext = createContext<HTMLElement | null>(null);

export function useFilesTopbarHost(): HTMLElement | null {
	return useContext(FilesTopbarHostContext);
}
