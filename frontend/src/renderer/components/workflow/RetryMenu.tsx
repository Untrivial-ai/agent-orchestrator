import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";

type RetryMenuProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onSelect: (mode: "resume" | "fresh") => void;
	isPending: boolean;
};

export function RetryMenu({ open, onOpenChange, onSelect, isPending }: RetryMenuProps) {
	const { t } = useTranslation();
	const [selectedMode, setSelectedMode] = useState<"resume" | "fresh" | null>(null);

	const handleConfirm = () => {
		if (!selectedMode) return;
		onSelect(selectedMode);
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) setSelectedMode(null);
				onOpenChange(nextOpen);
			}}
		>
			<DialogContent data-testid="retry-menu">
				<DialogHeader>
					<DialogTitle>{t("workflow.review.retryTitle")}</DialogTitle>
				</DialogHeader>
				<div className="flex flex-col gap-2">
					<button
						type="button"
						className={`rounded-md border p-3 text-left text-sm transition-colors ${selectedMode === "resume" ? "border-primary bg-primary/5" : "hover:bg-muted"}`}
						onClick={() => setSelectedMode("resume")}
						data-testid="retry-resume"
					>
						<p className="font-medium">{t("workflow.review.retryResume")}</p>
						<p className="text-xs text-muted-foreground">{t("workflow.review.retryResumeDescription")}</p>
					</button>
					<button
						type="button"
						className={`rounded-md border p-3 text-left text-sm transition-colors ${selectedMode === "fresh" ? "border-primary bg-primary/5" : "hover:bg-muted"}`}
						onClick={() => setSelectedMode("fresh")}
						data-testid="retry-fresh"
					>
						<p className="font-medium">{t("workflow.review.retryFresh")}</p>
						<p className="text-xs text-muted-foreground">{t("workflow.review.retryFreshDescription")}</p>
					</button>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						{t("common.close")}
					</Button>
					<Button
						onClick={handleConfirm}
						disabled={!selectedMode || isPending}
						data-testid="confirm-retry"
					>
						{isPending ? t("workflow.review.creatingRetry") : t("workflow.review.createRetry")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
