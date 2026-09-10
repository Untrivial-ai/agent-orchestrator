export const OPEN_DIALOG_OR_MENU_SELECTOR =
	'[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"]';

// Native BrowserView pixels sit above the renderer, so only dialogs and
// explicitly marked browser-panel overlays should park that view. Generic
// menus elsewhere in the shell do not overlap the BrowserView and parking for
// every Radix menu causes the preview to flash on ordinary toolbar clicks.
// Radix tooltips never use `data-state="open"` — they report `delayed-open` or
// `instant-open` depending on whether the hover delay elapsed or focus opened
// them instantly. Browser-panel tooltips can also be explicitly marked as native
// overlays, so those two states have to be matched here as well or the shell is
// never raised and the tooltip renders behind the page.
export const OPEN_BROWSER_OVERLAY_SELECTOR =
	'[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [data-browser-native-overlay="true"][data-state="open"], [data-browser-native-overlay="true"][data-state="delayed-open"], [data-browser-native-overlay="true"][data-state="instant-open"]';

export function isDialogOrMenuOpen(): boolean {
	if (typeof document === "undefined") return false;
	return document.querySelector(OPEN_DIALOG_OR_MENU_SELECTOR) !== null;
}
