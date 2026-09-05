import * as Dialog from "@radix-ui/react-dialog";

export function ModalBackdrop() {
	return <Dialog.Overlay className="dialog-overlay z-[calc(var(--z-overlay)-1)] data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />;
}
