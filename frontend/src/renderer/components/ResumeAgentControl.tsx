import { useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Play } from "lucide-react";
import { aoBridge } from "../lib/bridge";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { useCanResumeAgent } from "../hooks/useCanResumeAgent";
import { useEditorHandoffState } from "../hooks/useEditorHandoff";
import { usesPreviewWorkspaceData as usePreviewData } from "../lib/preview-mode";
import { cn } from "../lib/utils";
import type { WorkspaceSession } from "../types/workspace";
import { Button } from "./ui/button";

/**
 * Relaunches an agent that exited inside a still-live session. Distinct from
 * restore, which revives a TERMINATED row: this keeps the worktree, terminal
 * identity, and native conversation. Self-gating, so the inspector Summary and
 * the terminal strip can both mount it unconditionally.
 */
export function ResumeAgentControl({
	className,
	containerClassName,
	session,
}: {
	className?: string;
	containerClassName?: string;
	session: WorkspaceSession;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const canResume = useCanResumeAgent(session);
	const mutationKey = ["resume-agent", session.id] as const;
	const workspaceHandoff = useEditorHandoffState(session.id, {
		sessionCreatedAt: session.createdAt,
		sessionTerminated: session.isTerminated,
	});
	const resume = useMutation({
		mutationKey,
		mutationFn: async () => {
			if (usePreviewData) return;
			const { data, error, response } = await apiClient.POST("/api/v1/sessions/{sessionId}/resume-agent", {
				params: { path: { sessionId: session.id } },
			});
			if (error) throw new Error(apiErrorMessage(error, `Failed to resume agent (${response.status})`));
			return data;
		},
		onSuccess: async (data) => {
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			if (data?.resumeMode === "saved_prompt") {
				void aoBridge.notifications
					.show({
						id: `resume-agent-fallback:${session.id}:${Date.now()}`,
						title: t("inspector.startedFromPrompt"),
						body: t("inspector.resumeFallbackBody"),
					})
					.catch((err) => {
						console.warn("Unable to show resume fallback notification", err);
					});
			}
		},
	});
	const sharedResumeState = useMutationState({
		filters: { exact: true, mutationKey },
		select: (mutation) => ({ error: mutation.state.error, status: mutation.state.status }),
	}).at(-1);

	// Cloud sessions re-provision through the control plane (useRestoreSession),
	// not this local-daemon route — the local daemon has never heard of them and
	// would answer "Unknown session".
	if (!canResume || workspaceHandoff.data?.workspaceAvailable !== true) return null;

	const resumeError = resume.error ?? sharedResumeState?.error;
	const error = resumeError instanceof Error ? resumeError.message : null;
	const isPending = resume.isPending || sharedResumeState?.status === "pending";
	const control = (
		<>
			<Button
				className={cn("shrink-0", className)}
				disabled={isPending}
				onClick={() => resume.mutate()}
				size="sm"
				type="button"
				variant="outline"
			>
				<Play className="size-icon-sm" aria-hidden="true" />
				{isPending ? t("inspector.resumingAgent") : t("inspector.resumeAgent")}
			</Button>
			{error ? (
				<p className="mt-2 text-2xs leading-normal text-error" role="status">
					{error}
				</p>
			) : null}
		</>
	);
	return containerClassName ? <div className={containerClassName}>{control}</div> : control;
}
