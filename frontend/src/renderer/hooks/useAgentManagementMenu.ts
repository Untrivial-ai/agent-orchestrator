import { useRef } from "react";
import { useUiStore } from "../stores/ui-store";

/** Open Settings after the menu restores its trigger, so stacked dialogs retain focus. */
export function useAgentManagementMenu(focusAgentId?: string) {
	const triggerRef = useRef<HTMLButtonElement>(null);
	const pending = useRef(false);
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	return {
		triggerRef,
		requestManagement: () => { pending.current = true; },
		onCloseAutoFocus: (event: Event) => {
			if (!pending.current) return;
			pending.current = false;
			event.preventDefault();
			triggerRef.current?.focus({ preventScroll: true });
			openGlobalSettings("harness", { focusAgentId, preserveProject: true });
		},
	};
}
