import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";

type RejectDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onSubmit: (issues: string) => void;
	isPending: boolean;
	error?: string | null;
};

export function RejectDialog({ open, onOpenChange, onSubmit, isPending, error }: RejectDialogProps) {
	const { t } = useTranslation();
	const [issues, setIssues] = useState("");

	const resetForm = () => {
		setIssues("");
	};

	const trimmedIssues = issues.trim();
	const canSubmit = trimmedIssues.length > 0 && !isPending;

	const handleSubmit = () => {
		if (!canSubmit) return;
		onSubmit(trimmedIssues);
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) resetForm();
				onOpenChange(nextOpen);
			}}
		>
			<DialogContent data-testid="reject-review-dialog">
				<DialogHeader>
					<DialogTitle>{t("workflow.review.reject")}</DialogTitle>
				</DialogHeader>
				<div className="flex flex-col gap-3">
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="reject-issues">{t("workflow.review.rejectIssues")} *</Label>
						<Input
							id="reject-issues"
							value={issues}
							onChange={(e) => setIssues(e.target.value)}
							placeholder={t("workflow.review.rejectIssuesPlaceholder")}
							autoFocus
						/>
						{issues.length > 0 && !trimmedIssues && (
							<p className="text-xs text-destructive">{t("workflow.review.issuesRequired")}</p>
						)}
					</div>
				</div>
				{error && (
					<p className="text-sm text-destructive">{error}</p>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						{t("common.close")}
					</Button>
					<Button
						variant="secondary"
						className="text-destructive"
						onClick={handleSubmit}
						disabled={!canSubmit}
						data-testid="submit-reject-review"
					>
						{isPending ? t("workflow.review.rejecting") : t("workflow.review.reject")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
