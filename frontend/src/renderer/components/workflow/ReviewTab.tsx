import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { StatusBadge } from "./StatusBadge";
import { useReviewsByRun, useCreateRunReview, usePassReview, useRejectReview } from "../../hooks/useWorkflowReviews";
import { useCreateRetryRun } from "../../hooks/useWorkflowRuns";
import { CreateReviewDialog } from "./CreateReviewDialog";
import { RejectDialog } from "./RejectDialog";
import { RetryMenu } from "./RetryMenu";

type TaskView = components["schemas"]["TaskView"];
type RunView = components["schemas"]["ControllersRunView"];
type RunReviewView = components["schemas"]["RunReviewView"];

type ReviewTabProps = {
	task: TaskView;
	latestRun: RunView | null;
	taskId: string;
};

export function ReviewTab({ task, latestRun, taskId }: ReviewTabProps) {
	const { t } = useTranslation();
	const reviewsQuery = useReviewsByRun(latestRun?.id ?? null);
	const reviews = reviewsQuery.data ?? [];
	const review: RunReviewView | null = reviews[0] ?? null;

	const createReview = useCreateRunReview(latestRun?.id ?? "", taskId);
	const passReview = usePassReview(review?.id ?? "", latestRun?.id ?? "", taskId);
	const rejectReview = useRejectReview(review?.id ?? "", latestRun?.id ?? "", taskId);
	const createRetry = useCreateRetryRun(latestRun?.id ?? "", taskId);

	const [showCreateDialog, setShowCreateDialog] = useState(false);
	const [showRejectDialog, setShowRejectDialog] = useState(false);
	const [showRetryMenu, setShowRetryMenu] = useState(false);

	// Scenario A: Run not yet finished
	if (!latestRun || latestRun.status === "running" || latestRun.status === "pending") {
		return (
			<div className="flex flex-col gap-2 py-4 text-center">
				<p className="text-xs text-muted-foreground">{t("workflow.review.waitingExecution")}</p>
			</div>
		);
	}

	// Scenario B: Run failed
	if (latestRun.status === "failed") {
		return (
			<div className="flex flex-col gap-2 py-4 text-center">
				<p className="text-xs text-muted-foreground">{t("workflow.review.failedNoReview")}</p>
			</div>
		);
	}

	// Scenario B2: Run cancelled
	if (latestRun.status === "cancelled") {
		return (
			<div className="flex flex-col gap-2 py-4 text-center">
				<p className="text-xs text-muted-foreground">{t("workflow.review.cancelledNoReview")}</p>
			</div>
		);
	}

	// Run is succeeded from here
	const isRunSucceeded = latestRun.status === "succeeded";
	const isTaskReview = task.status === "review";
	const hasNoReview = !review;
	const isReviewPending = review?.status === "pending";
	const isReviewPassed = review?.status === "passed";
	const isReviewRejected = review?.status === "rejected";

	// Scenario C: Succeeded + Task REVIEW + no Review → show Create button
	const canCreateReview = isRunSucceeded && isTaskReview && hasNoReview;

	// Scenario F: Review REJECTED + Run SUCCEEDED + Task READY + non-empty issues → can retry
	const canRetry = isRunSucceeded && isReviewRejected && task.status === "ready" && (review?.issues ?? "").trim().length > 0;

	if (reviewsQuery.isLoading) {
		return (
			<div className="flex flex-col gap-2 py-2">
				<Skeleton className="h-16 w-full" />
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-3 py-2">
			{/* Scenario C: Create Review */}
			{canCreateReview && (
				<Button
					size="sm"
					onClick={() => setShowCreateDialog(true)}
					disabled={createReview.isPending}
					data-testid="create-review"
				>
					{t("workflow.review.createHumanReview")}
				</Button>
			)}

			{/* Review details */}
			{review && (
				<div className="rounded-md border p-3 text-xs">
					<div className="flex items-center justify-between">
						<span className="font-medium">{t("workflow.review.reviewStatus")}</span>
						<StatusBadge status={review.status} entity="review" />
					</div>

					{review.summary && (
						<div className="mt-2">
							<p className="text-muted-foreground">{t("workflow.review.summary")}</p>
							<p className="mt-0.5 whitespace-pre-wrap">{review.summary}</p>
						</div>
					)}

					{review.issues && (
						<div className="mt-2">
							<p className="text-muted-foreground">{t("workflow.review.issues")}</p>
							<p className="mt-0.5 whitespace-pre-wrap text-destructive">{review.issues}</p>
						</div>
					)}

					<div className="mt-2 grid grid-cols-2 gap-1 text-muted-foreground">
						<span>{t("workflow.review.source")}</span>
						<span>{review.source === "human" ? t("workflow.review.sourceHuman") : t("workflow.review.sourceAI")}</span>
						<span>{t("workflow.review.createdAt")}</span>
						<span>{new Date(review.createdAt).toLocaleString()}</span>
						{review.completedAt && (
							<>
								<span>{t("workflow.review.completedAt")}</span>
								<span>{new Date(review.completedAt).toLocaleString()}</span>
							</>
						)}
					</div>

					{/* Scenario D: Pending → Pass/Reject buttons */}
					{isReviewPending && (
						<div className="mt-3 flex gap-2">
							<Button
								size="sm"
								onClick={() => passReview.mutate()}
								disabled={passReview.isPending}
								data-testid="pass-review"
							>
								{t("workflow.review.pass")}
							</Button>
							<Button
								size="sm"
								variant="secondary"
								className="text-destructive"
								onClick={() => setShowRejectDialog(true)}
								disabled={rejectReview.isPending}
								data-testid="reject-review"
							>
								{t("workflow.review.reject")}
							</Button>
						</div>
					)}

					{/* Scenario E: Passed → readonly */}
					{isReviewPassed && (
						<p className="mt-2 text-xs text-green-600">{t("workflow.review.passedMessage")}</p>
					)}

					{/* Scenario F: Rejected → readonly + Retry if eligible */}
					{isReviewRejected && (
						<>
							<p className="mt-2 text-xs text-destructive">{t("workflow.review.rejectedMessage")}</p>
							{canRetry && (
								<Button
									size="sm"
									variant="outline"
									className="mt-2"
									onClick={() => setShowRetryMenu(true)}
									disabled={createRetry.isPending}
									data-testid="retry-run"
								>
									{t("workflow.review.retry")}
								</Button>
							)}
						</>
					)}
				</div>
			)}

			{/* No run succeeded and no review context */}
			{!isRunSucceeded && !review && (
				<p className="text-center text-xs text-muted-foreground">{t("workflow.empty.noReviews")}</p>
			)}

			{/* Error states */}
			{createReview.isError && (
				<p className="text-xs text-destructive">{createReview.error?.message}</p>
			)}
			{passReview.isError && (
				<p className="text-xs text-destructive">{passReview.error?.message}</p>
			)}
			{rejectReview.isError && (
				<p className="text-xs text-destructive">{rejectReview.error?.message}</p>
			)}
			{createRetry.isError && (
				<p className="text-xs text-destructive">{createRetry.error?.message}</p>
			)}

			{/* Dialogs */}
			<CreateReviewDialog
				open={showCreateDialog}
				onOpenChange={setShowCreateDialog}
				onSubmit={(input) => {
					createReview.mutate(input, {
						onSuccess: () => setShowCreateDialog(false),
					});
				}}
				isPending={createReview.isPending}
				error={createReview.isError ? createReview.error?.message : null}
			/>

			<RejectDialog
				open={showRejectDialog}
				onOpenChange={setShowRejectDialog}
				onSubmit={(issues) => {
					rejectReview.mutate(issues, {
						onSuccess: () => setShowRejectDialog(false),
					});
				}}
				isPending={rejectReview.isPending}
				error={rejectReview.isError ? rejectReview.error?.message : null}
			/>

			<RetryMenu
				open={showRetryMenu}
				onOpenChange={setShowRetryMenu}
				onSelect={(mode) => {
					createRetry.mutate(mode, {
						onSuccess: () => setShowRetryMenu(false),
					});
				}}
				isPending={createRetry.isPending}
			/>
		</div>
	);
}
