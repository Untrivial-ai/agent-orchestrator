import { CheckCircle2, Cookie, History as HistoryIcon, LoaderCircle, TriangleAlert, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AoBridge } from "../../../preload";
import type {
	BrowserImportProgress,
	BrowserImportResult,
	BrowserImportSource,
	BrowserImportWarning,
} from "../../../shared/browser-profile-import";
import { aoBridge } from "../../lib/bridge";
import { appI18n, type MessageKey } from "../../i18n";
import { Button } from "../ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";

type ImportBridge = AoBridge["browserProfiles"];
type View = "form" | "running" | "result";

export function BrowserImportDialog({
	open,
	onOpenChange,
	onImported,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onImported: () => void;
}) {
	const { t } = useTranslation();
	const bridge = (aoBridge as Partial<AoBridge>).browserProfiles as ImportBridge | undefined;
	const [view, setView] = useState<View>("form");
	const [sources, setSources] = useState<BrowserImportSource[]>([]);
	const [sourceId, setSourceId] = useState("");
	const [selectedProfileIds, setSelectedProfileIds] = useState<string[]>([]);
	const [includeCookies, setIncludeCookies] = useState(true);
	const [includeHistory, setIncludeHistory] = useState(true);
	const [destinationMode, setDestinationMode] = useState<"separate" | "merge">("separate");
	const [destinationNames, setDestinationNames] = useState<Record<string, string>>({});
	const [mergeName, setMergeName] = useState("");
	const [loading, setLoading] = useState(false);
	const [error, setError] = useState("");
	const [progress, setProgress] = useState<BrowserImportProgress | null>(null);
	const [result, setResult] = useState<BrowserImportResult | null>(null);

	const source = sources.find((candidate) => candidate.id === sourceId);
	const selectedProfiles = source?.profiles.filter((profile) => selectedProfileIds.includes(profile.id)) ?? [];
	const canClose = view !== "running";
	const requestId = progress?.requestId;

	useEffect(() => {
		if (!open) return;
		setView("form");
		setSources([]);
		setSourceId("");
		setSelectedProfileIds([]);
		setIncludeCookies(true);
		setIncludeHistory(true);
		setDestinationMode("separate");
		setDestinationNames({});
		setMergeName("");
		setError("");
		setProgress(null);
		setResult(null);
		if (!bridge) {
			setError(t("settings.browserImport.unavailable"));
			return;
		}
		setLoading(true);
		void bridge.discoverImportSources().then(
			(discovery) => {
				setSources(discovery.sources);
				const first = discovery.sources[0];
				if (first) applySourceDefaults(first, setSourceId, setSelectedProfileIds, setDestinationNames, setMergeName, setDestinationMode);
			},
			(reason) => setError(reason instanceof Error ? reason.message : t("settings.browserImport.discoveryFailed")),
		).finally(() => setLoading(false));
	}, [bridge, open, t]);

	useEffect(() => {
		if (!bridge || !open) return;
		return bridge.onImportProgress((next) => {
			if (requestId && next.requestId !== requestId) return;
			setProgress(next);
		});
	}, [bridge, open, requestId]);

	const progressPercent = useMemo(() => {
		if (!progress || progress.total < 1) return 8;
		return Math.max(8, Math.min(100, Math.round((progress.completed / progress.total) * 100)));
	}, [progress]);

	const selectSource = (id: string) => {
		const next = sources.find((candidate) => candidate.id === id);
		if (!next) return;
		applySourceDefaults(next, setSourceId, setSelectedProfileIds, setDestinationNames, setMergeName, setDestinationMode);
		setError("");
	};

	const selectProfiles = (ids: string[]) => {
		if (!source) return;
		setSelectedProfileIds(ids);
		setDestinationNames((current) => Object.fromEntries(
			source.profiles
				.filter((profile) => ids.includes(profile.id))
				.map((profile) => [profile.id, current[profile.id] ?? suggestedName(source.name, profile.name)]),
		));
		if (ids.length <= 1) setDestinationMode("merge");
		else if (selectedProfileIds.length <= 1) setDestinationMode("separate");
		setError("");
	};

	const startImport = async () => {
		if (!bridge || !source || selectedProfiles.length === 0 || (!includeCookies && !includeHistory)) return;
		const id = crypto.randomUUID();
		setProgress({ requestId: id, phase: "preparing", completed: 0, total: selectedProfiles.length });
		setView("running");
		setError("");
		try {
			const imported = await bridge.import({
				requestId: id,
				sourceId: source.id,
				profileIds: selectedProfiles.map((profile) => profile.id),
				includeCookies,
				includeHistory,
				destination:
					destinationMode === "merge"
						? { mode: "merge", name: mergeName.trim() }
						: {
								mode: "separate",
								names: Object.fromEntries(
									selectedProfiles.map((profile) => [profile.id, destinationNames[profile.id]?.trim() ?? ""]),
								),
							},
			});
			setResult(imported);
			setView("result");
		} catch (reason) {
			setError(importFailureMessage(reason, source.name));
			setView("form");
		} finally {
			onImported();
		}
	};

	const namesValid =
		destinationMode === "merge"
			? mergeName.trim().length > 0
			: selectedProfiles.every((profile) => (destinationNames[profile.id]?.trim().length ?? 0) > 0);

	return (
		<Dialog open={open} onOpenChange={(next) => canClose && onOpenChange(next)}>
			<DialogContent className={settingsDialogContentClass} showCloseButton={false}>
				<DialogClose asChild>
					<button
						aria-label={t("common.close")}
						className="settings-dialog-close-button settings-close-button"
						disabled={!canClose}
						type="button"
					>
						<X aria-hidden="true" className="size-5" />
					</button>
				</DialogClose>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{t("settings.browserImport.title")}</DialogTitle>
					<DialogDescription>{t("settings.browserImport.description")}</DialogDescription>
				</div>

				<div className={settingsDialogBodyClass}>
					{view === "form" ? (
						<ImportForm
							destinationMode={destinationMode}
							destinationNames={destinationNames}
							error={error}
							includeCookies={includeCookies}
							includeHistory={includeHistory}
							loading={loading}
							mergeName={mergeName}
							onSelectProfiles={selectProfiles}
							onSelectSource={selectSource}
							profiles={selectedProfiles}
							selectedProfileIds={selectedProfileIds}
							setDestinationMode={setDestinationMode}
							setDestinationNames={setDestinationNames}
							setIncludeCookies={(value) => { setIncludeCookies(value); setError(""); }}
							setIncludeHistory={(value) => { setIncludeHistory(value); setError(""); }}
							setMergeName={setMergeName}
							source={source}
							sourceId={sourceId}
							sources={sources}
						/>
					) : null}
					{view === "running" ? (
						<div className="flex flex-1 flex-col items-center justify-center gap-5 py-12 text-center">
							<LoaderCircle aria-hidden="true" className="size-8 animate-spin text-accent" />
							<div>
								<p className="font-semibold">{t(`settings.browserImport.progress.${progress?.phase ?? "preparing"}`)}</p>
								<p className="mt-1 text-xs text-muted-foreground">{t("settings.browserImport.progress.keepOpen")}</p>
							</div>
							<div className="h-1.5 w-full max-w-sm overflow-hidden rounded-full bg-muted">
								<div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${progressPercent}%` }} />
							</div>
						</div>
					) : null}
					{view === "result" && result ? <ResultStep result={result} /> : null}
				</div>

				<div className={settingsDialogFooterClass}>
					<div className="flex-1" />
					{view !== "running" ? (
						<DialogClose asChild>
							<Button type="button" variant="footer">
								{view === "result" ? t("settings.browserImport.done") : t("confirm.cancel")}
							</Button>
						</DialogClose>
					) : null}
					{view === "form" ? (
						<Button
							disabled={loading || !source || selectedProfiles.length === 0 || (!includeCookies && !includeHistory) || !namesValid}
							onClick={() => void startImport()}
							type="button"
							variant="footer-primary"
						>
							{t("settings.browserImport.start")}
						</Button>
					) : null}
				</div>
			</DialogContent>
		</Dialog>
	);
}

function importFailureMessage(reason: unknown, browser: string): string {
	const fallback = appI18n.t("settings.browserImport.failed");
	const raw = reason instanceof Error ? reason.message : typeof reason === "string" ? reason : fallback;
	const message = raw
		.replace(/^Error invoking remote method '[^']+':\s*(?:Error:\s*)?/i, "")
		.replace(/^Error:\s*/i, "")
		.trim();
	if (/unable to open database file|database is locked|SQLITE_(?:BUSY|CANTOPEN|LOCKED)|\b(?:EACCES|EBUSY|EPERM)\b/i.test(message)) {
		return appI18n.t("settings.browserImport.sourceDatabaseUnavailable", { browser });
	}
	return message || fallback;
}

function ImportForm({
	sources,
	source,
	sourceId,
	profiles,
	selectedProfileIds,
	loading,
	error,
	includeCookies,
	includeHistory,
	destinationMode,
	destinationNames,
	mergeName,
	onSelectSource,
	onSelectProfiles,
	setIncludeCookies,
	setIncludeHistory,
	setDestinationMode,
	setDestinationNames,
	setMergeName,
}: {
	sources: BrowserImportSource[];
	source?: BrowserImportSource;
	sourceId: string;
	profiles: BrowserImportSource["profiles"];
	selectedProfileIds: string[];
	loading: boolean;
	error: string;
	includeCookies: boolean;
	includeHistory: boolean;
	destinationMode: "separate" | "merge";
	destinationNames: Record<string, string>;
	mergeName: string;
	onSelectSource: (id: string) => void;
	onSelectProfiles: (ids: string[]) => void;
	setIncludeCookies: (value: boolean) => void;
	setIncludeHistory: (value: boolean) => void;
	setDestinationMode: (value: "separate" | "merge") => void;
	setDestinationNames: React.Dispatch<React.SetStateAction<Record<string, string>>>;
	setMergeName: (value: string) => void;
}) {
	const { t } = useTranslation();
	if (loading) return (
		<div className="flex items-center gap-2 py-4 text-sm text-muted-foreground">
			<LoaderCircle aria-hidden="true" className="size-4 animate-spin" />
			{t("settings.browserImport.detecting")}
		</div>
	);
	if (sources.length === 0) {
		return error
			? <p className="text-sm text-destructive" role="alert">{error}</p>
			: <p className="text-sm text-muted-foreground">{t("settings.browserImport.noneFound")}</p>;
	}
	return (
		<div className="space-y-5">
			<section className="space-y-2">
				<h3 className="text-sm font-semibold" id="browser-import-source-label">{t("settings.browserImport.from")}</h3>
				<Select onValueChange={onSelectSource} value={sourceId}>
					<SelectTrigger
						aria-disabled={sources.length === 1}
						aria-labelledby="browser-import-source-label"
						className={`w-full border-border bg-background ${sources.length === 1 ? "pointer-events-none [&>svg]:hidden" : ""}`}
						tabIndex={sources.length === 1 ? -1 : undefined}
					>
						<SelectValue>{source?.name}</SelectValue>
					</SelectTrigger>
					<SelectContent align="start" className="w-[var(--radix-select-trigger-width)]">
						{sources.map((candidate) => {
							const selected = candidate.id === sourceId;
							return (
								<SelectItem className={selected ? "bg-settings-menu-selected text-foreground" : ""} key={candidate.id} value={candidate.id}>
									<span className="flex w-full min-w-0 items-center gap-2">
										<span className="min-w-0 flex-1 truncate">{candidate.name}</span>
										<span className="text-xs text-muted-foreground">{t("settings.browserImport.profileCount", { count: candidate.profiles.length })}</span>
										{selected ? <CheckCircle2 aria-hidden="true" className="size-4 shrink-0 text-accent" /> : null}
									</span>
								</SelectItem>
							);
						})}
					</SelectContent>
				</Select>
			</section>

			{source ? (
				<>
					<ProfilesStep onChange={onSelectProfiles} selected={selectedProfileIds} source={source} />
					<OptionsStep
						destinationMode={destinationMode}
						destinationNames={destinationNames}
						error={error}
						includeCookies={includeCookies}
						includeHistory={includeHistory}
						mergeName={mergeName}
						profiles={profiles}
						setDestinationMode={setDestinationMode}
						setDestinationNames={setDestinationNames}
						setIncludeCookies={setIncludeCookies}
						setIncludeHistory={setIncludeHistory}
						setMergeName={setMergeName}
						source={source}
					/>
				</>
			) : null}
		</div>
	);
}

function ProfilesStep({ source, selected, onChange }: { source: BrowserImportSource; selected: string[]; onChange: (ids: string[]) => void }) {
	const { t } = useTranslation();
	return (
		<section className="space-y-2">
			<p className="text-sm text-muted-foreground">{t("settings.browserImport.selectProfiles", { browser: source.name })}</p>
			<div className="grid gap-2">
				{source.profiles.map((profile) => {
					const checked = selected.includes(profile.id);
					return (
					<label className={`flex cursor-pointer items-center gap-3 rounded-lg border p-3 text-foreground transition-[background-color,border-color,box-shadow] ${checked ? "border-accent bg-settings-menu-selected shadow-sm ring-1 ring-accent/40" : "border-border hover:bg-interactive-hover"}`} key={profile.id}>
						<input
							checked={checked}
							className="size-4 accent-accent"
							onChange={(event) => onChange(event.target.checked ? [...selected, profile.id] : selected.filter((id) => id !== profile.id))}
							type="checkbox"
						/>
						<span className="text-sm font-medium">{profile.name}</span>
						{profile.default ? <span className="text-xs text-muted-foreground">{t("settings.browserImport.defaultProfile")}</span> : null}
					</label>
					);
				})}
			</div>
		</section>
	);
}

function OptionsStep({
	source,
	profiles,
	includeCookies,
	includeHistory,
	destinationMode,
	destinationNames,
	mergeName,
	error,
	setIncludeCookies,
	setIncludeHistory,
	setDestinationMode,
	setDestinationNames,
	setMergeName,
}: {
	source: BrowserImportSource;
	profiles: BrowserImportSource["profiles"];
	includeCookies: boolean;
	includeHistory: boolean;
	destinationMode: "separate" | "merge";
	destinationNames: Record<string, string>;
	mergeName: string;
	error: string;
	setIncludeCookies: (value: boolean) => void;
	setIncludeHistory: (value: boolean) => void;
	setDestinationMode: (value: "separate" | "merge") => void;
	setDestinationNames: React.Dispatch<React.SetStateAction<Record<string, string>>>;
	setMergeName: (value: string) => void;
}) {
	const { t } = useTranslation();
	return (
		<div className="space-y-5 border-t border-border pt-5">
			<section className="space-y-2">
				<h3 className="text-sm font-semibold">{t("settings.browserImport.dataTitle")}</h3>
				<label className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors ${includeCookies ? "border-accent/70 bg-settings-menu-selected" : "border-border hover:bg-interactive-hover"}`}>
					<Cookie aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-muted-foreground" />
					<span className="min-w-0 flex-1">
						<span className="block text-sm font-medium">{t("settings.browserImport.cookies")}</span>
						<span className="block text-xs text-muted-foreground">{t("settings.browserImport.cookiesDescription")}</span>
					</span>
					<input checked={includeCookies} className="mt-0.5 size-4 shrink-0 accent-accent" onChange={(event) => setIncludeCookies(event.target.checked)} type="checkbox" />
				</label>
				{source.cookieSupport !== "supported" ? (
					<p className="flex items-start gap-2 text-xs text-warning">
						<TriangleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
						{t(capabilityKey(source.cookieSupportReason))}
					</p>
				) : null}
				<label className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors ${includeHistory ? "border-accent/70 bg-settings-menu-selected" : "border-border hover:bg-interactive-hover"}`}>
					<HistoryIcon aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-muted-foreground" />
					<span className="min-w-0 flex-1">
						<span className="block text-sm font-medium">{t("settings.browserImport.history")}</span>
						<span className="block text-xs text-muted-foreground">{t("settings.browserImport.historyDescription")}</span>
					</span>
					<input checked={includeHistory} className="mt-0.5 size-4 shrink-0 accent-accent" onChange={(event) => setIncludeHistory(event.target.checked)} type="checkbox" />
				</label>
			</section>

			<section className="space-y-2">
				<h3 className="text-sm font-semibold">{t("settings.browserImport.destinationTitle")}</h3>
				{profiles.length > 1 ? (
					<div className="flex flex-wrap gap-4 text-sm">
						<label className="flex items-center gap-2"><input checked={destinationMode === "separate"} onChange={() => setDestinationMode("separate")} type="radio" />{t("settings.browserImport.keepSeparate")}</label>
						<label className="flex items-center gap-2"><input checked={destinationMode === "merge"} onChange={() => setDestinationMode("merge")} type="radio" />{t("settings.browserImport.merge")}</label>
					</div>
				) : null}
				{destinationMode === "merge" ? (
					<label className="grid gap-1.5 text-xs text-muted-foreground">
						{t("settings.browserImport.destinationName")}
						<Input maxLength={64} onChange={(event) => setMergeName(event.target.value)} value={mergeName} />
					</label>
				) : (
					<div className="grid gap-2">
						{profiles.map((profile) => (
							<label className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)] items-center gap-3 text-xs" key={profile.id}>
								<span className="truncate text-muted-foreground">{profile.name}</span>
								<Input maxLength={64} onChange={(event) => setDestinationNames((current) => ({ ...current, [profile.id]: event.target.value }))} value={destinationNames[profile.id] ?? ""} />
							</label>
						))}
					</div>
				)}
			</section>
			{error ? (
				<p className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive" role="alert">
					<TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
					{error}
				</p>
			) : null}
		</div>
	);
}

function ResultStep({ result }: { result: BrowserImportResult }) {
	const { t } = useTranslation();
	return (
		<div className="space-y-4">
			<div className="flex items-start gap-3 rounded-lg border border-success/30 bg-success/10 p-3">
				<CheckCircle2 aria-hidden="true" className="mt-0.5 size-5 text-success" />
				<div><p className="text-sm font-semibold">{t("settings.browserImport.complete")}</p><p className="text-xs text-muted-foreground">{t("settings.browserImport.completeDescription", { browser: result.sourceName })}</p></div>
			</div>
			{result.entries.map((entry) => (
				<div className="rounded-lg border border-border p-3" key={entry.destinationProfile.id}>
					<p className="text-sm font-semibold">{entry.destinationProfile.name}</p>
					<p className="mt-1 text-xs text-muted-foreground">{t("settings.browserImport.resultCounts", { cookies: entry.importedCookies, history: entry.importedHistoryEntries })}</p>
					{entry.skippedCookies > 0 ? <p className="mt-1 text-xs text-warning">{t("settings.browserImport.skippedCookies", { count: entry.skippedCookies })}</p> : null}
					{entry.warnings.map((warning) => <p className="mt-1 text-xs text-warning" key={warning.code}>{warningText(warning)}</p>)}
				</div>
			))}
			<p className="text-xs text-muted-foreground">{t("settings.browserImport.useProfile")}</p>
		</div>
	);
}

function warningText(warning: BrowserImportWarning): string {
	return appI18n.t(warningKey(warning.code), { count: warning.count ?? 0 });
}

function suggestedName(browser: string, profile: string): string {
	const name = profile.toLowerCase() === "default" ? browser : `${browser} — ${profile}`;
	return name.slice(0, 64);
}

function applySourceDefaults(
	source: BrowserImportSource,
	setSourceId: (value: string) => void,
	setSelectedProfileIds: (value: string[]) => void,
	setDestinationNames: (value: Record<string, string>) => void,
	setMergeName: (value: string) => void,
	setDestinationMode: (value: "separate" | "merge") => void,
) {
	const defaults = source.profiles.filter((profile) => profile.default);
	const selected = defaults.length > 0 ? defaults : source.profiles[0] ? [source.profiles[0]] : [];
	setSourceId(source.id);
	setSelectedProfileIds(selected.map((profile) => profile.id));
	setDestinationNames(Object.fromEntries(selected.map((profile) => [profile.id, suggestedName(source.name, profile.name)])));
	setMergeName(source.name);
	setDestinationMode(selected.length > 1 ? "separate" : "merge");
}

function capabilityKey(reason: BrowserImportSource["cookieSupportReason"]): MessageKey {
	if (reason === "chromium-encryption-partial") return "settings.browserImport.capability.chromium-encryption-partial";
	if (reason === "chromium-encryption-unsupported") return "settings.browserImport.capability.chromium-encryption-unsupported";
	return "settings.browserImport.cookiesDescription";
}

function warningKey(code: BrowserImportWarning["code"]): MessageKey {
	switch (code) {
		case "cookie-database-missing": return "settings.browserImport.warning.cookie-database-missing";
		case "history-database-missing": return "settings.browserImport.warning.history-database-missing";
		case "isolated-cookies-skipped": return "settings.browserImport.warning.isolated-cookies-skipped";
		case "cookie-limit-truncated": return "settings.browserImport.warning.cookie-limit-truncated";
		case "history-limit-truncated": return "settings.browserImport.warning.history-limit-truncated";
		case "encrypted-cookies-skipped": return "settings.browserImport.warning.encrypted-cookies-skipped";
		case "expired-cookies-skipped": return "settings.browserImport.warning.expired-cookies-skipped";
		case "invalid-cookies-skipped": return "settings.browserImport.warning.invalid-cookies-skipped";
		case "cookie-attributes-defaulted": return "settings.browserImport.warning.cookie-attributes-defaulted";
		case "cookie-write-failed": return "settings.browserImport.warning.cookie-write-failed";
	}
}
