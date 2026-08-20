import { useEffect, useRef, useState } from "react";

/** Wire-format simulator action accepted by the input endpoint. */
export type SimulatorAction =
	| { action: "tap" | "swipe"; x: number; y: number; x2?: number; y2?: number }
	| { action: "text"; text: string }
	| { action: "key"; keyCode: number }
	| { action: "home" | "lock" | "rotateLeft" | "rotateRight" };

// macOS virtual key codes for the keys the simulator delivers via Simulator.app.
const VK_RETURN = 36;
const VK_TAB = 48;
const VK_SPACE = 49;
const VK_DELETE = 51;
const VK_ARROW_LEFT = 123;
const VK_ARROW_RIGHT = 124;
const VK_ARROW_DOWN = 125;
const VK_ARROW_UP = 126;

/**
 * Physical-keyboard control for the simulator pane.
 *
 * The pane "arms" on the first pointer interaction inside it and disarms when
 * the window loses focus, the user presses Escape, or the pointer goes
 * elsewhere — exactly the focus-gating Anthropic uses for the Claude Code iOS
 * simulator. While armed:
 *   - ⌘⇧H → Home, ⌘L → Lock, ⌘←/⌘→ rotate (Simulator.app device shortcuts),
 *   - printable characters and Enter/Backspace/Space/Tab/arrows are forwarded
 *     to the simulator as text/key input, so a human can type into an app
 *     running in the pane as if it were on screen.
 *
 * Keystrokes aimed at AO's own text fields (inputs, textareas, contenteditable)
 * and any ⌘-prefixed system shortcut are never consumed.
 */
export function useSimulatorKeyboard(options: {
	active: boolean;
	booted: boolean;
	onInput: (input: SimulatorAction) => void;
}): { armed: boolean; containerRef: React.RefObject<HTMLDivElement | null> } {
	const [armed, setArmed] = useState(false);
	const containerRef = useRef<HTMLDivElement | null>(null);
	const optionsRef = useRef(options);
	optionsRef.current = options;
	const armedRef = useRef(false);
	useEffect(() => {
		armedRef.current = armed;
	}, [armed]);

	useEffect(() => {
		if (!options.active) {
			setArmed(false);
			return;
		}
		if (!options.booted) {
			setArmed(false);
			return;
		}

		const onPointerDown = (event: PointerEvent) => {
			const target = event.target as Node | null;
			const inside = containerRef.current?.contains(target) ?? false;
			setArmed(inside);
		};
		const onWindowBlur = () => setArmed(false);
		const onKeyDown = (event: KeyboardEvent) => {
			if (!optionsRef.current.booted || !armedRef.current) return;
			const target = event.target as HTMLElement | null;
			const wantsTyping =
				target?.isContentEditable ||
				target instanceof HTMLInputElement ||
				target instanceof HTMLTextAreaElement;
			const meta = event.metaKey || event.ctrlKey;

			if (meta && event.shiftKey && event.key.toLowerCase() === "h") {
				event.preventDefault();
				optionsRef.current.onInput({ action: "home" });
				return;
			}
			if (meta && event.key.toLowerCase() === "l") {
				event.preventDefault();
				optionsRef.current.onInput({ action: "lock" });
				return;
			}
			if (meta && event.key === "ArrowLeft") {
				event.preventDefault();
				optionsRef.current.onInput({ action: "rotateLeft" });
				return;
			}
			if (meta && event.key === "ArrowRight") {
				event.preventDefault();
				optionsRef.current.onInput({ action: "rotateRight" });
				return;
			}
			// Never hijack AO's own text entry or ⌘/⌃-system shortcuts.
			if (wantsTyping || meta) return;
			if (event.key === "Escape") {
				setArmed(false);
				return;
			}

			switch (event.key) {
				case "Enter":
					event.preventDefault();
					optionsRef.current.onInput({ action: "key", keyCode: VK_RETURN });
					return;
				case "Backspace":
					event.preventDefault();
					optionsRef.current.onInput({ action: "key", keyCode: VK_DELETE });
					return;
				case "Tab":
					event.preventDefault();
					optionsRef.current.onInput({ action: "key", keyCode: VK_TAB });
					return;
				case " ":
					event.preventDefault();
					optionsRef.current.onInput({ action: "key", keyCode: VK_SPACE });
					return;
				case "ArrowLeft":
				case "ArrowRight":
				case "ArrowUp":
				case "ArrowDown": {
					event.preventDefault();
					const code = {
						ArrowLeft: VK_ARROW_LEFT,
						ArrowRight: VK_ARROW_RIGHT,
						ArrowUp: VK_ARROW_UP,
						ArrowDown: VK_ARROW_DOWN,
					}[event.key];
					optionsRef.current.onInput({ action: "key", keyCode: code });
					return;
				}
				default:
					break;
			}

			if (event.key.length === 1) {
				// Single printable character (Auto Repeat included). Multi-char
				// compositions should go through the pane's own text field.
				event.preventDefault();
				optionsRef.current.onInput({ action: "text", text: event.key });
			}
		};

		document.addEventListener("pointerdown", onPointerDown, true);
		window.addEventListener("blur", onWindowBlur);
		window.addEventListener("keydown", onKeyDown);
		return () => {
			document.removeEventListener("pointerdown", onPointerDown, true);
			window.removeEventListener("blur", onWindowBlur);
			window.removeEventListener("keydown", onKeyDown);
		};
	}, [options.active, options.booted]);

	return { armed, containerRef };
}