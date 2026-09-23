import type { KeyboardEvent } from "react";

/**
 * Implements the arrow/Home/End behavior expected of a horizontal tablist.
 * Selection follows focus so terminal tabs remain fast to switch by keyboard.
 */
export function handleTabListKeyDown(event: KeyboardEvent<HTMLDivElement>) {
	if (!(event.target instanceof HTMLElement) || event.target.getAttribute("role") !== "tab") return;
	if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;

	const tabs = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]:not(:disabled)'));
	const currentIndex = tabs.indexOf(event.target as HTMLButtonElement);
	if (currentIndex === -1 || tabs.length === 0) return;

	event.preventDefault();
	const nextIndex =
		event.key === "Home"
			? 0
			: event.key === "End"
				? tabs.length - 1
				: (currentIndex + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
	tabs[nextIndex]?.focus();
	tabs[nextIndex]?.click();
}

export const handleTerminalTabListKeyDown = handleTabListKeyDown;
