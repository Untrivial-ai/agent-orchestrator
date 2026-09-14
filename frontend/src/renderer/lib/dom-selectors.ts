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

const BROWSER_OVERLAY_CANDIDATE_SELECTOR =
	'[role="dialog"], [role="alertdialog"], [data-browser-native-overlay="true"]';

function containsBrowserOverlayCandidate(node: Node): boolean {
	if (!(node instanceof Element)) return false;
	return node.matches(BROWSER_OVERLAY_CANDIDATE_SELECTOR) || node.querySelector(BROWSER_OVERLAY_CANDIDATE_SELECTOR) !== null;
}

/**
 * MutationObserver is still needed for portaled Radix content whose open state
 * is owned inside the primitive. Filter its records before doing the one global
 * open-overlay lookup: unrelated accordions, switches, menus, and other
 * data-state churn must not make the browser compositor do work.
 */
export function hasRelevantBrowserOverlayMutation(records: MutationRecord[]): boolean {
	return records.some((record) => {
		if (record.type === "attributes") return containsBrowserOverlayCandidate(record.target);
		return [...record.addedNodes, ...record.removedNodes].some(containsBrowserOverlayCandidate);
	});
}

export function isDialogOrMenuOpen(): boolean {
	if (typeof document === "undefined") return false;
	return document.querySelector(OPEN_DIALOG_OR_MENU_SELECTOR) !== null;
}
