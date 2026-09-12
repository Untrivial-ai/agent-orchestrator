import { create } from "zustand";

// Shared open-state for the dev-only local (email/password) cloud sign-in
// dialog. Both sidebar entry points (the expanded row and the collapsed rail
// button) drive the single mounted dialog through this store, mirroring
// credential-dialog-store.
type LocalSignInDialogState = {
	open: boolean;
	openDialog: () => void;
	closeDialog: () => void;
	setOpen: (open: boolean) => void;
};

export const useLocalSignInDialogStore = create<LocalSignInDialogState>((set) => ({
	open: false,
	openDialog: () => set({ open: true }),
	closeDialog: () => set({ open: false }),
	setOpen: (open) => set({ open }),
}));
