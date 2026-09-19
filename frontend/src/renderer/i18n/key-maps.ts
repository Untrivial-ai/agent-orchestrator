import type { AppShortcutId, ShortcutCategory } from "../../shared/shortcuts";
import type { MessageKey } from "./messages";

/** Exhaustive mappings keep dynamic domain identifiers inside the typed catalog. */
export const shortcutLabelKeys: Record<AppShortcutId, MessageKey> = {
	"new-session": "shortcut.new-session",
	"new-shell-terminal": "shortcut.new-shell-terminal",
	"close-shell-terminal": "shortcut.close-shell-terminal",
	"keyboard-shortcuts": "shortcut.keyboard-shortcuts",
	"command-palette": "shortcut.command-palette",
	"open-settings": "shortcut.open-settings",
	"toggle-sidebar": "shortcut.toggle-sidebar",
	"open-project": "shortcut.open-project",
	"previous-session": "shortcut.previous-session",
	"next-session": "shortcut.next-session",
	"previous-tab": "shortcut.previous-tab",
	"next-tab": "shortcut.next-tab",
	"toggle-inspector": "shortcut.toggle-inspector",
	"focus-terminal": "shortcut.focus-terminal",
	"toggle-browser-devtools": "titlebar.devtools",
};

export const shortcutCategoryLabelKeys: Record<ShortcutCategory, MessageKey> = {
	General: "shortcut.category.general",
	Navigation: "shortcut.category.navigation",
	Session: "shortcut.category.session",
};

