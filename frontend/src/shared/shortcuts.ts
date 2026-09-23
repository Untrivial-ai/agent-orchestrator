// Pure, platform-parameterized shortcut matchers shared by the main process
// (Electron `before-input-event`) and any renderer code. Kept free of Electron
// and DOM types so it is trivially unit-testable and usable on both sides.

export type ShortcutChord = {
	key: string;
	// Physical key (KeyboardEvent.code / Electron input.code), independent of
	// layout and modifiers. Needed for chords whose character shifts — e.g.
	// Ctrl+Shift+` reports key "~" on a US layout but code "Backquote".
	code?: string;
	ctrl: boolean;
	meta: boolean;
	shift: boolean;
	alt: boolean;
};

export const SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL =
	"app:set-close-shell-terminal-shortcut-enabled";
export const SET_TERMINAL_FOCUSED_CHANNEL = "terminal:set-focused";
export const TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL = "terminal:font-size-shortcut";

export function terminalFontSizeDelta(chord: ShortcutChord, isMac: boolean): -1 | 0 | 1 {
	const hasPrimaryModifier = isMac
		? chord.meta && !chord.ctrl
		: chord.ctrl && !chord.meta;
	if (!hasPrimaryModifier || chord.alt) return 0;
	if (chord.key === "+" || chord.key === "=" || chord.code === "NumpadAdd") return 1;
	if (chord.key === "-" || chord.code === "NumpadSubtract") return -1;
	return 0;
}

export type AppShortcutId =
	"new-session" | "new-shell-terminal" | "close-shell-terminal" | "keyboard-shortcuts" | "toggle-sidebar" | "open-project" | "toggle-inspector" | "command-palette" | "open-settings" | "previous-session" | "next-session" | "previous-tab" | "next-tab" | "focus-terminal" | "toggle-browser-devtools";

export type ShortcutCategory = "General" | "Navigation" | "Session";

export type ShortcutBinding = {
	key: string;
	code?: string;
	ctrl: boolean;
	meta: boolean;
	shift: boolean;
	alt: boolean;
};

export type KeybindingOverrides = Partial<Record<AppShortcutId, readonly ShortcutBinding[]>>;

export type ShortcutDefinition = {
	id: AppShortcutId;
	label: string;
	category: ShortcutCategory;
	/** Indexed project selection is a family of nine bindings, not one command. */
	customizable?: boolean;
};

export const SHORTCUT_CATEGORIES: readonly ShortcutCategory[] = ["General", "Navigation", "Session"];

// The user-facing shortcut catalog. Bindings live exclusively in
// defaultShortcutBindings so runtime matching and displayed labels cannot drift.
export const APP_SHORTCUTS: readonly ShortcutDefinition[] = [
	{
		id: "new-session",
		label: "New session",
		category: "General",
	},
	{
		id: "new-shell-terminal",
		label: "New terminal",
		category: "General",
	},
	{
		id: "close-shell-terminal",
		label: "Close terminal",
		category: "Session",
	},
	{
		id: "keyboard-shortcuts",
		label: "Show keyboard shortcuts",
		category: "General",
	},
	{
		id: "command-palette",
		label: "Open command palette",
		category: "General",
	},
	{
		id: "open-settings",
		label: "Open settings",
		category: "General",
	},
	{
		id: "toggle-sidebar",
		label: "Toggle sidebar",
		category: "General",
	},
	{
		id: "open-project",
		label: "Open project 1–9",
		category: "Navigation",
		customizable: false,
	},
	{
		id: "previous-session",
		label: "Previous session",
		category: "Navigation",
	},
	{
		id: "next-session",
		label: "Next session",
		category: "Navigation",
	},
	{
		id: "previous-tab",
		label: "Previous tab",
		category: "Navigation",
	},
	{
		id: "next-tab",
		label: "Next tab",
		category: "Navigation",
	},
	{
		id: "toggle-inspector",
		label: "Toggle inspector",
		category: "Session",
	},
	{
		id: "focus-terminal",
		label: "Focus terminal",
		category: "Session",
	},
	{
		id: "toggle-browser-devtools",
		label: "Toggle browser DevTools",
		category: "Session",
	},
];

const binding = (
	key: string,
	modifiers: Partial<Pick<ShortcutBinding, "ctrl" | "meta" | "shift" | "alt" | "code">>,
): ShortcutBinding => ({
	key,
	ctrl: false,
	meta: false,
	shift: false,
	alt: false,
	...modifiers,
});

