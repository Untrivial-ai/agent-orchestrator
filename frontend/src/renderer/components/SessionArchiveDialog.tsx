import type { ReactElement } from "react";
import { useTranslation } from "react-i18next";
import type { WorkspaceSession } from "../types/workspace";
import { ConfirmDialog } from "./ConfirmDialog";

// Archiving is reversible — the session moves to the board's Archive section and
// can be restored from there — so this reuses the shared confirm modal without
// the destructive red fill rather than the old inline popover, which read like a
// delete prompt. Callers keep driving `open` themselves: the trigger is rendered
// as-is so repeated taps re-open the confirm instead of toggling it shut.
export function SessionArchiveDialog({
	onConfirm,
	onOpenChange,
	open,
	session,
	trigger,
}: {
	onConfirm: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	session?: WorkspaceSession;
	trigger: ReactElement;
}) {
	const { t } = useTranslation();
	const title = session?.title;
	// A cloud session's teardown is restorable too — the control plane keeps its
	// conversation and work so it can be re-provisioned later.
	const isCloud = session?.cloud !== undefined;
	const body = isCloud
		? title
			? t("termination.bodyCloudNamed", { title })
			: t("termination.bodyCloud")
		: title
			? t("termination.bodyNamed", { title })
			: t("termination.body");
	return (
		<>
			{trigger}
			<ConfirmDialog
				cancelLabel={t("common.no")}
				confirmAriaLabel={t("termination.confirmAria")}
				confirmLabel={t("confirm.confirm")}
				description={body}
				onConfirm={onConfirm}
				onOpenChange={onOpenChange}
				open={open}
				title={title ? t("termination.dialogNamed", { title }) : t("termination.dialog")}
			/>
		</>
	);
}
