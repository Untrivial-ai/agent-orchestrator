import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";

type CreateReviewDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onSubmit: (input: { summary?: string; issues?: string }) => void;
	isPending: boolean;
	error?: string | null;
};

export function CreateReviewDialog({ open, onOpenChange, onSubmit, isPending, error }: CreateReviewDialogProps) {
	const { t } = useTranslation();
	const [summary, setSummary] = useState("");
	const [issues, setIssues] = useState("");

	const resetForm = () => {
		setSummary("");
		setIssues("");
	};

	const handleSubmit = () => {
		onSubmit({
			summary: summary.trim() || undefined,
			issues: issues.trim() || undefined,
		});
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) resetForm();
				onOpenChange(nextOpen);
			}}
		>
			<DialogContent data-testid="create-review-dialog">
				<DialogHeader>
					<DialogTitle>{t("workflow.review.createHumanReview")}</DialogTitle>
				</DialogHeader>
				<div className="flex flex-col gap-3">
					<p className="text-xs text-muted-foreground">
						{t("workflow.review.source")}: {t("workflow.review.sourceHuman")}
					</p>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="review-summary">{t("workflow.review.summary")}</Label>
						<Input
							id="review-summary"
							value={summary}
							onChange={(e) => setSummary(e.target.value)}
							placeholder={t("workflow.review.summaryPlaceholder")}
							autoFocus
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="review-issues">{t("workflow.review.issues")}</Label>
						<Input
							id="review-issues"
							value={issues}
							onChange={(e) => setIssues(e.target.value)}
							placeholder={t("workflow.review.issuesPlaceholder")}
						/>
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
						onClick={handleSubmit}
						disabled={isPending}
						data-testid="submit-create-review"
					>
						{isPending ? t("common.creating") : t("workflow.review.create")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