export function defaultShortcutBindings(id: AppShortcutId, isMac: boolean): readonly ShortcutBinding[] {
	switch (id) {
		case "new-session":
			return [isMac ? binding("n", { meta: true }) : binding("n", { ctrl: true, shift: true })];
		case "new-shell-terminal":
			return [isMac ? binding("t", { meta: true }) : binding("t", { ctrl: true })];
		case "close-shell-terminal":
			return [isMac ? binding("w", { meta: true }) : binding("w", { ctrl: true })];
		case "keyboard-shortcuts":
			return [isMac ? binding("/", { meta: true }) : binding("/", { ctrl: true })];
		case "toggle-sidebar":
			return [isMac ? binding("b", { meta: true }) : binding("b", { ctrl: true })];
		case "open-project":
			return [isMac ? binding("1-9", { meta: true }) : binding("1-9", { ctrl: true })];
		case "toggle-inspector":
			return [isMac ? binding("b", { meta: true, shift: true }) : binding("b", { ctrl: true, shift: true })];
		case "command-palette":
			return [isMac ? binding("k", { meta: true }) : binding("k", { ctrl: true })];
		case "open-settings":
			return [isMac ? binding(",", { meta: true }) : binding(",", { ctrl: true })];
		case "previous-session":
			return [isMac ? binding("ArrowUp", { meta: true, alt: true }) : binding("PageUp", { ctrl: true })];
		case "next-session":
			return [isMac ? binding("ArrowDown", { meta: true, alt: true }) : binding("PageDown", { ctrl: true })];
		case "previous-tab":
			return [binding("Tab", { ctrl: true, shift: true })];
		case "next-tab":
			return [binding("Tab", { ctrl: true })];
		case "focus-terminal":
			return [isMac ? binding("t", { meta: true, shift: true }) : binding("t", { ctrl: true, shift: true })];
		case "toggle-browser-devtools":
			return [isMac ? binding("i", { meta: true, alt: true }) : binding("i", { ctrl: true, shift: true })];
	}
}

export function effectiveShortcutBindings(
	id: AppShortcutId,
	isMac: boolean,
	overrides: KeybindingOverrides = {},
): readonly ShortcutBinding[] {
	return overrides[id] ?? defaultShortcutBindings(id, isMac);
}

function normalizedKey(key: string): string {
	if (key === "Up") return "arrowup";
	if (key === "Down") return "arrowdown";
	return key.toLowerCase();
}

export function matchesShortcutBinding(chord: ShortcutChord, candidate: ShortcutBinding): boolean {
	const keyMatches =
		candidate.key === "1-9"
			? /^[1-9]$/.test(chord.key)
			: normalizedKey(candidate.key) === normalizedKey(chord.key);
	const codeMatches =
		candidate.code !== undefined &&
		(chord.code !== undefined
			? normalizedKey(candidate.code) === normalizedKey(chord.code)
			: normalizedKey(candidate.code) === normalizedKey(chord.key));
	return (
		(keyMatches || codeMatches) &&
		chord.ctrl === candidate.ctrl &&
		chord.meta === candidate.meta &&
		chord.shift === candidate.shift &&
		chord.alt === candidate.alt
	);
}

/**
 * Application shortcuts are intercepted before the focused terminal or text
 * field sees them. Reject bindings that would consume ordinary input, terminal
 * control sequences, or common OS editing/window commands.
 */
export function shortcutBindingValidationError(binding: ShortcutBinding, isMac: boolean): string | null {
	if (
		["alt", "altgraph", "capslock", "control", "dead", "meta", "numlock", "process", "scrolllock", "shift", "unidentified"].includes(
			normalizedKey(binding.key),
		)
	) {
		return "Press a non-modifier key with Ctrl, Alt, or Cmd.";
	}
	if (!binding.ctrl && !binding.meta && !binding.alt) {
		return "Use Ctrl, Alt, or Cmd with another key.";
	}

	const key = normalizedKey(binding.key);
	if (binding.ctrl && !binding.meta && !binding.alt) {
		if (key === "c" || key === "v") {
			return "That shortcut is reserved for terminal copy, paste, or interrupt.";
		}
		if (!binding.shift && ["d", "z", "\\", "s", "q"].includes(key)) {
			return "That shortcut is reserved for terminal control.";
		}
	}

	if (isMac && binding.meta && !binding.ctrl && !binding.alt) {
		if (["a", "c", "h", "m", "q", "s", "v", "w", "x", "z"].includes(key)) {
			return "That shortcut is reserved by macOS or standard editing commands.";
		}
	}

	if (!isMac && binding.alt && !binding.ctrl && !binding.meta && key === "f4") {
		return "That shortcut is reserved for closing the window.";
	}

	return null;
}

