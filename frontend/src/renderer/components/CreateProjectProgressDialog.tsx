import * as Dialog from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { Button } from "./ui/button";

export default function CreateProjectProgressDialog({
	message,
	onCancel,
	open,
	progress,
}: {
	message: string;
	onCancel: () => void;
	open: boolean;
	progress: number;
}) {
	const { t } = useTranslation();

	return (
		<Dialog.Root open={open} onOpenChange={(next) => !next && onCancel()}>
			<Dialog.Portal>
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(440px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					<div className="px-5 pb-5 pt-5">
						<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
							{t("createProject.cloneProgressTitle", { defaultValue: "Creating the project" })}
						</Dialog.Title>
						<Dialog.Description className="sr-only">
							{t("createProject.cloneProgressDescription", { defaultValue: "Creating the project" })}
						</Dialog.Description>
						<div className="mt-6 space-y-3">
							<div aria-label={`${Math.round(progress)}%`} aria-valuemax={100} aria-valuemin={0} aria-valuenow={Math.round(progress)} className="h-2 w-full overflow-hidden rounded-full bg-muted" role="progressbar">
								<div className="h-full rounded-full bg-primary transition-[width] duration-300 ease-out" style={{ width: `${Math.max(0, Math.min(100, progress))}%` }} />
							</div>
							<p className="min-h-5 text-[13px] text-muted-foreground" role="status">{message}</p>
						</div>
						<div className="mt-6 flex justify-end">
							<Button type="button" variant="outline" onClick={onCancel}>{t("createProject.cancel")}</Button>
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
