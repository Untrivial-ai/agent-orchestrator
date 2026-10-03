import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { Button } from "./ui/button";

type Row = { name: string; value: string; visible: boolean };
type Project = components["schemas"]["Project"];

const rowsFromEnv = (env?: Record<string, string>): Row[] =>
	Object.entries(env ?? {}).map(([name, value]) => ({ name, value, visible: false }));

function parsePastedEnv(text: string): { rows: Row[]; invalidLine?: number } {
	const rows: Row[] = [];
	const names = new Set<string>();
	for (const [index, source] of text.split(/\r?\n/).entries()) {
		const line = source.trim();
		if (!line || line.startsWith("#")) continue;
		const match = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line);
		if (!match) return { rows: [], invalidLine: index + 1 };
		const name = match[1];
		const folded = name.toUpperCase();
		if (folded.startsWith("AO_") || names.has(folded)) return { rows: [], invalidLine: index + 1 };
		names.add(folded);
		let value = match[2].trim();
		if (value.startsWith('"') || value.startsWith("'")) {
			const quoted = /^(['"])(.*)\1(?:\s*#.*)?$/.exec(value);
			if (!quoted) return { rows: [], invalidLine: index + 1 };
			value = quoted[2];
			if (quoted[1] === '"') value = value.replace(/\\n/g, "\n").replace(/\\r/g, "\r");
		} else {
			value = value.replace(/\s+#.*$/, "").trimEnd();
		}
		if (value.includes("\0")) return { rows: [], invalidLine: index + 1 };
		rows.push({ name, value, visible: false });
	}
	return { rows };
}

export function ProjectEnvironmentSettings({ projectId, onSaveState }: { projectId: string; onSaveState?: (state: ProjectSettingsSaveState) => void }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
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
	return <VariablesEditor key={projectId} projectId={projectId} initial={query.data.config?.env} onSaveState={onSaveState} onSaved={() => {
		void queryClient.invalidateQueries({ queryKey: ["project", projectId] });
		void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
	}} />;
}

function VariablesEditor({ projectId, initial, onSaveState, onSaved }: {
	projectId: string;
	initial?: Record<string, string>;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
	onSaved: () => void;
}) {
	const { t } = useTranslation();
	const [rows, setRows] = useState(() => rowsFromEnv(initial));
	const [saved, setSaved] = useState(() => JSON.stringify(initial ?? {}));
	const [error, setError] = useState<string | null>(null);
	const [savedAt, setSavedAt] = useState(false);
	const [pasteOpen, setPasteOpen] = useState(false);
	const [pasteText, setPasteText] = useState("");
	const [importedCount, setImportedCount] = useState<number | null>(null);
	const dirty = JSON.stringify(Object.fromEntries(rows.map(({ name, value }) => [name, value]))) !== saved;
	const mutation = useMutation({
		mutationFn: async (env: Record<string, string>) => {
			// Refresh before the whole-config PUT so this page cannot erase changes made elsewhere.
			const current = await apiClient.GET("/api/v1/projects/{id}", { params: { path: { id: projectId } } });
			if (current.error) throw new Error(apiErrorMessage(current.error));
			if (current.data?.status !== "ok" || !current.data.project) throw new Error(t("settings.project.degraded"));
			const project = current.data.project as Project;
			const { error: updateError } = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
				body: { displayName: project.name, config: { ...project.config, env } },
			});
			if (updateError) throw new Error(apiErrorMessage(updateError));
			return env;
		},
		onSuccess: (env) => {
			setSaved(JSON.stringify(env));
			setSavedAt(true);
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
	const update = (next: Row[]) => { setRows(next); setError(null); setSavedAt(false); setImportedCount(null); };
	const importPasted = () => {
		const parsed = parsePastedEnv(pasteText);
		if (parsed.invalidLine || parsed.rows.length === 0) {
			setError(t("settings.project.envPasteInvalid", { line: parsed.invalidLine ?? 1 }));
			return;
		}
		const next = [...rows];
		for (const row of parsed.rows) {
			const existing = next.findIndex((item) => item.name.toUpperCase() === row.name.toUpperCase());
			if (existing < 0) next.push(row);
			else next[existing] = { ...row, name: next[existing].name };
		}
		update(next);
		setImportedCount(parsed.rows.length);
		setPasteText("");
		setPasteOpen(false);
	};
	const save = () => {
		if (pasteOpen && pasteText.trim()) {
			setError(t("settings.project.envPastePending"));
			return;
		}
		const env: Record<string, string> = {};
		const names = new Set<string>();
		for (const row of rows) {
			const name = row.name;
			const folded = name.toUpperCase();
			if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name) || names.has(folded)) {
				setError(t("settings.project.envInvalid"));
				return;
			}
			names.add(folded);
			env[name] = row.value;
		}
		setError(null);
		mutation.mutate(env);
	};
	return <form id="project-settings-form" className="space-y-5 pb-6" onSubmit={(event) => { event.preventDefault(); save(); }}>
		<div>
			<h2 className="text-base font-semibold text-settings-label">{t("settings.project.environment")}</h2>
			<p className="mt-2 text-sm text-settings-muted">{t("settings.project.environmentHint")}</p>
		</div>
		<div className="space-y-3">
			{rows.map((row, index) => <div className="flex flex-wrap items-center gap-2" key={index}>
				<input aria-label={`${t("settings.project.envName")} ${index + 1}`} className="settings-field-control min-w-32 flex-1" placeholder={t("settings.project.envName")} value={row.name} onChange={(event) => update(rows.map((item, i) => i === index ? { ...item, name: event.target.value } : item))} />
				<input aria-label={`${t("settings.project.envValue")} ${index + 1}`} autoComplete="off" className="settings-field-control min-w-32 flex-1" placeholder={t("settings.project.envValue")} type={row.visible ? "text" : "password"} value={row.value} onChange={(event) => update(rows.map((item, i) => i === index ? { ...item, value: event.target.value } : item))} />
				<button aria-label={row.visible ? t("settings.project.hideVariable") : t("settings.project.showVariable")} className="rounded p-2 text-settings-muted hover:text-settings-label focus-visible:ring-2 focus-visible:ring-ring" onClick={() => update(rows.map((item, i) => i === index ? { ...item, visible: !item.visible } : item))} type="button">{row.visible ? <EyeOff size={16} /> : <Eye size={16} />}</button>
				<button aria-label={t("settings.project.removeVariable", { name: row.name || index + 1 })} className="rounded p-2 text-settings-muted hover:text-error focus-visible:ring-2 focus-visible:ring-ring" onClick={() => update(rows.filter((_, i) => i !== index))} type="button"><Trash2 size={16} /></button>
			</div>)}
			<div className="flex flex-wrap items-center gap-4">
				<button className="flex items-center gap-1 text-sm text-settings-label underline" onClick={() => update([...rows, { name: "", value: "", visible: false }])} type="button"><Plus size={16} />{t("settings.project.addVariable")}</button>
				<button className="text-sm text-settings-label underline" onClick={() => { setPasteOpen(!pasteOpen); setError(null); }} type="button">{t("settings.project.pasteVariables")}</button>
			</div>
		</div>
		{pasteOpen && <div className="space-y-2">
			<label className="text-sm font-medium text-settings-label" htmlFor="project-env-paste">{t("settings.project.pasteVariables")}</label>
			<p className="text-sm text-settings-muted">{t("settings.project.envPasteHint")}</p>
			<textarea autoComplete="off" className="settings-field-control min-h-32 w-full font-mono text-sm" id="project-env-paste" onChange={(event) => setPasteText(event.target.value)} spellCheck={false} value={pasteText} />
			<div className="flex justify-end gap-2">
				<Button onClick={() => { setPasteOpen(false); setPasteText(""); setError(null); }} type="button" variant="outline">{t("settings.project.envPasteCancel")}</Button>
				<Button disabled={!pasteText.trim()} onClick={importPasted} type="button">{t("settings.project.importVariables")}</Button>
			</div>
		</div>}
		{importedCount !== null && <p role="status" className="text-sm text-settings-muted">{t("settings.project.envImported", { count: importedCount })}</p>}
		{error && <p role="alert" className="text-sm text-error">{error}</p>}
		{mutation.isError && <p role="alert" className="text-sm text-error">{mutation.error instanceof Error ? mutation.error.message : t("settings.project.saveFailed")}</p>}
		<div className="flex justify-end"><Button disabled={!dirty || mutation.isPending} type="submit">{t("settings.project.saveChanges")}</Button></div>
	</form>;
}