export function matchesAppShortcut(
	id: AppShortcutId,
	chord: ShortcutChord,
	isMac: boolean,
	overrides: KeybindingOverrides = {},
): boolean {
	return effectiveShortcutBindings(id, isMac, overrides).some((candidate) => matchesShortcutBinding(chord, candidate));
}

export function shortcutBindingKeys(binding: ShortcutBinding, isMac: boolean): readonly string[] {
	const keys: string[] = [];
	if (binding.ctrl) keys.push("Ctrl");
	if (binding.meta) keys.push(isMac ? "⌘" : "Meta");
	if (binding.alt) keys.push(isMac ? "⌥" : "Alt");
	if (binding.shift) keys.push("Shift");
	const labels: Record<string, string> = {
		ArrowUp: "↑",
		ArrowDown: "↓",
		PageUp: "PageUp",
		PageDown: "PageDown",
		Backquote: "`",
	};
	keys.push(labels[binding.code ?? binding.key] ?? binding.key.toUpperCase());
	return keys;
}

export function shortcutBindingLabel(binding: ShortcutBinding, isMac: boolean): string {
	return shortcutBindingKeys(binding, isMac).join(isMac ? "" : "+");
}

// IPC channel the main process uses to tell the renderer shell to open the New
// Task flow. Lives here (not in main/) so the main process, preload, and
// renderer can all reference one constant without crossing bundle boundaries.
export const NEW_SESSION_SHORTCUT_CHANNEL = "app:new-session";
export const KEYBOARD_SHORTCUTS_HELP_CHANNEL = "app:keyboard-shortcuts-help";
export const NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL = "app:new-shell-terminal";
export const CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL = "app:close-shell-terminal";
export const OPEN_SETTINGS_SHORTCUT_CHANNEL = "app:open-settings";
export const PREVIOUS_SESSION_SHORTCUT_CHANNEL = "app:previous-session";
export const NEXT_SESSION_SHORTCUT_CHANNEL = "app:next-session";
export const PREVIOUS_TAB_SHORTCUT_CHANNEL = "app:previous-tab";
export const NEXT_TAB_SHORTCUT_CHANNEL = "app:next-tab";
export const FOCUS_TERMINAL_SHORTCUT_CHANNEL = "app:focus-terminal";

// New session: ⌘N on macOS, Ctrl+Shift+N on Windows/Linux. Plain Ctrl+N is a
// live terminal keystroke (readline/vim "next line"), so the non-mac binding
// adds Shift to stay clear of the shell. Handled at the application level
// (main-process before-input-event) so it fires even when focus is inside
// xterm's helper textarea or a native Browser-preview WebContentsView.
export function matchesNewSessionShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("new-session", chord, isMac);
}

// Terminal tabs follow the desktop tab convention: ⌘T on macOS and Ctrl+T on
// Windows/Linux. Handled in the main process so xterm cannot swallow it.
export function matchesNewShellTerminalShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("new-shell-terminal", chord, isMac);
}

// Keyboard shortcut help: ⌘/ on macOS, Ctrl+/ on Windows/Linux. This is also
// handled at the application level so the terminal and Browser preview cannot
// swallow the command before the shell sees it.
export function matchesKeyboardShortcutsHelpShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("keyboard-shortcuts", chord, isMac);
}

export function matchesOpenSettingsShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("open-settings", chord, isMac);
}

export function matchesPreviousSessionShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("previous-session", chord, isMac);
}

export function matchesNextSessionShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("next-session", chord, isMac);
}

export function matchesPreviousTabShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("previous-tab", chord, isMac);
}

export function matchesNextTabShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("next-tab", chord, isMac);
}

export function matchesFocusTerminalShortcut(chord: ShortcutChord, isMac: boolean): boolean {
	return matchesAppShortcut("focus-terminal", chord, isMac);
}
