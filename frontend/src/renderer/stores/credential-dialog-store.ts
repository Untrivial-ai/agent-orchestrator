import { create } from "zustand";

// Shared open-state for the cloud coding-agent credential dialog. Both the
// post-sign-in onboarding gate (auto-open when no valid connection) and the
// manual entry point in the sidebar account row drive the single mounted dialog
// through this store.
type CredentialDialogState = {
	open: boolean;
	openDialog: () => void;
	closeDialog: () => void;
	setOpen: (open: boolean) => void;
};

export const useCredentialDialogStore = create<CredentialDialogState>((set) => ({
	open: false,
	openDialog: () => set({ open: true }),
	closeDialog: () => set({ open: false }),
	setOpen: (open) => set({ open }),
}));
