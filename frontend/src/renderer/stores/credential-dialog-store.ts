import { create } from "zustand";

// Shared open-state for the cloud coding-agent credential dialog. Both the
// post-sign-in onboarding gate (auto-open when no valid connection) and the
// manual entry point in the sidebar account row drive the single mounted dialog
// through this store.
type CredentialDialogState = {
	open: boolean;
	// When opened from a specific harness row, the dialog scopes to that agent
	// (pre-selected and locked) instead of the generic picker. null = generic.
	targetAgent: string | null;
	openDialog: (agent?: string) => void;
	closeDialog: () => void;
	setOpen: (open: boolean, agent?: string) => void;
};

export const useCredentialDialogStore = create<CredentialDialogState>((set) => ({
	open: false,
	targetAgent: null,
	openDialog: (agent) => set({ open: true, targetAgent: agent ?? null }),
	closeDialog: () => set({ open: false, targetAgent: null }),
	setOpen: (open, agent) => set({ open, targetAgent: open ? agent ?? null : null }),
}));
