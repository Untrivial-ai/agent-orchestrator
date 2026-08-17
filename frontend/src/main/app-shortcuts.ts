import {
	CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL,
	FOCUS_TERMINAL_SHORTCUT_CHANNEL,
	KEYBOARD_SHORTCUTS_HELP_CHANNEL,
	matchesAppShortcut,
	NEXT_SESSION_SHORTCUT_CHANNEL,
	NEXT_TAB_SHORTCUT_CHANNEL,
	NEW_SESSION_SHORTCUT_CHANNEL,
	NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL,
	OPEN_SETTINGS_SHORTCUT_CHANNEL,
	PREVIOUS_SESSION_SHORTCUT_CHANNEL,
	PREVIOUS_TAB_SHORTCUT_CHANNEL,
	terminalFontSizeDelta,
	TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL,
	type AppShortcutId,
	type KeybindingOverrides,
	type ShortcutChord,
} from "../shared/shortcuts";

// The slice of Electron's Input we read, plus the emitter shape. Declared
// locally so tests can supply a plain fake while WebContents still satisfies it.
type BeforeInput = {
	key: string;
	// Physical key (layout-independent), needed for chords whose character
	// shifts, e.g. Ctrl+Shift+` reports key "~" but code "Backquote". Optional so
	// test doubles need not supply it; Electron always does at runtime.
	code?: string;
	control: boolean;
	meta: boolean;
	shift: boolean;
	alt: boolean;
	type: string;
	isAutoRepeat?: boolean;
};

type BeforeInputContents = {
	on(
		event: "before-input-event",
		listener: (event: { preventDefault: () => void }, input: BeforeInput) => void,
	): unknown;
};

type ShortcutTargetContents = {
	focus: () => void;
	send: (channel: string, ...args: unknown[]) => void;
};

const mainShortcutChannels: readonly [AppShortcutId, string][] = [
	["new-session", NEW_SESSION_SHORTCUT_CHANNEL],
	["new-shell-terminal", NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL],
	["close-shell-terminal", CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL],
	["keyboard-shortcuts", KEYBOARD_SHORTCUTS_HELP_CHANNEL],
	["open-settings", OPEN_SETTINGS_SHORTCUT_CHANNEL],
	["previous-session", PREVIOUS_SESSION_SHORTCUT_CHANNEL],
	["next-session", NEXT_SESSION_SHORTCUT_CHANNEL],
	["previous-tab", PREVIOUS_TAB_SHORTCUT_CHANNEL],
	["next-tab", NEXT_TAB_SHORTCUT_CHANNEL],
	["focus-terminal", FOCUS_TERMINAL_SHORTCUT_CHANNEL],
];

const appShortcutChannel = (
	chord: ShortcutChord,
	isMac: boolean,
	overrides: KeybindingOverrides,
): readonly [AppShortcutId, string] | null => {
	for (const [id, channel] of mainShortcutChannels) {
		if (matchesAppShortcut(id, chord, isMac, overrides)) return [id, channel];
	}
	return null;
};

// Handle application-owned shortcuts in the main process so they work no
// matter which web contents holds focus, including xterm's helper textarea and
// the native Browser-preview WebContentsView.
export function attachAppShortcuts(
	contents: BeforeInputContents,
	isMac: boolean,
	target: ShortcutTargetContents,
	focusTarget = false,
	getOverrides: () => KeybindingOverrides = () => ({}),
	isRecording: () => boolean = () => false,
	shouldHandle: (id: AppShortcutId) => boolean = () => true,
	onShortcut?: (id: AppShortcutId) => void,
	isTerminalFocused: () => boolean = () => false,
): void {
	contents.on("before-input-event", (event, input) => {
		if (input.type !== "keyDown") return;
		const chord = {
			key: input.key,
			code: input.code,
			ctrl: input.control,
			meta: input.meta,
			shift: input.shift,
			alt: input.alt,
		};
		const fontSizeDelta = terminalFontSizeDelta(chord, isMac);
		if (fontSizeDelta !== 0 && isTerminalFocused()) {
			event.preventDefault();
			if (focusTarget) target.focus();
			target.send(TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL, fontSizeDelta);
			return;
		}
		if (input.isAutoRepeat) return;
		// Let the renderer's capture listener receive application-owned chords
		// while the user is recording a replacement binding.
		if (isRecording()) return;
		if (onShortcut && matchesAppShortcut("toggle-browser-devtools", chord, isMac, getOverrides())) {
			event.preventDefault();
			if (focusTarget) target.focus();
			onShortcut("toggle-browser-devtools");
			return;
		}
		const match = appShortcutChannel(chord, isMac, getOverrides());
		if (!match) return;
		const [id, channel] = match;
		if (!shouldHandle(id)) return;

		event.preventDefault();
		if (focusTarget) target.focus();
		target.send(channel);
	});
}
