import { describe, expect, it, vi } from "vitest";
import { CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, FOCUS_TERMINAL_SHORTCUT_CHANNEL, KEYBOARD_SHORTCUTS_HELP_CHANNEL, NEXT_SESSION_SHORTCUT_CHANNEL, NEXT_TAB_SHORTCUT_CHANNEL, NEW_SESSION_SHORTCUT_CHANNEL, NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, OPEN_SETTINGS_SHORTCUT_CHANNEL, PREVIOUS_SESSION_SHORTCUT_CHANNEL, PREVIOUS_TAB_SHORTCUT_CHANNEL, TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL } from "../shared/shortcuts";
import { attachAppShortcuts } from "./app-shortcuts";

type InputEvent = {
	key: string;
	// Physical key (Electron input.code), needed for chords whose character is
	// layout-shifted, e.g. Ctrl+Shift+` reports key "~" but code "Backquote".
	code?: string;
	control: boolean;
	meta: boolean;
	shift: boolean;
	alt: boolean;
	type: "keyDown" | "keyUp";
	isAutoRepeat?: boolean;
};

function fakeSource() {
	let handler: ((event: { preventDefault: () => void }, input: InputEvent) => void) | undefined;
	return {
		on(channel: string, listener: typeof handler) {
			if (channel === "before-input-event") handler = listener;
			return this;
		},
		emit(input: Partial<InputEvent> & { key: string }) {
			const event = { preventDefault: vi.fn() };
			handler?.(event, {
				control: false,
				meta: false,
				shift: false,
				alt: false,
				type: "keyDown",
				...input,
			});
			return event;
		},
	};
}

function fakeTarget() {
	return { focus: vi.fn(), send: vi.fn() };
}

