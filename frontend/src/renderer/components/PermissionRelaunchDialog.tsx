import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, RotateCw } from "lucide-react";
import { useEffect, useState } from "react";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

type PermissionRelaunchDialogProps = {
	open: boolean;
	projectId: string;
	onOpenChange: (open: boolean) => void;
};

export function PermissionRelaunchDialog({ open, projectId, onOpenChange }: PermissionRelaunchDialogProps) {
	const queryClient = useQueryClient();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string>();
	const [result, setResult] = useState<{ relaunched: number; failed: number }>();
	const affectedQuery = useQuery({
		queryKey: ["permission-relaunch-affected", projectId],
		enabled: open,
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}/permission-relaunch/affected", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
	});
	const count = affectedQuery.data?.count ?? 0;

	useEffect(() => {
		if (affectedQuery.data && count === 0) onOpenChange(false);
	}, [affectedQuery.data, count, onOpenChange]);

	const relaunch = async () => {
		setBusy(true);
		setError(undefined);
		try {
			const { data, error } = await apiClient.POST("/api/v1/projects/{id}/permission-relaunch", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			setResult({ relaunched: data?.relaunched ?? 0, failed: data?.failed ?? 0 });
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			if ((data?.failed ?? 0) === 0) onOpenChange(false);
		} catch (caught) {
			setError(caught instanceof Error ? caught.message : "Could not relaunch sessions");
		} finally {
			setBusy(false);
		}
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!busy) onOpenChange(nextOpen);
			}}
		>
			<DialogContent className={`${settingsDialogContentClass} w-dialog-orchestrator`} showCloseButton={!busy}>
				<DialogHeader className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">Relaunch sessions with the new permission mode?</DialogTitle>
					<DialogDescription className="text-control leading-5 text-settings-muted">
						The new setting applies to future sessions. Relaunching restarts these agents in place; their branches and
						uncommitted work are kept, but any in-progress turn is interrupted.
					</DialogDescription>
				</DialogHeader>
				<div className={settingsDialogBodyClass}>
					{affectedQuery.isLoading ? <Loader2 className="mx-auto size-5 animate-spin text-settings-muted" /> : null}
					{affectedQuery.isError ? (
						<p className="text-sm text-error">{affectedQuery.error instanceof Error ? affectedQuery.error.message : "Could not load sessions"}</p>
					) : null}
					{affectedQuery.data && !result ? (
						<ul className="space-y-2 text-sm">
							{affectedQuery.data.affected.map((session) => (
								<li key={session.sessionId} className="flex items-center justify-between gap-3">
									<span className="truncate font-medium">{session.title}</span>
									<span className="shrink-0 text-settings-muted">{session.fromMode} → {session.toMode}</span>
								</li>
							))}
						</ul>
					) : null}
					{result ? <p className="text-sm text-settings-muted">Relaunched {result.relaunched}; failed {result.failed}.</p> : null}
					{error ? <p className="text-sm text-error">{error}</p> : null}
				</div>
				<DialogFooter className={settingsDialogFooterClass}>
					<Button type="button" variant="footer" disabled={busy} onClick={() => onOpenChange(false)}>
						{result ? "Close" : "Cancel"}
					</Button>
					{!result && count > 0 ? (
						<Button type="button" variant="footer-primary" disabled={busy || affectedQuery.isLoading} onClick={relaunch}>
							{busy ? <Loader2 className="size-3.5 animate-spin" /> : <RotateCw className="size-3.5" />}
							Relaunch {count} session{count === 1 ? "" : "s"}
						</Button>
					) : null}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
