import { create } from "zustand";

export const useCuesDialogStore = create<{
	open: boolean;
	openCuesDialog(): void;
	closeCuesDialog(): void;
}>()((set) => ({
	open: false,
	openCuesDialog: () => set({ open: true }),
	closeCuesDialog: () => set({ open: false }),
}));