describe("attachAppShortcuts", () => {
	it("forwards and prevents default on the main-window chord", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		const event = source.emit({ key: "N", control: true, shift: true });

		expect(target.send).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL);
		expect(target.focus).not.toHaveBeenCalled();
		expect(event.preventDefault).toHaveBeenCalledTimes(1);
	});

	it("forwards the macOS command chord", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, true, target);

		source.emit({ key: "n", meta: true });

		expect(target.send).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL);
	});

	it("focuses a separate shell target before forwarding", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target, true);

		source.emit({ key: "N", control: true, shift: true });

		expect(target.focus).toHaveBeenCalledTimes(1);
		expect(target.send).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL);
		expect(target.focus.mock.invocationCallOrder[0]).toBeLessThan(target.send.mock.invocationCallOrder[0]);
	});

	it("ignores non-matching chords and key-up events", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		source.emit({ key: "n", control: true });
		source.emit({ key: "N", control: true, shift: true, type: "keyUp" });
		source.emit({ key: "a", control: true, shift: true });

		expect(target.send).not.toHaveBeenCalled();
	});

	it("ignores auto-repeat so holding the combo fires once", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		source.emit({ key: "N", control: true, shift: true });
		source.emit({ key: "N", control: true, shift: true, isAutoRepeat: true });
		source.emit({ key: "N", control: true, shift: true, isAutoRepeat: true });

		expect(target.send).toHaveBeenCalledTimes(1);
	});

	it.each([
		["macOS", true, { key: "t", meta: true }],
		["Windows/Linux", false, { key: "t", control: true }],
	])("forwards the new-shell-terminal chord on %s", (_name, isMac, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, isMac, target);

		source.emit(input);

		expect(target.send).toHaveBeenCalledWith(NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL);
	});

	it.each([
		["macOS", true, { key: "w", meta: true }],
		["Windows/Linux", false, { key: "w", control: true }],
	])("forwards and consumes the close-shell-terminal chord on %s", (_name, isMac, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, isMac, target);

		const event = source.emit(input);

		expect(target.send).toHaveBeenCalledWith(CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL);
		expect(event.preventDefault).toHaveBeenCalledOnce();
	});

	it("preserves the close chord when no shell terminal is closeable", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target, false, () => ({}), () => false, (id) => id !== "close-shell-terminal");

		const event = source.emit({ key: "w", control: true });

		expect(target.send).not.toHaveBeenCalled();
		expect(event.preventDefault).not.toHaveBeenCalled();
	});

	it("forwards keyboard-shortcut help on each platform", () => {
		const windowsSource = fakeSource();
		const windowsTarget = fakeTarget();
		attachAppShortcuts(windowsSource, false, windowsTarget);
		windowsSource.emit({ key: "/", control: true });

		const macSource = fakeSource();
		const macTarget = fakeTarget();
		attachAppShortcuts(macSource, true, macTarget);
		macSource.emit({ key: "/", meta: true });

		expect(windowsTarget.send).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
		expect(macTarget.send).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
	});

	it.each([
		["settings", { key: ",", control: true }, OPEN_SETTINGS_SHORTCUT_CHANNEL],
		["previous session", { key: "PageUp", control: true }, PREVIOUS_SESSION_SHORTCUT_CHANNEL],
		["next session", { key: "PageDown", control: true }, NEXT_SESSION_SHORTCUT_CHANNEL],
		["previous tab", { key: "Tab", control: true, shift: true }, PREVIOUS_TAB_SHORTCUT_CHANNEL],
		["next tab", { key: "Tab", control: true }, NEXT_TAB_SHORTCUT_CHANNEL],
		["focus terminal", { key: "T", control: true, shift: true }, FOCUS_TERMINAL_SHORTCUT_CHANNEL],
	] as const)("forwards the Windows/Linux %s shortcut", (_label, input, channel) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		const event = source.emit(input);

		expect(target.send).toHaveBeenCalledWith(channel);
		expect(event.preventDefault).toHaveBeenCalledTimes(1);
	});

	it("reads live user overrides without reattaching the listener", () => {
		const source = fakeSource();
		const target = fakeTarget();
		let overrides = {};
		attachAppShortcuts(source, false, target, false, () => overrides);

		source.emit({ key: "T", control: true, shift: true });
		overrides = {
			"focus-terminal": [
				{ key: "j", ctrl: true, meta: false, shift: false, alt: false },
			],
		};
		source.emit({ key: "T", control: true, shift: true });
		source.emit({ key: "j", control: true });

		expect(target.send).toHaveBeenCalledTimes(2);
		expect(target.send).toHaveBeenLastCalledWith(FOCUS_TERMINAL_SHORTCUT_CHANNEL);
	});

	it("does not intercept application shortcuts while a binding is being recorded", () => {
		const source = fakeSource();
		const target = fakeTarget();
		let recording = true;
		attachAppShortcuts(source, false, target, false, () => ({}), () => recording);

		const recordingEvent = source.emit({ key: "/", control: true });
		recording = false;
		const activeEvent = source.emit({ key: "/", control: true });

		expect(recordingEvent.preventDefault).not.toHaveBeenCalled();
		expect(activeEvent.preventDefault).toHaveBeenCalledTimes(1);
		expect(target.send).toHaveBeenCalledTimes(1);
		expect(target.send).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
	});

	it.each([
		["macOS", true, { key: "=", code: "Equal", meta: true }],
		["Windows/Linux", false, { key: "+", code: "Equal", control: true }],
	] as const)("routes primary-modifier plus/minus to the focused terminal on %s and preserves app zoom otherwise", (_platform, isMac, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		let terminalFocused = true;
		attachAppShortcuts(
			source,
			isMac,
			target,
			false,
			() => ({}),
			() => false,
			() => true,
			undefined,
			() => terminalFocused,
		);

		const focusedEvent = source.emit(input);

		expect(focusedEvent.preventDefault).toHaveBeenCalledOnce();
		expect(target.send).toHaveBeenCalledWith(TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL, 1);

		target.send.mockClear();
		terminalFocused = false;
		const appZoomEvent = source.emit(
			isMac
				? { key: "-", code: "Minus", meta: true }
				: { key: "-", code: "Minus", control: true },
		);

		expect(appZoomEvent.preventDefault).not.toHaveBeenCalled();
		expect(target.send).not.toHaveBeenCalled();
	});
});
