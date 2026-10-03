import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { clearTerminateSessionState } from "../hooks/useTerminateSession";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { Button } from "./ui/button";

type Project = components["schemas"]["Project"];

export function ProjectCleanupSettings({ projectId, onSaveState }: { projectId: string; onSaveState?: (state: ProjectSettingsSaveState) => void }) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const query = useQuery({
		queryKey: ["project", projectId],
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}", { params: { path: { id: projectId } } });
			if (error) throw new Error(apiErrorMessage(error));
			if (data?.status !== "ok") throw new Error(t("settings.project.degraded"));
			return data.project as Project;
		},
	});
	if (query.isLoading) return <p className="text-sm text-settings-muted">{t("settings.project.loading")}</p>;
	if (query.isError || !query.data) return <p role="alert" className="text-sm text-error">{query.error instanceof Error ? query.error.message : t("settings.project.loadFailed")}</p>;
	if (query.data.kind === "scratch") return <p className="text-sm text-settings-muted">{t("settings.project.cleanupScratchUnavailable")}</p>;
	return <CleanupEditor key={projectId} project={query.data} onSaveState={onSaveState} onSaved={() => {
		void client.invalidateQueries({ queryKey: ["project", projectId] });
		void client.invalidateQueries({ queryKey: workspaceQueryKey });
	}} />;
}

function CleanupEditor({ project, onSaveState, onSaved }: { project: Project; onSaveState?: (state: ProjectSettingsSaveState) => void; onSaved: () => void }) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const initial = project.config?.preRemove ?? [];
	const [steps, setSteps] = useState<string[]>(() => initial.length ? [...initial] : [""]);
	const [saved, setSaved] = useState(() => JSON.stringify(initial));
	const [savedAt, setSavedAt] = useState(false);
	const commands = steps.filter((step) => step.trim());
	const dirty = JSON.stringify(commands) !== saved;
	const mutation = useMutation({
		mutationFn: async (preRemove: string[]) => {
			const current = await apiClient.GET("/api/v1/projects/{id}", { params: { path: { id: project.id } } });
			if (current.error) throw new Error(apiErrorMessage(current.error));
			if (current.data?.status !== "ok" || !current.data.project) throw new Error(t("settings.project.degraded"));
			const latest = current.data.project as Project;
			const { error } = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: project.id } },
				body: { displayName: latest.name, config: { ...latest.config, preRemove } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return preRemove;
	},
		onSuccess: (preRemove) => { setSaved(JSON.stringify(preRemove)); setSavedAt(true); onSaved(); },
	});
	const retry = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/sessions/cleanup", { params: { query: { project: project.id } } });
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: (result) => {
			for (const sessionId of [...result.cleaned, ...result.alreadyGone]) {
				clearTerminateSessionState(client, sessionId);
			}
			onSaved();
		},
	});
	useEffect(() => {
		onSaveState?.({
			phase: mutation.isError ? "failed" : mutation.isPending ? "saving" : dirty ? "pending" : savedAt ? "saved" : "idle",
			dirty,
			requestPending: mutation.isPending,
			error: mutation.error instanceof Error ? mutation.error.message : undefined,
		});
	}, [dirty, mutation.error, mutation.isError, mutation.isPending, onSaveState, savedAt]);
	return <form id="project-settings-form" className="space-y-5 pb-6" onSubmit={(event) => { event.preventDefault(); mutation.mutate(commands); }}>
		<div>
			<h2 className="text-base font-semibold text-settings-label">{t("settings.project.workspaceCleanup")}</h2>
			<p className="mt-2 text-sm text-settings-muted">{t("settings.project.cleanupHint")}</p>
			<p className="mt-1 text-xs text-settings-muted">{t("settings.project.cleanupShellHint")}</p>
		</div>
		<div className="rounded-md border border-border p-3 text-xs text-settings-muted">
			<p className="font-medium text-settings-label">{t("settings.project.cleanupPaths")}</p>
			<p><code>{"AO_SOURCE_TREE_PATH"}</code> — {t("settings.project.cleanupSourcePath")}</p>
			<p><code>{"AO_WORKTREE_PATH"}</code> — {t("settings.project.cleanupWorktreePath")}</p>
		</div>
		<div className="space-y-4">
			{steps.map((step, index) => <div key={index}>
				<div className="mb-1 flex items-center justify-between"><label className="text-sm font-medium text-settings-label" htmlFor={`cleanup-step-${index}`}>{t("settings.project.cleanupStep", { number: index + 1 })}</label>
					<button aria-label={t("settings.project.removeCleanupStep", { number: index + 1 })} className="rounded p-1 text-settings-muted hover:text-error focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setSteps(steps.length === 1 ? [""] : steps.filter((_, i) => i !== index))} type="button"><Trash2 size={16} /></button></div>
				<textarea className="settings-field-control min-h-24 w-full font-mono text-xs" id={`cleanup-step-${index}`} spellCheck={false} value={step} onChange={(event) => { setSteps(steps.map((item, i) => i === index ? event.target.value : item)); setSavedAt(false); }} />
			</div>)}
			<button className="flex items-center gap-1 text-sm text-settings-label underline" onClick={() => setSteps([...steps, ""])} type="button"><Plus size={16} />{t("settings.project.addCleanupStep")}</button>
		</div>
		{mutation.isError && <p role="alert" className="text-sm text-error">{mutation.error instanceof Error ? mutation.error.message : t("settings.project.saveFailed")}</p>}
		<div className="flex justify-end"><Button disabled={!dirty || mutation.isPending} type="submit">{t("settings.project.saveChanges")}</Button></div>
		<div className="space-y-2 border-t border-border pt-4">
			<p className="text-sm text-settings-muted">{t("settings.project.cleanupRetryHint")}</p>
			<Button disabled={dirty || mutation.isPending || retry.isPending} onClick={() => retry.mutate()} type="button" variant="outline">{t("settings.project.retryCleanup")}</Button>
			{retry.isError && <p role="alert" className="text-sm text-error">{retry.error instanceof Error ? retry.error.message : t("settings.project.cleanupRetryFailed")}</p>}
			{retry.data && <div role="status" className="space-y-1 text-sm text-settings-muted">
				<p>{t("settings.project.cleanupRetryResult", { cleaned: retry.data.cleaned.length, skipped: retry.data.skipped.length })}</p>
				{retry.data.skipped.map((item) => <p key={item.sessionId}>{item.sessionId}: {item.reason}</p>)}
			</div>}
		</div>
	</form>;
}
